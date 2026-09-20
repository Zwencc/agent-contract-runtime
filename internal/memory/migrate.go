package memory

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
)

// migrateV1 adds the per-project revision hash chain to a schema v1 store
// inside the caller's write transaction. Every project must first pass the
// v1 checks, so the chain never seals history that was already inconsistent.
func migrateV1(ctx context.Context, tx *sql.Tx) error {
	projects, err := storedProjects(ctx, tx)
	if err != nil {
		return err
	}
	for _, projectID := range projects {
		if err := verifyProject(ctx, tx, projectID, false); err != nil {
			return fmt.Errorf("refusing to chain unverified history of project %q: %w", projectID, err)
		}
	}

	// rowid preserves the order in which v1 appended revisions.
	rows, err := tx.QueryContext(ctx, `SELECT `+revisionColumnsV1+` FROM memory_revisions ORDER BY project_id, rowid`)
	if err != nil {
		return fmt.Errorf("read v1 revisions: %w", err)
	}
	revisions := []Revision{}
	for rows.Next() {
		revision, err := scanRevisionV1(rows)
		if err != nil {
			rows.Close()
			return fmt.Errorf("scan v1 revision: %w", err)
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read v1 revisions: %w", err)
	}
	rows.Close()

	for _, name := range baseTriggerNames {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+name); err != nil {
			return fmt.Errorf("drop trigger %s: %w", name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE memory_revisions RENAME TO memory_revisions_v1`); err != nil {
		return fmt.Errorf("rename v1 revisions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, revisionsTableSQL); err != nil {
		return fmt.Errorf("create v2 revisions: %w", err)
	}

	heads := map[string]Revision{}
	for _, revision := range revisions {
		head := heads[revision.Memory.ProjectID]
		revision.ProjectSequence = head.ProjectSequence + 1
		revision.PreviousHash = head.RevisionHash
		if revision.RevisionHash, err = revision.computeRevisionHash(); err != nil {
			return err
		}
		if err := insertRevisionRow(ctx, tx, revision); err != nil {
			return err
		}
		heads[revision.Memory.ProjectID] = revision
	}
	for _, projectID := range slices.Sorted(maps.Keys(heads)) {
		head := heads[projectID]
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO memory_chain_heads (project_id, head_sequence, head_hash) VALUES (?, ?, ?)`,
			projectID, int64(head.ProjectSequence), head.RevisionHash,
		); err != nil {
			return fmt.Errorf("record chain head of project %q: %w", projectID, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `DROP TABLE memory_revisions_v1`); err != nil {
		return fmt.Errorf("drop v1 revisions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, baseTriggersSQL+chainTriggersSQL); err != nil {
		return fmt.Errorf("create v2 triggers: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE memory_schema SET version = ?`, storeSchemaVersion); err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}

	for _, projectID := range projects {
		if err := verifyProject(ctx, tx, projectID, true); err != nil {
			return fmt.Errorf("verify migrated project %q: %w", projectID, err)
		}
	}
	return nil
}

func storedProjects(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT project_id FROM memory_revisions UNION SELECT project_id FROM memories ORDER BY project_id`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	projects := []string{}
	for rows.Next() {
		var projectID string
		if err := rows.Scan(&projectID); err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}
		projects = append(projects, projectID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return projects, nil
}
