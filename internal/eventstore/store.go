package eventstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const storeSchemaVersion = 2

// Every write transaction starts with BEGIN IMMEDIATE (_txlock), so appenders
// take the SQLite write lock before reading the chain head. Read-only
// transactions stay deferred and read a WAL snapshot without blocking writers.
//
// synchronous(NORMAL) does not flush the WAL on every commit. A crash of any
// process loses nothing; an operating system crash or power loss can lose the
// last commits, never corrupts the database, and leaves a valid chain that
// ends a few events earlier. Flushing every commit made each hook wait for
// the disk (66 ms per flush on WSL2), so hosts were more likely to time out
// the hook than to lose events.
const dsnOptions = "?_txlock=immediate" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)"

const schemaSQL = `
CREATE TABLE IF NOT EXISTS projects (
	project_id    TEXT PRIMARY KEY,
	head_sequence INTEGER NOT NULL CHECK (head_sequence >= 1),
	head_hash     TEXT NOT NULL,
	updated_at    TEXT NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS events (
	project_id     TEXT NOT NULL,
	sequence       INTEGER NOT NULL CHECK (sequence >= 1),
	event_id       TEXT NOT NULL UNIQUE,
	schema_version TEXT NOT NULL,
	workspace_id   TEXT NOT NULL,
	occurred_at    TEXT NOT NULL,
	recorded_at    TEXT NOT NULL,
	type           TEXT NOT NULL,
	actor          TEXT NOT NULL CHECK (json_valid(actor)),
	causality      TEXT NOT NULL CHECK (json_valid(causality)),
	payload        TEXT NOT NULL CHECK (json_valid(payload)),
	evidence_refs  TEXT NOT NULL CHECK (json_valid(evidence_refs)),
	previous_hash  TEXT NOT NULL,
	payload_hash   TEXT NOT NULL,
	event_hash     TEXT NOT NULL,
	PRIMARY KEY (project_id, sequence)
) STRICT;

CREATE TRIGGER IF NOT EXISTS events_no_update BEFORE UPDATE ON events
BEGIN
	SELECT RAISE(ABORT, 'events are append-only');
END;

CREATE TRIGGER IF NOT EXISTS events_no_delete BEFORE DELETE ON events
BEGIN
	SELECT RAISE(ABORT, 'events are append-only');
END;

PRAGMA user_version = 1;
`

// indexSQL (schema version 2) lets hot-path readers fetch the events of one
// type or one session without scanning the project. The session indexes use
// the same expressions as the queries in ListMatching.
const indexSQL = `
CREATE INDEX IF NOT EXISTS events_by_type ON events(project_id, type, sequence);
CREATE INDEX IF NOT EXISTS events_by_native_session ON events(project_id, json_extract(actor, '$.native_session_id'), sequence);
CREATE INDEX IF NOT EXISTS events_by_acr_session ON events(project_id, json_extract(actor, '$.acr_session_id'), sequence);
PRAGMA user_version = 2;
`

const eventColumns = `schema_version, event_id, project_id, workspace_id, sequence,
	occurred_at, recorded_at, type, actor, causality, payload, evidence_refs,
	previous_hash, payload_hash, event_hash`

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("open event store: path is required")
	}
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("open event store %q: path must not contain '?'", path)
	}

	db, err := sql.Open("sqlite", path+dsnOptions)
	if err != nil {
		return nil, fmt.Errorf("open event store %q: %w", path, err)
	}
	store := &Store{db: db}
	if err := store.initSchema(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("open event store %q: %w", path, err)
	}
	return store, nil
}

func (store *Store) Close() error {
	if err := store.db.Close(); err != nil {
		return fmt.Errorf("close event store: %w", err)
	}
	return nil
}

func (store *Store) initSchema(ctx context.Context) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema transaction: %w", err)
	}
	defer tx.Rollback()

	var version int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	switch version {
	case storeSchemaVersion:
		return nil
	case 0:
		if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
			return fmt.Errorf("create schema: %w", err)
		}
	case 1:
	default:
		return fmt.Errorf("unsupported schema version %d, want %d", version, storeSchemaVersion)
	}

	if _, err := tx.ExecContext(ctx, indexSQL); err != nil {
		return fmt.Errorf("create event indexes: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema: %w", err)
	}
	return nil
}

func newRecord(request AppendRequest) (record, error) {
	if strings.TrimSpace(request.ProjectID) == "" {
		return record{}, errors.New("project_id is required")
	}
	if strings.TrimSpace(request.Type) == "" {
		return record{}, errors.New("type is required")
	}
	if request.OccurredAt.IsZero() {
		return record{}, errors.New("occurred_at is required")
	}

	payload, err := canonicalJSON(request.Payload)
	if err != nil {
		return record{}, fmt.Errorf("payload: %w", err)
	}
	actor, err := canonicalMarshal(request.Actor)
	if err != nil {
		return record{}, fmt.Errorf("actor: %w", err)
	}
	causality, err := canonicalMarshal(request.Causality)
	if err != nil {
		return record{}, fmt.Errorf("causality: %w", err)
	}
	evidenceRefs := request.EvidenceRefs
	if evidenceRefs == nil {
		evidenceRefs = []string{}
	}
	evidence, err := canonicalMarshal(evidenceRefs)
	if err != nil {
		return record{}, fmt.Errorf("evidence_refs: %w", err)
	}

	eventID := request.EventID
	if eventID == "" {
		eventID = rand.Text()
	}

	return record{
		SchemaVersion: SchemaVersion,
		EventID:       eventID,
		ProjectID:     request.ProjectID,
		WorkspaceID:   request.WorkspaceID,
		OccurredAt:    formatTime(request.OccurredAt),
		Type:          request.Type,
		Actor:         actor,
		Causality:     causality,
		Payload:       string(payload),
		EvidenceRefs:  evidence,
	}, nil
}

// Append reads the project head, assigns the next sequence, hashes, inserts
// the event and moves the head inside one write transaction.
func (store *Store) Append(ctx context.Context, request AppendRequest) (Event, error) {
	events, err := store.AppendBatch(ctx, []AppendRequest{request})
	if err != nil {
		return Event{}, err
	}
	return events[0], nil
}

// AppendBatch appends requests in order within one transaction: either all
// of them become part of their projects' chains or none does.
func (store *Store) AppendBatch(ctx context.Context, requests []AppendRequest) ([]Event, error) {
	rows := make([]record, 0, len(requests))
	for _, request := range requests {
		row, err := newRecord(request)
		if err != nil {
			return nil, fmt.Errorf("append event to project %q: %w", request.ProjectID, err)
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, errors.New("append events: no events")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("append event to project %q: begin: %w", rows[0].ProjectID, err)
	}
	defer tx.Rollback()
	events := make([]Event, 0, len(rows))
	for index := range rows {
		if err := appendInTx(ctx, tx, &rows[index]); err != nil {
			return nil, err
		}
		event, err := rows[index].event()
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("append event to project %q sequence %d: commit: %w", rows[0].ProjectID, rows[0].Sequence, err)
	}
	return events, nil
}

// appendInTx links row to its project's chain and inserts it.
func appendInTx(ctx context.Context, tx *sql.Tx, row *record) error {
	var headSequence int64
	err := tx.QueryRowContext(ctx,
		`SELECT head_sequence, head_hash FROM projects WHERE project_id = ?`,
		row.ProjectID,
	).Scan(&headSequence, &row.PreviousHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("append event to project %q: read head: %w", row.ProjectID, err)
	}

	row.Sequence = uint64(headSequence) + 1
	row.RecordedAt = formatTime(time.Now())
	row.PayloadHash = hashHex([]byte(row.Payload))
	if row.EventHash, err = row.computeEventHash(); err != nil {
		return fmt.Errorf("append event to project %q: %w", row.ProjectID, err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO events (`+eventColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.SchemaVersion, row.EventID, row.ProjectID, row.WorkspaceID, int64(row.Sequence),
		row.OccurredAt, row.RecordedAt, row.Type, row.Actor, row.Causality, row.Payload, row.EvidenceRefs,
		row.PreviousHash, row.PayloadHash, row.EventHash,
	)
	if err != nil {
		return fmt.Errorf("append event to project %q sequence %d: insert: %w", row.ProjectID, row.Sequence, err)
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO projects (project_id, head_sequence, head_hash, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (project_id) DO UPDATE SET
			head_sequence = excluded.head_sequence,
			head_hash = excluded.head_hash,
			updated_at = excluded.updated_at`,
		row.ProjectID, int64(row.Sequence), row.EventHash, row.RecordedAt,
	)
	if err != nil {
		return fmt.Errorf("append event to project %q sequence %d: update head: %w", row.ProjectID, row.Sequence, err)
	}

	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRecord(source scanner) (record, error) {
	var row record
	var sequence int64
	err := source.Scan(
		&row.SchemaVersion, &row.EventID, &row.ProjectID, &row.WorkspaceID, &sequence,
		&row.OccurredAt, &row.RecordedAt, &row.Type, &row.Actor, &row.Causality, &row.Payload, &row.EvidenceRefs,
		&row.PreviousHash, &row.PayloadHash, &row.EventHash,
	)
	if err != nil {
		return record{}, err
	}
	row.Sequence = uint64(sequence)
	return row, nil
}

// List returns up to limit events of a project with sequence greater than
// afterSequence, in sequence order. An unknown project yields no events.
func (store *Store) List(ctx context.Context, projectID string, afterSequence uint64, limit int) ([]Event, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("list events of project %q: limit must be positive, got %d", projectID, limit)
	}
	if afterSequence > math.MaxInt64 {
		return nil, fmt.Errorf("list events of project %q: after sequence %d is out of range", projectID, afterSequence)
	}

	rows, err := store.db.QueryContext(ctx,
		`SELECT `+eventColumns+` FROM events
		WHERE project_id = ? AND sequence > ?
		ORDER BY sequence
		LIMIT ?`,
		projectID, int64(afterSequence), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list events of project %q: %w", projectID, err)
	}
	defer rows.Close()

	events := []Event{}
	for rows.Next() {
		row, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("list events of project %q: scan: %w", projectID, err)
		}
		value, err := row.event()
		if err != nil {
			return nil, fmt.Errorf("list events of project %q: %w", projectID, err)
		}
		events = append(events, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list events of project %q: %w", projectID, err)
	}
	return events, nil
}

// Filter selects events by type or session. Empty fields match every event,
// and set fields must all match.
type Filter struct {
	Types           []string
	NativeSessionID string
	ACRSessionID    string
}

// ListMatching is List restricted to the events that match filter.
func (store *Store) ListMatching(ctx context.Context, projectID string, afterSequence uint64, limit int, filter Filter) ([]Event, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("list events of project %q: limit must be positive, got %d", projectID, limit)
	}
	if afterSequence > math.MaxInt64 {
		return nil, fmt.Errorf("list events of project %q: after sequence %d is out of range", projectID, afterSequence)
	}
	query, args := matchingQuery(projectID, afterSequence, filter, "ASC")
	rows, err := store.db.QueryContext(ctx, query, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("list events of project %q: %w", projectID, err)
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		row, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("list events of project %q: scan: %w", projectID, err)
		}
		value, err := row.event()
		if err != nil {
			return nil, fmt.Errorf("list events of project %q: %w", projectID, err)
		}
		events = append(events, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list events of project %q: %w", projectID, err)
	}
	return events, nil
}

// matchingQuery builds a filtered query in sequence order (ASC or DESC); the
// caller appends the limit.
func matchingQuery(projectID string, afterSequence uint64, filter Filter, order string) (string, []any) {
	// SQLite prefers walking the primary key in sequence order whenever a
	// query is ordered and limited, which reads the whole project to find a
	// few events of one type. Naming the index keeps these reads proportional
	// to the events they return.
	index := ""
	switch {
	case filter.NativeSessionID != "":
		index = ` INDEXED BY events_by_native_session`
	case filter.ACRSessionID != "":
		index = ` INDEXED BY events_by_acr_session`
	case len(filter.Types) > 0:
		index = ` INDEXED BY events_by_type`
	}
	query := `SELECT ` + eventColumns + ` FROM events` + index + ` WHERE project_id = ? AND sequence > ?`
	args := []any{projectID, int64(afterSequence)}
	if len(filter.Types) > 0 {
		query += ` AND type IN (?` + strings.Repeat(`, ?`, len(filter.Types)-1) + `)`
		for _, value := range filter.Types {
			args = append(args, value)
		}
	}
	if filter.NativeSessionID != "" {
		query += ` AND json_extract(actor, '$.native_session_id') = ?`
		args = append(args, filter.NativeSessionID)
	}
	if filter.ACRSessionID != "" {
		query += ` AND json_extract(actor, '$.acr_session_id') = ?`
		args = append(args, filter.ACRSessionID)
	}
	return query + ` ORDER BY sequence ` + order + ` LIMIT ?`, args
}

// Latest returns the last limit events that match filter, oldest first.
func (store *Store) Latest(ctx context.Context, projectID string, limit int, filter Filter) ([]Event, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("latest events of project %q: limit must be positive, got %d", projectID, limit)
	}
	query, args := matchingQuery(projectID, 0, filter, "DESC")
	rows, err := store.db.QueryContext(ctx, query, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("latest events of project %q: %w", projectID, err)
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		row, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("latest events of project %q: scan: %w", projectID, err)
		}
		value, err := row.event()
		if err != nil {
			return nil, fmt.Errorf("latest events of project %q: %w", projectID, err)
		}
		events = append(events, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("latest events of project %q: %w", projectID, err)
	}
	slices.Reverse(events)
	return events, nil
}

// Each visits, in order, the events after afterSequence that match filter,
// and stops at the first error visit returns.
func (store *Store) Each(ctx context.Context, projectID string, afterSequence uint64, filter Filter, visit func(Event) error) error {
	for after := afterSequence; ; {
		events, err := store.ListMatching(ctx, projectID, after, 500, filter)
		if err != nil || len(events) == 0 {
			return err
		}
		for _, event := range events {
			after = event.Sequence
			if err := visit(event); err != nil {
				return err
			}
		}
	}
}

// Head returns the latest sequence and event hash of a project, or
// ErrProjectNotFound when the project has no events.
func (store *Store) Head(ctx context.Context, projectID string) (ProjectHead, error) {
	return readHead(ctx, store.db, projectID)
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readHead(ctx context.Context, source queryRower, projectID string) (ProjectHead, error) {
	var sequence int64
	var updatedAt string
	head := ProjectHead{ProjectID: projectID}
	err := source.QueryRowContext(ctx,
		`SELECT head_sequence, head_hash, updated_at FROM projects WHERE project_id = ?`,
		projectID,
	).Scan(&sequence, &head.EventHash, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectHead{}, fmt.Errorf("head of project %q: %w", projectID, ErrProjectNotFound)
	}
	if err != nil {
		return ProjectHead{}, fmt.Errorf("head of project %q: %w", projectID, err)
	}
	head.Sequence = uint64(sequence)
	if head.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return ProjectHead{}, fmt.Errorf("head of project %q: decode updated_at: %w", projectID, err)
	}
	return head, nil
}

// Verify recomputes the whole project chain from one read snapshot. Chain
// defects wrap ErrIntegrity; a project without head and events returns
// ErrProjectNotFound.
func (store *Store) Verify(ctx context.Context, projectID string) error {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("verify project %q: begin: %w", projectID, err)
	}
	defer tx.Rollback()

	head, err := readHead(ctx, tx, projectID)
	headFound := err == nil
	if err != nil && !errors.Is(err, ErrProjectNotFound) {
		return fmt.Errorf("verify project %q: %w", projectID, err)
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT `+eventColumns+` FROM events WHERE project_id = ? ORDER BY sequence`,
		projectID,
	)
	if err != nil {
		return fmt.Errorf("verify project %q: %w", projectID, err)
	}
	defer rows.Close()

	var lastSequence uint64
	lastHash := ""
	for rows.Next() {
		row, err := scanRecord(rows)
		if err != nil {
			return fmt.Errorf("verify project %q: scan: %w", projectID, err)
		}
		if row.Sequence != lastSequence+1 {
			return fmt.Errorf("verify project %q: sequence %d follows %d: %w", projectID, row.Sequence, lastSequence, ErrIntegrity)
		}
		if row.PreviousHash != lastHash {
			return fmt.Errorf("verify project %q sequence %d: previous_hash mismatch: %w", projectID, row.Sequence, ErrIntegrity)
		}
		if hashHex([]byte(row.Payload)) != row.PayloadHash {
			return fmt.Errorf("verify project %q sequence %d: payload_hash mismatch: %w", projectID, row.Sequence, ErrIntegrity)
		}
		eventHash, err := row.computeEventHash()
		if err != nil {
			return fmt.Errorf("verify project %q sequence %d: %w: %w", projectID, row.Sequence, ErrIntegrity, err)
		}
		if eventHash != row.EventHash {
			return fmt.Errorf("verify project %q sequence %d: event_hash mismatch: %w", projectID, row.Sequence, ErrIntegrity)
		}
		lastSequence = row.Sequence
		lastHash = row.EventHash
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("verify project %q: %w", projectID, err)
	}

	if !headFound {
		if lastSequence == 0 {
			return fmt.Errorf("verify project %q: %w", projectID, ErrProjectNotFound)
		}
		return fmt.Errorf("verify project %q: events exist without head: %w", projectID, ErrIntegrity)
	}
	if head.Sequence != lastSequence || head.EventHash != lastHash {
		return fmt.Errorf("verify project %q: head %d does not match last event %d: %w", projectID, head.Sequence, lastSequence, ErrIntegrity)
	}
	return nil
}
