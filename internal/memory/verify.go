package memory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
)

// Verify checks the project revision chain (contiguous project sequence,
// previous_hash links, recomputed revision hashes, matching chain head) and
// that every memory matches its revision history: contiguous revisions,
// recomputed content hashes, legal status sequence,
// unchanged immutable fields, a current row equal to the latest revision, and
// consistent supersede and contest references inside the project. It reads
// one read-only snapshot, so concurrent writers neither block it nor cause
// false failures. Defects wrap ErrIntegrity.
func (store *Store) Verify(ctx context.Context, projectID string) error {
	if err := validateIdentifier("project_id", projectID); err != nil {
		return fmt.Errorf("verify memories: %w", err)
	}
	if err := store.verifyTx(ctx, projectID); err != nil {
		return fmt.Errorf("verify memories in project %q: %w", projectID, err)
	}
	return nil
}

func (store *Store) verifyTx(ctx context.Context, projectID string) error {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	return verifyProject(ctx, tx, projectID, true)
}

// verifyProject runs every check inside the caller's transaction. Without
// withChain it reads the schema v1 layout and skips the chain checks.
func verifyProject(ctx context.Context, tx *sql.Tx, projectID string, withChain bool) error {
	ordered, err := loadRevisions(ctx, tx, projectID, withChain)
	if err != nil {
		return err
	}
	if withChain {
		if err := verifyChain(ctx, tx, projectID, ordered); err != nil {
			return err
		}
	}
	histories := map[string][]Revision{}
	for _, revision := range ordered {
		id := revision.Memory.MemoryID
		histories[id] = append(histories[id], revision)
	}

	rows, err := queryMemories(ctx, tx,
		`SELECT `+memoryColumns+` FROM memories WHERE project_id = ? ORDER BY memory_id`,
		projectID,
	)
	if err != nil {
		return err
	}
	current := map[string]Memory{}
	for _, row := range rows {
		current[row.MemoryID] = row
		if _, ok := histories[row.MemoryID]; !ok {
			return integrityError("memory %q has a current row but no revisions", row.MemoryID)
		}
	}

	for _, memoryID := range slices.Sorted(maps.Keys(histories)) {
		history := histories[memoryID]
		if err := verifyHistory(memoryID, history); err != nil {
			return err
		}
		row, ok := current[memoryID]
		if !ok {
			return integrityError("memory %q has revisions but no current row", memoryID)
		}
		if !sameJSON(row, history[len(history)-1].Memory) {
			return integrityError("memory %q current row differs from revision %d", memoryID, len(history))
		}
		if err := verifyReferences(memoryID, histories); err != nil {
			return err
		}
	}
	return nil
}

// loadRevisions returns the project's revisions in chain order, or in
// memory and revision order for the schema v1 layout.
func loadRevisions(ctx context.Context, tx *sql.Tx, projectID string, withChain bool) ([]Revision, error) {
	columns, order, scan := revisionColumns, "project_sequence", scanRevision
	if !withChain {
		columns, order, scan = revisionColumnsV1, "memory_id, revision", scanRevisionV1
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT `+columns+` FROM memory_revisions WHERE project_id = ? ORDER BY `+order,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("query revisions: %w", err)
	}
	defer rows.Close()

	revisions := []Revision{}
	for rows.Next() {
		revision, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan revision: %w", err)
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query revisions: %w", err)
	}
	return revisions, nil
}

// verifyChain recomputes the project chain in sequence order and compares
// its end with the stored chain head.
func verifyChain(ctx context.Context, tx *sql.Tx, projectID string, ordered []Revision) error {
	previous := ""
	for i, revision := range ordered {
		sequence := uint64(i + 1)
		if revision.ProjectSequence != sequence {
			return integrityError("project chain has sequence %d where %d is expected", revision.ProjectSequence, sequence)
		}
		if revision.PreviousHash != previous {
			return integrityError("project chain sequence %d previous_hash mismatch", sequence)
		}
		hash, err := revision.computeRevisionHash()
		if err != nil {
			return fmt.Errorf("project chain sequence %d: %w: %w", sequence, ErrIntegrity, err)
		}
		if hash != revision.RevisionHash {
			return integrityError("project chain sequence %d revision_hash mismatch", sequence)
		}
		previous = revision.RevisionHash
	}

	var headSequence int64
	var headHash string
	err := tx.QueryRowContext(ctx,
		`SELECT head_sequence, head_hash FROM memory_chain_heads WHERE project_id = ?`,
		projectID,
	).Scan(&headSequence, &headHash)
	if errors.Is(err, sql.ErrNoRows) {
		if len(ordered) > 0 {
			return integrityError("project chain has %d revisions but no head", len(ordered))
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read chain head: %w", err)
	}
	if uint64(headSequence) != uint64(len(ordered)) || headHash != previous {
		return integrityError("project chain head %d does not match last revision %d", headSequence, len(ordered))
	}
	return nil
}

func integrityError(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), ErrIntegrity)
}

func sameJSON(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

// immutableView drops the fields a lifecycle change may legitimately alter.
func immutableView(value Memory) Memory {
	value.Revision = 0
	value.Status = ""
	value.EvidenceRefs = nil
	value.UpdatedAt = value.CreatedAt
	value.ContentHash = ""
	return value
}

func verifyHistory(memoryID string, history []Revision) error {
	first := history[0]
	switch {
	case first.Action == ActionCreate && first.Memory.Status == StatusCandidate:
	case first.Action == ActionCorrect && first.Memory.Status == StatusActive:
	default:
		return integrityError("memory %q starts with %s/%s", memoryID, first.Action, first.Memory.Status)
	}

	for i, revision := range history {
		snapshot := revision.Memory
		if snapshot.Revision != uint64(i+1) {
			return integrityError("memory %q has revision %d where %d is expected", memoryID, snapshot.Revision, i+1)
		}
		// Each write stamps the snapshot and its revision with one clock reading.
		if !snapshot.UpdatedAt.Equal(revision.RecordedAt) {
			return integrityError("memory %q revision %d updated_at differs from recorded_at", memoryID, snapshot.Revision)
		}
		hash, err := snapshot.computeContentHash()
		if err != nil {
			return fmt.Errorf("memory %q revision %d: %w: %w", memoryID, snapshot.Revision, ErrIntegrity, err)
		}
		if hash != snapshot.ContentHash {
			return integrityError("memory %q revision %d content_hash mismatch", memoryID, snapshot.Revision)
		}
		if i == 0 {
			continue
		}

		previous := history[i-1].Memory
		rule, ok := transitions[revision.Action]
		if !ok || !rule.allows(previous.Status) || snapshot.Status != rule.target {
			return integrityError("memory %q revision %d: %s from %s to %s is not a legal transition",
				memoryID, snapshot.Revision, revision.Action, previous.Status, snapshot.Status)
		}
		if !sameJSON(immutableView(snapshot), immutableView(previous)) {
			return integrityError("memory %q revision %d changed immutable fields", memoryID, snapshot.Revision)
		}
		if revision.Action != ActionConfirm && !slices.Equal(snapshot.EvidenceRefs, previous.EvidenceRefs) {
			return integrityError("memory %q revision %d changed evidence_refs without confirm", memoryID, snapshot.Revision)
		}
		for _, ref := range previous.EvidenceRefs {
			if !slices.Contains(snapshot.EvidenceRefs, ref) {
				return integrityError("memory %q revision %d dropped evidence %q", memoryID, snapshot.Revision, ref)
			}
		}
	}
	return nil
}

// verifyReferences checks that every memory reference stays inside the
// project and that correction and pairwise contest links point both ways.
func verifyReferences(memoryID string, histories map[string][]Revision) error {
	history := histories[memoryID]
	supersedes := history[0].Memory.SupersedesMemoryID

	if history[0].Action == ActionCorrect && history[0].RelatedMemoryID != supersedes {
		return integrityError("memory %q correction revision references %q but supersedes %q",
			memoryID, history[0].RelatedMemoryID, supersedes)
	}
	if supersedes != "" {
		if history[0].Action != ActionCorrect {
			return integrityError("memory %q supersedes %q without a correction revision", memoryID, supersedes)
		}
		old, ok := histories[supersedes]
		if !ok {
			return integrityError("memory %q supersedes %q outside the project", memoryID, supersedes)
		}
		if latest := latestRevision(old, ActionSupersede); latest == nil || latest.RelatedMemoryID != memoryID {
			return integrityError("memory %q supersedes %q but %q does not point back", memoryID, supersedes, supersedes)
		}
	}

	for _, revision := range history {
		related := revision.RelatedMemoryID
		switch revision.Action {
		case ActionCorrect, ActionSupersede:
			if related == "" {
				return integrityError("memory %q revision %d %s has no related memory", memoryID, revision.Memory.Revision, revision.Action)
			}
		case ActionContest:
			if related == "" {
				continue
			}
		default:
			if related != "" {
				return integrityError("memory %q revision %d %s must not reference %q", memoryID, revision.Memory.Revision, revision.Action, related)
			}
			continue
		}

		other, ok := histories[related]
		if !ok {
			return integrityError("memory %q revision %d references %q outside the project", memoryID, revision.Memory.Revision, related)
		}
		switch revision.Action {
		case ActionSupersede:
			if other[0].Memory.SupersedesMemoryID != memoryID {
				return integrityError("memory %q was superseded by %q, which does not supersede it", memoryID, related)
			}
		case ActionContest:
			if !slices.ContainsFunc(other, func(candidate Revision) bool {
				return candidate.Action == ActionContest && candidate.RelatedMemoryID == memoryID
			}) {
				return integrityError("memory %q contests %q, which does not contest it back", memoryID, related)
			}
		}
	}
	return nil
}

func latestRevision(history []Revision, action Action) *Revision {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Action == action {
			return &history[i]
		}
	}
	return nil
}
