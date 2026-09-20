package memory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const storeSchemaVersion = 2

// applicationID ("ACRM") marks a file as a memory store so it is never mixed
// with the event store or another SQLite database.
const applicationID = 0x4143524D

// Write transactions start with BEGIN IMMEDIATE (_txlock), so every lifecycle
// change takes the write lock before reading the current revision. Read-only
// queries see a WAL snapshot without blocking writers.
const dsnOptions = "?_txlock=immediate" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=journal_mode(WAL)" +
	// NORMAL for the same reason as the event store: see its dsnOptions.
	"&_pragma=synchronous(NORMAL)"

const statusCheck = `CHECK (status IN ('candidate', 'active', 'confirmed', 'stale',
		'contested', 'superseded', 'retracted', 'archived'))`

const snapshotColumnsSQL = `
	project_id           TEXT NOT NULL,
	memory_id            TEXT NOT NULL,
	revision             INTEGER NOT NULL CHECK (revision >= 1),
	workspace_id         TEXT,
	status               TEXT NOT NULL ` + statusCheck + `,
	priority             INTEGER NOT NULL CHECK (priority BETWEEN 0 AND 6),
	kind                 TEXT NOT NULL,
	claim                TEXT NOT NULL CHECK (length(trim(claim)) > 0),
	scope                TEXT NOT NULL CHECK (json_valid(scope) AND json_type(scope) = 'object'),
	evidence_refs        TEXT NOT NULL CHECK (json_valid(evidence_refs) AND json_type(evidence_refs) = 'array'),
	source               TEXT NOT NULL,
	confidence           REAL NOT NULL CHECK (confidence BETWEEN 0 AND 1),
	valid_from           TEXT NOT NULL,
	valid_until          TEXT,
	created_at           TEXT NOT NULL,
	updated_at           TEXT NOT NULL,
	supersedes_memory_id TEXT,
	content_hash         TEXT NOT NULL,`

// revisionBacked holds when the memories row equals a recorded revision.
const revisionBacked = `EXISTS (SELECT 1 FROM memory_revisions r
		WHERE r.project_id = NEW.project_id AND r.memory_id = NEW.memory_id
		AND r.revision = NEW.revision AND r.status = NEW.status
		AND r.content_hash = NEW.content_hash)`

// revisionBodySQL holds the revision columns that follow the snapshot in
// every schema version.
const revisionBodySQL = `
	action               TEXT NOT NULL,
	reason               TEXT NOT NULL,
	change_evidence_refs TEXT NOT NULL CHECK (json_valid(change_evidence_refs)),
	related_memory_id    TEXT,
	recorded_at          TEXT NOT NULL,`

const revisionsTableSQL = `
CREATE TABLE memory_revisions (` + snapshotColumnsSQL + revisionBodySQL + `
	project_sequence     INTEGER NOT NULL CHECK (project_sequence >= 1),
	previous_hash        TEXT NOT NULL,
	revision_hash        TEXT NOT NULL,
	PRIMARY KEY (project_id, memory_id, revision),
	UNIQUE (project_id, project_sequence)
) STRICT;

CREATE TABLE memory_chain_heads (
	project_id    TEXT PRIMARY KEY,
	head_sequence INTEGER NOT NULL CHECK (head_sequence >= 1),
	head_hash     TEXT NOT NULL
) STRICT;
`

const memoriesTableSQL = `
CREATE TABLE memories (` + snapshotColumnsSQL + `
	PRIMARY KEY (project_id, memory_id)
) STRICT;

CREATE INDEX memories_retrieve ON memories (project_id, status, priority, updated_at);
CREATE INDEX memories_expiry ON memories (project_id, valid_until) WHERE valid_until IS NOT NULL;
CREATE INDEX memories_supersedes ON memories (project_id, supersedes_memory_id) WHERE supersedes_memory_id IS NOT NULL;
`

// baseTriggerNames lists the schema v1 triggers; chainTriggerNames those
// added by schema v2.
var (
	baseTriggerNames = []string{"memory_revisions_no_update", "memory_revisions_no_delete",
		"memory_revisions_contiguous", "memories_no_delete", "memories_insert_guard", "memories_update_guard"}
	chainTriggerNames = []string{"memory_revisions_chain", "memory_chain_heads_no_delete",
		"memory_chain_heads_insert_guard", "memory_chain_heads_update_guard"}
)

const baseTriggersSQL = `
CREATE TRIGGER memory_revisions_no_update BEFORE UPDATE ON memory_revisions
BEGIN
	SELECT RAISE(ABORT, 'memory revisions are append-only');
END;

CREATE TRIGGER memory_revisions_no_delete BEFORE DELETE ON memory_revisions
BEGIN
	SELECT RAISE(ABORT, 'memory revisions are append-only');
END;

CREATE TRIGGER memory_revisions_contiguous BEFORE INSERT ON memory_revisions
WHEN NEW.revision <> COALESCE((SELECT MAX(revision) FROM memory_revisions
	WHERE project_id = NEW.project_id AND memory_id = NEW.memory_id), 0) + 1
BEGIN
	SELECT RAISE(ABORT, 'memory revisions must be contiguous');
END;

CREATE TRIGGER memories_no_delete BEFORE DELETE ON memories
BEGIN
	SELECT RAISE(ABORT, 'memories cannot be deleted');
END;

CREATE TRIGGER memories_insert_guard BEFORE INSERT ON memories
WHEN NEW.revision <> 1 OR NOT ` + revisionBacked + `
BEGIN
	SELECT RAISE(ABORT, 'memory must start from a recorded revision 1');
END;

CREATE TRIGGER memories_update_guard BEFORE UPDATE ON memories
WHEN NEW.revision <> OLD.revision + 1
	OR NEW.project_id IS NOT OLD.project_id
	OR NEW.memory_id IS NOT OLD.memory_id
	OR NEW.workspace_id IS NOT OLD.workspace_id
	OR NEW.kind IS NOT OLD.kind
	OR NEW.priority IS NOT OLD.priority
	OR NEW.claim IS NOT OLD.claim
	OR NEW.scope IS NOT OLD.scope
	OR NEW.created_at IS NOT OLD.created_at
	OR NEW.supersedes_memory_id IS NOT OLD.supersedes_memory_id
	OR NOT ` + revisionBacked + `
BEGIN
	SELECT RAISE(ABORT, 'memory update must follow a recorded next revision');
END;
`

// headBacked holds when a chain head points at a recorded revision.
const headBacked = `EXISTS (SELECT 1 FROM memory_revisions r
		WHERE r.project_id = NEW.project_id AND r.project_sequence = NEW.head_sequence
		AND r.revision_hash = NEW.head_hash)`

const chainTriggersSQL = `
CREATE TRIGGER memory_revisions_chain BEFORE INSERT ON memory_revisions
WHEN NEW.project_sequence <> COALESCE((SELECT head_sequence FROM memory_chain_heads
		WHERE project_id = NEW.project_id), 0) + 1
	OR NEW.previous_hash <> COALESCE((SELECT head_hash FROM memory_chain_heads
		WHERE project_id = NEW.project_id), '')
BEGIN
	SELECT RAISE(ABORT, 'memory revision must extend the project chain head');
END;

CREATE TRIGGER memory_chain_heads_no_delete BEFORE DELETE ON memory_chain_heads
BEGIN
	SELECT RAISE(ABORT, 'memory chain heads cannot be deleted');
END;

CREATE TRIGGER memory_chain_heads_insert_guard BEFORE INSERT ON memory_chain_heads
WHEN NEW.head_sequence <> 1 OR NOT ` + headBacked + `
BEGIN
	SELECT RAISE(ABORT, 'memory chain must start from a recorded revision');
END;

CREATE TRIGGER memory_chain_heads_update_guard BEFORE UPDATE ON memory_chain_heads
WHEN NEW.project_id IS NOT OLD.project_id
	OR NEW.head_sequence <> OLD.head_sequence + 1
	OR NOT ` + headBacked + `
BEGIN
	SELECT RAISE(ABORT, 'memory chain head must move to the next recorded revision');
END;
`

const schemaSQL = `
CREATE TABLE memory_schema (
	version INTEGER NOT NULL
) STRICT;
` + revisionsTableSQL + memoriesTableSQL + baseTriggersSQL + chainTriggersSQL + `
PRAGMA application_id = 1094931021;
`

const memoryColumns = `project_id, memory_id, revision, workspace_id, status, priority,
	kind, claim, scope, evidence_refs, source, confidence, valid_from, valid_until,
	created_at, updated_at, supersedes_memory_id, content_hash`

const revisionColumnsV1 = memoryColumns + `, action, reason, change_evidence_refs,
	related_memory_id, recorded_at`

const revisionColumns = revisionColumnsV1 + `, project_sequence, previous_hash, revision_hash`

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("open memory store: path is required")
	}
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("open memory store %q: path must not contain '?'", path)
	}

	db, err := sql.Open("sqlite", path+dsnOptions)
	if err != nil {
		return nil, fmt.Errorf("open memory store %q: %w", path, err)
	}
	store := &Store{db: db, now: time.Now}
	if err := store.initSchema(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("open memory store %q: %w", path, err)
	}
	return store, nil
}

func (store *Store) Close() error {
	if err := store.db.Close(); err != nil {
		return fmt.Errorf("close memory store: %w", err)
	}
	return nil
}

func (store *Store) initSchema(ctx context.Context) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema transaction: %w", err)
	}
	defer tx.Rollback()

	var id int64
	if err := tx.QueryRowContext(ctx, `PRAGMA application_id`).Scan(&id); err != nil {
		return fmt.Errorf("read application_id: %w", err)
	}
	switch id {
	case applicationID:
		var version int
		if err := tx.QueryRowContext(ctx, `SELECT version FROM memory_schema`).Scan(&version); err != nil {
			return fmt.Errorf("read schema version: %w", err)
		}
		switch version {
		case storeSchemaVersion:
			return nil
		case 1:
			if err := migrateV1(ctx, tx); err != nil {
				return fmt.Errorf("migrate schema v1 to v%d: %w", storeSchemaVersion, err)
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit schema migration: %w", err)
			}
			return nil
		default:
			return fmt.Errorf("unsupported schema version %d, want %d", version, storeSchemaVersion)
		}
	case 0:
		var objects int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master`).Scan(&objects); err != nil {
			return fmt.Errorf("inspect database: %w", err)
		}
		if objects > 0 {
			return errors.New("database already holds other data and is not an ACR memory store")
		}
	default:
		return fmt.Errorf("database application_id %#x is not an ACR memory store", id)
	}

	if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_schema (version) VALUES (?)`, storeSchemaVersion); err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema: %w", err)
	}
	return nil
}

// normalizeContent validates and normalizes every caller-owned field of a new
// memory in place.
func normalizeContent(value *Memory) error {
	if err := validateIdentifier("project_id", value.ProjectID); err != nil {
		return err
	}
	if value.WorkspaceID != "" {
		if err := validateIdentifier("workspace_id", value.WorkspaceID); err != nil {
			return err
		}
	}
	if err := validateKind(value.Kind); err != nil {
		return err
	}
	if !value.Priority.Valid() {
		return invalid("priority %d must be within P0..P6", int(value.Priority))
	}
	var err error
	if value.Claim, err = normalizeClaim(value.Claim); err != nil {
		return err
	}
	if value.Scope, err = normalizeScope(value.Scope); err != nil {
		return err
	}
	if value.EvidenceRefs, err = normalizeEvidence(value.EvidenceRefs); err != nil {
		return err
	}
	if value.Source, err = normalizeSource(value.Source); err != nil {
		return err
	}
	if err := validateConfidence(value.Confidence); err != nil {
		return err
	}
	if !value.ValidUntil.IsZero() && !value.ValidUntil.After(value.ValidFrom) {
		return invalid("valid_until must be after valid_from")
	}
	return nil
}

func newMemoryID() string {
	return "mem_" + rand.Text()
}

// CreateCandidate stores a new memory in candidate status. Candidates never
// reach normal retrieval until activated.
func (store *Store) CreateCandidate(ctx context.Context, request CreateRequest) (Memory, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Memory{}, fmt.Errorf("create memory candidate: begin: %w", err)
	}
	defer tx.Rollback()

	now := store.now()
	value := Memory{
		ProjectID:    request.ProjectID,
		WorkspaceID:  request.WorkspaceID,
		Kind:         request.Kind,
		Priority:     request.Priority,
		Claim:        request.Claim,
		Scope:        request.Scope,
		EvidenceRefs: request.EvidenceRefs,
		Source:       request.Source,
		Confidence:   request.Confidence,
		ValidFrom:    request.ValidFrom,
		ValidUntil:   request.ValidUntil,
	}
	if value.ValidFrom.IsZero() {
		value.ValidFrom = now
	}
	if err := normalizeContent(&value); err != nil {
		return Memory{}, fmt.Errorf("create memory candidate: %w", err)
	}

	value.MemoryID = newMemoryID()
	value.Status = StatusCandidate
	value.CreatedAt = now
	if err := insertNew(ctx, tx, &value, ActionCreate, "", value.EvidenceRefs, "", now); err != nil {
		return Memory{}, fmt.Errorf("create memory candidate in project %q: %w", value.ProjectID, err)
	}
	if err := tx.Commit(); err != nil {
		return Memory{}, fmt.Errorf("create memory candidate in project %q: commit: %w", value.ProjectID, err)
	}
	return value, nil
}

// Activate moves a candidate, stale or contested memory to active. The
// memory must carry at least one evidence reference.
func (store *Store) Activate(ctx context.Context, projectID, memoryID, reason string) (Memory, error) {
	return store.change(ctx, projectID, memoryID, ActionActivate, reason, nil)
}

// Confirm marks a memory confirmed and merges the new evidence references.
func (store *Store) Confirm(ctx context.Context, projectID, memoryID string, evidenceRefs []string) (Memory, error) {
	return store.change(ctx, projectID, memoryID, ActionConfirm, "", evidenceRefs)
}

func (store *Store) MarkStale(ctx context.Context, projectID, memoryID, reason string) (Memory, error) {
	return store.change(ctx, projectID, memoryID, ActionMarkStale, reason, nil)
}

func (store *Store) Contest(ctx context.Context, projectID, memoryID, reason string) (Memory, error) {
	return store.change(ctx, projectID, memoryID, ActionContest, reason, nil)
}

// Retract marks a memory as confirmed wrong. Nothing is deleted, and a
// retracted memory can only be replaced by a new memory.
func (store *Store) Retract(ctx context.Context, projectID, memoryID, reason string) (Memory, error) {
	return store.change(ctx, projectID, memoryID, ActionRetract, reason, nil)
}

func (store *Store) Archive(ctx context.Context, projectID, memoryID, reason string) (Memory, error) {
	return store.change(ctx, projectID, memoryID, ActionArchive, reason, nil)
}

// ActivateAtRevision is Activate guarded by optimistic concurrency: it fails
// with ErrRevisionConflict unless the memory is still at expectedRevision.
func (store *Store) ActivateAtRevision(ctx context.Context, projectID, memoryID string, expectedRevision uint64, reason string) (Memory, error) {
	return store.changeAt(ctx, projectID, memoryID, expectedRevision, ActionActivate, reason, nil)
}

func (store *Store) ConfirmAtRevision(ctx context.Context, projectID, memoryID string, expectedRevision uint64, evidenceRefs []string) (Memory, error) {
	return store.changeAt(ctx, projectID, memoryID, expectedRevision, ActionConfirm, "", evidenceRefs)
}

func (store *Store) MarkStaleAtRevision(ctx context.Context, projectID, memoryID string, expectedRevision uint64, reason string) (Memory, error) {
	return store.changeAt(ctx, projectID, memoryID, expectedRevision, ActionMarkStale, reason, nil)
}

func (store *Store) ContestAtRevision(ctx context.Context, projectID, memoryID string, expectedRevision uint64, reason string) (Memory, error) {
	return store.changeAt(ctx, projectID, memoryID, expectedRevision, ActionContest, reason, nil)
}

func (store *Store) RetractAtRevision(ctx context.Context, projectID, memoryID string, expectedRevision uint64, reason string) (Memory, error) {
	return store.changeAt(ctx, projectID, memoryID, expectedRevision, ActionRetract, reason, nil)
}

func (store *Store) ArchiveAtRevision(ctx context.Context, projectID, memoryID string, expectedRevision uint64, reason string) (Memory, error) {
	return store.changeAt(ctx, projectID, memoryID, expectedRevision, ActionArchive, reason, nil)
}

func (store *Store) change(ctx context.Context, projectID, memoryID string, action Action, reason string, evidenceRefs []string) (Memory, error) {
	value, err := store.changeTx(ctx, projectID, memoryID, 0, action, reason, evidenceRefs)
	if err != nil {
		return Memory{}, fmt.Errorf("%s memory %q in project %q: %w", action, memoryID, projectID, err)
	}
	return value, nil
}

func (store *Store) changeAt(ctx context.Context, projectID, memoryID string, expectedRevision uint64, action Action, reason string, evidenceRefs []string) (Memory, error) {
	if expectedRevision == 0 {
		return Memory{}, fmt.Errorf("%s memory %q in project %q: %w", action, memoryID, projectID, invalid("expected revision must be positive"))
	}
	value, err := store.changeTx(ctx, projectID, memoryID, expectedRevision, action, reason, evidenceRefs)
	if err != nil {
		return Memory{}, fmt.Errorf("%s memory %q in project %q: %w", action, memoryID, projectID, err)
	}
	return value, nil
}

// checkRevision compares the revision read inside the write transaction with
// the caller's expectation. Zero means the caller did not ask for a check.
func checkRevision(current Memory, expectedRevision uint64) error {
	if expectedRevision != 0 && current.Revision != expectedRevision {
		return fmt.Errorf("memory %q expected revision %d, actual %d: %w",
			current.MemoryID, expectedRevision, current.Revision, ErrRevisionConflict)
	}
	return nil
}

func (store *Store) changeTx(ctx context.Context, projectID, memoryID string, expectedRevision uint64, action Action, reason string, evidenceRefs []string) (Memory, error) {
	if err := validateKeys(projectID, memoryID); err != nil {
		return Memory{}, err
	}
	var err error
	if action == ActionConfirm {
		if evidenceRefs, err = normalizeEvidence(evidenceRefs); err != nil {
			return Memory{}, err
		}
		if len(evidenceRefs) == 0 {
			return Memory{}, invalid("confirm requires evidence_refs")
		}
	} else if reason, err = normalizeReason(reason); err != nil {
		return Memory{}, err
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Memory{}, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	current, err := getMemory(ctx, tx, projectID, memoryID)
	if err != nil {
		return Memory{}, err
	}
	if err := checkRevision(current, expectedRevision); err != nil {
		return Memory{}, err
	}
	next, err := applyTransition(ctx, tx, current, action, reason, evidenceRefs, "", store.now())
	if err != nil {
		return Memory{}, err
	}
	if err := tx.Commit(); err != nil {
		return Memory{}, fmt.Errorf("commit: %w", err)
	}
	return next, nil
}

// Correct atomically creates an active replacement memory pointing at the old
// one and moves the old memory to superseded. Both sides record the reason
// and correction evidence in their revision history.
func (store *Store) Correct(ctx context.Context, projectID, memoryID string, request CorrectRequest) (Memory, error) {
	value, err := store.correctTx(ctx, projectID, memoryID, 0, request)
	if err != nil {
		return Memory{}, fmt.Errorf("correct memory %q in project %q: %w", memoryID, projectID, err)
	}
	return value, nil
}

// CorrectAtRevision is Correct guarded by optimistic concurrency on the old
// memory.
func (store *Store) CorrectAtRevision(ctx context.Context, projectID, memoryID string, expectedRevision uint64, request CorrectRequest) (Memory, error) {
	if expectedRevision == 0 {
		return Memory{}, fmt.Errorf("correct memory %q in project %q: %w", memoryID, projectID, invalid("expected revision must be positive"))
	}
	value, err := store.correctTx(ctx, projectID, memoryID, expectedRevision, request)
	if err != nil {
		return Memory{}, fmt.Errorf("correct memory %q in project %q: %w", memoryID, projectID, err)
	}
	return value, nil
}

func (store *Store) correctTx(ctx context.Context, projectID, memoryID string, expectedRevision uint64, request CorrectRequest) (Memory, error) {
	if err := validateKeys(projectID, memoryID); err != nil {
		return Memory{}, err
	}
	reason, err := normalizeReason(request.Reason)
	if err != nil {
		return Memory{}, err
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Memory{}, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	now := store.now()
	old, err := getMemory(ctx, tx, projectID, memoryID)
	if err != nil {
		return Memory{}, err
	}
	if err := checkRevision(old, expectedRevision); err != nil {
		return Memory{}, err
	}
	if !transitions[ActionSupersede].allows(old.Status) {
		return Memory{}, fmt.Errorf("cannot correct a %s memory: %w", old.Status, ErrInvalidTransition)
	}

	replacement := Memory{
		ProjectID:          old.ProjectID,
		WorkspaceID:        old.WorkspaceID,
		Kind:               old.Kind,
		Priority:           old.Priority,
		Claim:              request.Claim,
		Scope:              request.Scope,
		EvidenceRefs:       request.EvidenceRefs,
		Source:             request.Source,
		Confidence:         request.Confidence,
		ValidFrom:          now,
		ValidUntil:         request.ValidUntil,
		SupersedesMemoryID: old.MemoryID,
	}
	if replacement.Scope == nil {
		replacement.Scope = old.Scope
	}
	if err := normalizeContent(&replacement); err != nil {
		return Memory{}, err
	}
	if len(replacement.EvidenceRefs) == 0 {
		return Memory{}, invalid("correction requires evidence_refs")
	}

	replacement.MemoryID = newMemoryID()
	replacement.Status = StatusActive
	replacement.CreatedAt = now
	if err := insertNew(ctx, tx, &replacement, ActionCorrect, reason, replacement.EvidenceRefs, old.MemoryID, now); err != nil {
		return Memory{}, err
	}
	if _, err := applyTransition(ctx, tx, old, ActionSupersede, reason, replacement.EvidenceRefs, replacement.MemoryID, now); err != nil {
		return Memory{}, err
	}
	if err := tx.Commit(); err != nil {
		return Memory{}, fmt.Errorf("commit: %w", err)
	}
	return replacement, nil
}

// ContestPair marks two conflicting memories of one project contested in a
// single transaction. Each side records the other memory, the reason and the
// conflict evidence; any failure leaves both unchanged.
func (store *Store) ContestPair(
	ctx context.Context,
	projectID string,
	leftMemoryID string,
	leftExpectedRevision uint64,
	rightMemoryID string,
	rightExpectedRevision uint64,
	reason string,
	evidenceRefs []string,
) (left Memory, right Memory, err error) {
	left, right, err = store.contestPairTx(ctx, projectID, leftMemoryID, leftExpectedRevision, rightMemoryID, rightExpectedRevision, reason, evidenceRefs)
	if err != nil {
		return Memory{}, Memory{}, fmt.Errorf("contest memories %q and %q in project %q: %w", leftMemoryID, rightMemoryID, projectID, err)
	}
	return left, right, nil
}

func (store *Store) contestPairTx(ctx context.Context, projectID, leftMemoryID string, leftExpectedRevision uint64, rightMemoryID string, rightExpectedRevision uint64, reason string, evidenceRefs []string) (Memory, Memory, error) {
	if err := validateKeys(projectID, leftMemoryID); err != nil {
		return Memory{}, Memory{}, err
	}
	if err := validateIdentifier("memory_id", rightMemoryID); err != nil {
		return Memory{}, Memory{}, err
	}
	if leftMemoryID == rightMemoryID {
		return Memory{}, Memory{}, invalid("a memory cannot contest itself")
	}
	if leftExpectedRevision == 0 || rightExpectedRevision == 0 {
		return Memory{}, Memory{}, invalid("expected revisions must be positive")
	}
	reason, err := normalizeReason(reason)
	if err != nil {
		return Memory{}, Memory{}, err
	}
	if evidenceRefs, err = normalizeEvidence(evidenceRefs); err != nil {
		return Memory{}, Memory{}, err
	}
	if len(evidenceRefs) == 0 {
		return Memory{}, Memory{}, invalid("contest requires evidence_refs")
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Memory{}, Memory{}, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	// Both reads are scoped by projectID, so a memory of another project is
	// simply not found.
	left, err := getMemory(ctx, tx, projectID, leftMemoryID)
	if err != nil {
		return Memory{}, Memory{}, fmt.Errorf("memory %q: %w", leftMemoryID, err)
	}
	right, err := getMemory(ctx, tx, projectID, rightMemoryID)
	if err != nil {
		return Memory{}, Memory{}, fmt.Errorf("memory %q: %w", rightMemoryID, err)
	}
	if err := checkRevision(left, leftExpectedRevision); err != nil {
		return Memory{}, Memory{}, err
	}
	if err := checkRevision(right, rightExpectedRevision); err != nil {
		return Memory{}, Memory{}, err
	}

	now := store.now()
	if left, err = applyTransition(ctx, tx, left, ActionContest, reason, evidenceRefs, rightMemoryID, now); err != nil {
		return Memory{}, Memory{}, fmt.Errorf("memory %q: %w", leftMemoryID, err)
	}
	if right, err = applyTransition(ctx, tx, right, ActionContest, reason, evidenceRefs, leftMemoryID, now); err != nil {
		return Memory{}, Memory{}, fmt.Errorf("memory %q: %w", rightMemoryID, err)
	}
	if err := tx.Commit(); err != nil {
		return Memory{}, Memory{}, fmt.Errorf("commit: %w", err)
	}
	return left, right, nil
}

// ArchiveExpired archives every memory of a project whose valid_until is at
// or before now, except P0 and P1, which are never archived automatically.
func (store *Store) ArchiveExpired(ctx context.Context, projectID string, now time.Time) (int, error) {
	count, err := store.archiveExpiredTx(ctx, projectID, now)
	if err != nil {
		return 0, fmt.Errorf("archive expired memories in project %q: %w", projectID, err)
	}
	return count, nil
}

func (store *Store) archiveExpiredTx(ctx context.Context, projectID string, now time.Time) (int, error) {
	if err := validateIdentifier("project_id", projectID); err != nil {
		return 0, err
	}
	if now.IsZero() {
		return 0, invalid("now is required")
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	statuses := transitions[ActionExpire].from
	args := []any{projectID, formatTime(now), int(P2)}
	for _, status := range statuses {
		args = append(args, string(status))
	}
	expired, err := queryMemories(ctx, tx,
		`SELECT `+memoryColumns+` FROM memories
		WHERE project_id = ? AND valid_until IS NOT NULL AND valid_until <= ?
			AND priority >= ? AND status IN (`+placeholders(len(statuses))+`)
		ORDER BY memory_id`,
		args...,
	)
	if err != nil {
		return 0, err
	}

	recordedAt := store.now()
	for _, value := range expired {
		if _, err := applyTransition(ctx, tx, value, ActionExpire, "valid_until expired", nil, "", recordedAt); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return len(expired), nil
}

func (store *Store) Get(ctx context.Context, projectID, memoryID string) (Memory, error) {
	if err := validateKeys(projectID, memoryID); err != nil {
		return Memory{}, fmt.Errorf("get memory: %w", err)
	}
	value, err := getMemory(ctx, store.db, projectID, memoryID)
	if err != nil {
		return Memory{}, fmt.Errorf("get memory %q in project %q: %w", memoryID, projectID, err)
	}
	return value, nil
}

// List returns the current revision of every memory in a project for human
// governance surfaces. Unlike Retrieve it includes candidates and terminal
// states, so callers must never use it to assemble Agent context.
func (store *Store) List(ctx context.Context, projectID string, limit int) ([]Memory, error) {
	if err := validateIdentifier("project_id", projectID); err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	if limit <= 0 || limit > MaximumRetrieveLimit {
		return nil, fmt.Errorf("list memories: %w", invalid("limit %d must be within 1..%d", limit, MaximumRetrieveLimit))
	}
	values, err := queryMemories(ctx, store.db, `SELECT `+memoryColumns+` FROM memories
		WHERE project_id = ? ORDER BY priority, updated_at DESC, memory_id LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list memories in project %q: %w", projectID, err)
	}
	return values, nil
}

// History returns every revision of a memory in revision order.
func (store *Store) History(ctx context.Context, projectID, memoryID string) ([]Revision, error) {
	if err := validateKeys(projectID, memoryID); err != nil {
		return nil, fmt.Errorf("memory history: %w", err)
	}
	rows, err := store.db.QueryContext(ctx,
		`SELECT `+revisionColumns+` FROM memory_revisions
		WHERE project_id = ? AND memory_id = ?
		ORDER BY revision`,
		projectID, memoryID,
	)
	if err != nil {
		return nil, fmt.Errorf("history of memory %q in project %q: %w", memoryID, projectID, err)
	}
	defer rows.Close()

	revisions := []Revision{}
	for rows.Next() {
		revision, err := scanRevision(rows)
		if err != nil {
			return nil, fmt.Errorf("history of memory %q in project %q: %w", memoryID, projectID, err)
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("history of memory %q in project %q: %w", memoryID, projectID, err)
	}
	if len(revisions) == 0 {
		return nil, fmt.Errorf("history of memory %q in project %q: %w", memoryID, projectID, ErrNotFound)
	}
	return revisions, nil
}

// Retrieve returns memories eligible for normal agent context: active and
// confirmed (plus contested when requested), valid at AsOf, ordered by
// priority, confirmed before active, newest update, then memory_id.
func (store *Store) Retrieve(ctx context.Context, query Query) ([]Memory, error) {
	statement, args, err := store.retrieveStatement(query)
	if err != nil {
		return nil, fmt.Errorf("retrieve memories: %w", err)
	}
	values, err := queryMemories(ctx, store.db, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("retrieve memories in project %q: %w", query.ProjectID, err)
	}
	return values, nil
}

func (store *Store) retrieveStatement(query Query) (string, []any, error) {
	if err := validateIdentifier("project_id", query.ProjectID); err != nil {
		return "", nil, err
	}
	if query.Limit <= 0 || query.Limit > MaximumRetrieveLimit {
		return "", nil, invalid("limit %d must be within 1..%d", query.Limit, MaximumRetrieveLimit)
	}
	asOf := query.AsOf
	if asOf.IsZero() {
		asOf = store.now()
	}

	statuses := []any{string(StatusActive), string(StatusConfirmed)}
	if query.IncludeContested {
		statuses = append(statuses, string(StatusContested))
	}
	var where strings.Builder
	where.WriteString(`project_id = ? AND status IN (` + placeholders(len(statuses)) + `)
		AND valid_from <= ? AND (valid_until IS NULL OR valid_until > ?)`)
	args := append([]any{query.ProjectID}, statuses...)
	args = append(args, formatTime(asOf), formatTime(asOf))

	if query.WorkspaceID != "" {
		if err := validateIdentifier("workspace_id", query.WorkspaceID); err != nil {
			return "", nil, err
		}
		where.WriteString(` AND (workspace_id IS NULL OR workspace_id = ?)`)
		args = append(args, query.WorkspaceID)
	}
	if len(query.Kinds) > 0 {
		if len(query.Kinds) > maximumKinds {
			return "", nil, invalid("kinds exceeds %d entries", maximumKinds)
		}
		for _, kind := range query.Kinds {
			if err := validateKind(kind); err != nil {
				return "", nil, err
			}
			args = append(args, kind)
		}
		where.WriteString(` AND kind IN (` + placeholders(len(query.Kinds)) + `)`)
	}
	if query.MaximumPriority != nil {
		if !query.MaximumPriority.Valid() {
			return "", nil, invalid("maximum_priority %d must be within P0..P6", int(*query.MaximumPriority))
		}
		where.WriteString(` AND priority <= ?`)
		args = append(args, int(*query.MaximumPriority))
	}
	args = append(args, query.Limit)

	statement := `SELECT ` + memoryColumns + ` FROM memories WHERE ` + where.String() + `
		ORDER BY priority ASC,
			CASE status WHEN 'confirmed' THEN 0 WHEN 'active' THEN 1 ELSE 2 END ASC,
			updated_at DESC,
			memory_id ASC
		LIMIT ?`
	return statement, args, nil
}

func validateKeys(projectID, memoryID string) error {
	if err := validateIdentifier("project_id", projectID); err != nil {
		return err
	}
	return validateIdentifier("memory_id", memoryID)
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", count), ", ")
}

// applyTransition appends the next revision of current and moves the memories
// row to it. It must run inside a write transaction.
func applyTransition(ctx context.Context, tx *sql.Tx, current Memory, action Action, reason string, changeEvidence []string, relatedMemoryID string, now time.Time) (Memory, error) {
	rule, ok := transitions[action]
	if !ok || !rule.allows(current.Status) {
		return Memory{}, fmt.Errorf("%s from %s: %w", action, current.Status, ErrInvalidTransition)
	}
	if action == ActionActivate && len(current.EvidenceRefs) == 0 {
		return Memory{}, fmt.Errorf("activation requires evidence_refs: %w", ErrInvalidTransition)
	}

	next := current
	next.Status = rule.target
	next.Revision = current.Revision + 1
	next.UpdatedAt = now
	if action == ActionConfirm {
		merged, err := mergeEvidence(current.EvidenceRefs, changeEvidence)
		if err != nil {
			return Memory{}, err
		}
		next.EvidenceRefs = merged
	}
	var err error
	if next.ContentHash, err = next.computeContentHash(); err != nil {
		return Memory{}, err
	}
	if err := insertRevision(ctx, tx, next, action, reason, changeEvidence, relatedMemoryID, now); err != nil {
		return Memory{}, err
	}

	evidence, err := json.Marshal(next.EvidenceRefs)
	if err != nil {
		return Memory{}, fmt.Errorf("encode evidence_refs: %w", err)
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE memories SET revision = ?, status = ?, evidence_refs = ?, updated_at = ?, content_hash = ?
		WHERE project_id = ? AND memory_id = ? AND revision = ?`,
		int64(next.Revision), string(next.Status), string(evidence), formatTime(next.UpdatedAt), next.ContentHash,
		next.ProjectID, next.MemoryID, int64(current.Revision),
	)
	if err != nil {
		return Memory{}, fmt.Errorf("update memory %q to revision %d: %w", next.MemoryID, next.Revision, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Memory{}, fmt.Errorf("update memory %q to revision %d: %w", next.MemoryID, next.Revision, err)
	}
	if affected != 1 {
		return Memory{}, fmt.Errorf("update memory %q to revision %d: revision %d is no longer current: %w", next.MemoryID, next.Revision, current.Revision, ErrRevisionConflict)
	}
	return next, nil
}

// insertNew records revision 1 of a new memory, then its current row.
func insertNew(ctx context.Context, tx *sql.Tx, value *Memory, action Action, reason string, changeEvidence []string, relatedMemoryID string, now time.Time) error {
	value.Revision = 1
	value.UpdatedAt = now
	var err error
	if value.ContentHash, err = value.computeContentHash(); err != nil {
		return err
	}
	if err := insertRevision(ctx, tx, *value, action, reason, changeEvidence, relatedMemoryID, now); err != nil {
		return err
	}
	args, err := memoryArgs(*value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO memories (`+memoryColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		args...,
	)
	if err != nil {
		return fmt.Errorf("insert memory %q: %w", value.MemoryID, err)
	}
	return nil
}

// insertRevision appends a revision to the project chain: it reads the chain
// head, links and hashes the revision, inserts it and moves the head, all in
// the caller's write transaction.
func insertRevision(ctx context.Context, tx *sql.Tx, value Memory, action Action, reason string, changeEvidence []string, relatedMemoryID string, now time.Time) error {
	revision := Revision{
		Memory:             value,
		Action:             action,
		Reason:             reason,
		ChangeEvidenceRefs: emptyIfNil(changeEvidence),
		RelatedMemoryID:    relatedMemoryID,
		RecordedAt:         now,
	}
	var headSequence int64
	err := tx.QueryRowContext(ctx,
		`SELECT head_sequence, head_hash FROM memory_chain_heads WHERE project_id = ?`,
		value.ProjectID,
	).Scan(&headSequence, &revision.PreviousHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read chain head of project %q: %w", value.ProjectID, err)
	}
	revision.ProjectSequence = uint64(headSequence) + 1
	if revision.RevisionHash, err = revision.computeRevisionHash(); err != nil {
		return err
	}
	if err := insertRevisionRow(ctx, tx, revision); err != nil {
		return err
	}

	if headSequence == 0 {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO memory_chain_heads (project_id, head_sequence, head_hash) VALUES (?, ?, ?)`,
			value.ProjectID, int64(revision.ProjectSequence), revision.RevisionHash,
		)
	} else {
		_, err = tx.ExecContext(ctx,
			`UPDATE memory_chain_heads SET head_sequence = ?, head_hash = ?
			WHERE project_id = ? AND head_sequence = ?`,
			int64(revision.ProjectSequence), revision.RevisionHash, value.ProjectID, headSequence,
		)
	}
	if err != nil {
		return fmt.Errorf("move chain head of project %q to %d: %w", value.ProjectID, revision.ProjectSequence, err)
	}
	return nil
}

func insertRevisionRow(ctx context.Context, tx *sql.Tx, revision Revision) error {
	value := revision.Memory
	args, err := memoryArgs(value)
	if err != nil {
		return err
	}
	evidence, err := json.Marshal(emptyIfNil(revision.ChangeEvidenceRefs))
	if err != nil {
		return fmt.Errorf("encode change_evidence_refs: %w", err)
	}
	args = append(args, string(revision.Action), revision.Reason, string(evidence), nullable(revision.RelatedMemoryID),
		formatTime(revision.RecordedAt), int64(revision.ProjectSequence), revision.PreviousHash, revision.RevisionHash)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO memory_revisions (`+revisionColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		args...,
	)
	if err != nil {
		return fmt.Errorf("insert revision %d of memory %q: %w", value.Revision, value.MemoryID, err)
	}
	return nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func memoryArgs(value Memory) ([]any, error) {
	evidence, err := json.Marshal(value.EvidenceRefs)
	if err != nil {
		return nil, fmt.Errorf("encode evidence_refs: %w", err)
	}
	var validUntil any
	if !value.ValidUntil.IsZero() {
		validUntil = formatTime(value.ValidUntil)
	}
	return []any{
		value.ProjectID, value.MemoryID, int64(value.Revision), nullable(value.WorkspaceID),
		string(value.Status), int(value.Priority), value.Kind, value.Claim, string(value.Scope),
		string(evidence), value.Source, value.Confidence, formatTime(value.ValidFrom), validUntil,
		formatTime(value.CreatedAt), formatTime(value.UpdatedAt), nullable(value.SupersedesMemoryID),
		value.ContentHash,
	}, nil
}

type scanner interface {
	Scan(dest ...any) error
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getMemory(ctx context.Context, source querier, projectID, memoryID string) (Memory, error) {
	value, err := scanMemory(source.QueryRowContext(ctx,
		`SELECT `+memoryColumns+` FROM memories WHERE project_id = ? AND memory_id = ?`,
		projectID, memoryID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, ErrNotFound
	}
	if err != nil {
		return Memory{}, fmt.Errorf("read memory: %w", err)
	}
	return value, nil
}

func queryMemories(ctx context.Context, source querier, statement string, args ...any) ([]Memory, error) {
	rows, err := source.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}
	defer rows.Close()

	values := []Memory{}
	for rows.Next() {
		value, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query memories: %w", err)
	}
	return values, nil
}

func scanMemory(source scanner) (Memory, error) {
	value, _, err := scanColumns(source, false, false)
	return value, err
}

// scanRevision reads revisionColumns.
func scanRevision(source scanner) (Revision, error) {
	value, revision, err := scanColumns(source, true, true)
	if err != nil {
		return Revision{}, err
	}
	revision.Memory = value
	return revision, nil
}

// scanRevisionV1 reads revisionColumnsV1, which lack the chain columns.
func scanRevisionV1(source scanner) (Revision, error) {
	value, revision, err := scanColumns(source, true, false)
	if err != nil {
		return Revision{}, err
	}
	revision.Memory = value
	return revision, nil
}

func scanColumns(source scanner, withRevision, withChain bool) (Memory, Revision, error) {
	var value Memory
	var revision Revision
	var number, projectSequence int64
	var workspaceID, validUntil, supersedes, related sql.NullString
	var status, scope, evidence, validFrom, createdAt, updatedAt string
	var action, changeEvidence, recordedAt string

	destinations := []any{
		&value.ProjectID, &value.MemoryID, &number, &workspaceID,
		&status, &value.Priority, &value.Kind, &value.Claim, &scope,
		&evidence, &value.Source, &value.Confidence, &validFrom, &validUntil,
		&createdAt, &updatedAt, &supersedes, &value.ContentHash,
	}
	if withRevision {
		destinations = append(destinations, &action, &revision.Reason, &changeEvidence, &related, &recordedAt)
	}
	if withChain {
		destinations = append(destinations, &projectSequence, &revision.PreviousHash, &revision.RevisionHash)
	}
	if err := source.Scan(destinations...); err != nil {
		return Memory{}, Revision{}, err
	}

	value.Revision = uint64(number)
	revision.ProjectSequence = uint64(projectSequence)
	value.WorkspaceID = workspaceID.String
	value.Status = Status(status)
	value.Scope = json.RawMessage(scope)
	value.SupersedesMemoryID = supersedes.String
	if err := json.Unmarshal([]byte(evidence), &value.EvidenceRefs); err != nil {
		return Memory{}, Revision{}, fmt.Errorf("decode memory %q evidence_refs: %w", value.MemoryID, err)
	}
	var err error
	for _, field := range []struct {
		name   string
		text   string
		target *time.Time
	}{
		{"valid_from", validFrom, &value.ValidFrom},
		{"valid_until", validUntil.String, &value.ValidUntil},
		{"created_at", createdAt, &value.CreatedAt},
		{"updated_at", updatedAt, &value.UpdatedAt},
		{"recorded_at", recordedAt, &revision.RecordedAt},
	} {
		if field.text == "" {
			continue
		}
		if *field.target, err = parseTime(field.text); err != nil {
			return Memory{}, Revision{}, fmt.Errorf("decode memory %q %s: %w", value.MemoryID, field.name, err)
		}
	}
	if withRevision {
		revision.Action = Action(action)
		revision.RelatedMemoryID = related.String
		if err := json.Unmarshal([]byte(changeEvidence), &revision.ChangeEvidenceRefs); err != nil {
			return Memory{}, Revision{}, fmt.Errorf("decode memory %q change_evidence_refs: %w", value.MemoryID, err)
		}
	}
	return value, revision, nil
}

// correctionActions are the revisions that make an earlier memory unsafe to
// rely on.
var correctionActions = []Action{ActionSupersede, ActionRetract, ActionContest, ActionMarkStale}

// ChangesSince returns, in recording order, the revisions recorded after
// since that superseded, retracted, contested or staled a memory of the
// project. At most limit revisions are returned.
func (store *Store) ChangesSince(ctx context.Context, projectID string, since time.Time, limit int) ([]Revision, error) {
	return store.RevisionsSince(ctx, projectID, since, limit, correctionActions)
}

// RevisionsSince returns, in recording order, the revisions with one of
// actions recorded after since.
func (store *Store) RevisionsSince(ctx context.Context, projectID string, since time.Time, limit int, actions []Action) ([]Revision, error) {
	if len(actions) == 0 {
		return nil, fmt.Errorf("memory changes: %w", invalid("at least one action is required"))
	}
	if err := validateIdentifier("project_id", projectID); err != nil {
		return nil, fmt.Errorf("memory changes: %w", err)
	}
	if limit <= 0 || limit > MaximumRetrieveLimit {
		return nil, fmt.Errorf("memory changes: %w", invalid("limit %d must be within 1..%d", limit, MaximumRetrieveLimit))
	}
	args := []any{projectID, formatTime(since)}
	for _, action := range actions {
		args = append(args, string(action))
	}
	args = append(args, limit)
	rows, err := store.db.QueryContext(ctx,
		`SELECT `+revisionColumns+` FROM memory_revisions
		WHERE project_id = ? AND recorded_at > ? AND action IN (`+placeholders(len(actions))+`)
		ORDER BY project_sequence
		LIMIT ?`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("memory changes in project %q: %w", projectID, err)
	}
	defer rows.Close()
	revisions := []Revision{}
	for rows.Next() {
		revision, err := scanRevision(rows)
		if err != nil {
			return nil, fmt.Errorf("memory changes in project %q: %w", projectID, err)
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory changes in project %q: %w", projectID, err)
	}
	return revisions, nil
}
