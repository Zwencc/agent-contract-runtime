// Package projector derives a rebuildable project state graph from the event
// log. The graph is never a source of truth: every node lists the events it
// came from, and deleting the projection database loses nothing because Sync
// rebuilds it from events.
package projector

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/verification"

	_ "modernc.org/sqlite"
)

// Version changes whenever projection rules change; a stored projection of
// another version is rebuilt from events.
const Version = 1

const applicationID = 0x41435250 // "ACRP"

const dsnOptions = "?_txlock=immediate" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)"

const schemaSQL = `
CREATE TABLE projection_state (
	project_id        TEXT PRIMARY KEY,
	projector_version INTEGER NOT NULL,
	last_sequence     INTEGER NOT NULL,
	last_event_hash   TEXT NOT NULL
) STRICT;

CREATE TABLE graph_nodes (
	project_id           TEXT NOT NULL,
	node_id              TEXT NOT NULL,
	type                 TEXT NOT NULL,
	key                  TEXT NOT NULL,
	attributes           TEXT NOT NULL CHECK (json_valid(attributes)),
	confidence           TEXT NOT NULL CHECK (confidence IN ('observed', 'verified', 'inferred')),
	valid_from_sequence  INTEGER NOT NULL,
	valid_until_sequence INTEGER,
	updated_sequence     INTEGER NOT NULL,
	PRIMARY KEY (project_id, node_id)
) STRICT;

CREATE TABLE graph_edges (
	project_id      TEXT NOT NULL,
	from_id         TEXT NOT NULL,
	type            TEXT NOT NULL,
	to_id           TEXT NOT NULL,
	source_event_id TEXT NOT NULL,
	PRIMARY KEY (project_id, from_id, type, to_id)
) STRICT;

CREATE INDEX graph_edges_to ON graph_edges (project_id, to_id, type);

CREATE TABLE projection_sources (
	project_id TEXT NOT NULL,
	node_id    TEXT NOT NULL,
	event_id   TEXT NOT NULL,
	sequence   INTEGER NOT NULL,
	PRIMARY KEY (project_id, node_id, event_id)
) STRICT;

PRAGMA application_id = 1094931024;
`

const (
	NodeArtifact     = "Artifact"
	NodeModule       = "Module"
	NodeFailure      = "FailurePattern"
	NodeVerification = "Verification"

	EdgeContains = "CONTAINS"
	EdgeFailedIn = "FAILED_IN"
)

type Node struct {
	ID                 string          `json:"id"`
	Type               string          `json:"type"`
	Key                string          `json:"key"`
	Attributes         json.RawMessage `json:"attributes"`
	Confidence         string          `json:"confidence"`
	ValidFromSequence  uint64          `json:"valid_from_sequence"`
	ValidUntilSequence uint64          `json:"valid_until_sequence,omitempty"`
	UpdatedSequence    uint64          `json:"updated_sequence"`
	SourceEventIDs     []string        `json:"source_event_ids"`
}

type Edge struct {
	FromID        string `json:"from_id"`
	Type          string `json:"type"`
	ToID          string `json:"to_id"`
	SourceEventID string `json:"source_event_id"`
}

type FailureAttributes struct {
	Tool             string   `json:"tool"`
	Signature        string   `json:"signature"`
	Occurrences      int      `json:"occurrences"`
	LastEventID      string   `json:"last_event_id"`
	LastSequence     uint64   `json:"last_sequence"`
	Paths            []string `json:"paths"`
}

type VerificationAttributes struct {
	Subject  string             `json:"subject"`
	State    verification.State `json:"state"`
	Patterns []string           `json:"patterns"`
	Digest   string             `json:"digest"`
	ExitCode int                `json:"exit_code"`
	EventID  string             `json:"event_id"`
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" || strings.Contains(path, "?") {
		return nil, fmt.Errorf("open projection store %q: path must be non-empty and contain no '?'", path)
	}
	db, err := sql.Open("sqlite", path+dsnOptions)
	if err != nil {
		return nil, fmt.Errorf("open projection store %q: %w", path, err)
	}
	store := &Store{db: db}
	if err := store.initSchema(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("open projection store %q: %w", path, err)
	}
	return store, nil
}

func (store *Store) Close() error {
	if err := store.db.Close(); err != nil {
		return fmt.Errorf("close projection store: %w", err)
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
	if id == applicationID {
		return nil
	}
	var objects int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master`).Scan(&objects); err != nil {
		return fmt.Errorf("inspect database: %w", err)
	}
	if id != 0 || objects > 0 {
		return errors.New("database is not an ACR projection store")
	}
	if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	return tx.Commit()
}

// Sync applies events recorded since the last sync. It rebuilds the
// project's projection from the first event when the projector version
// changed or the event at the stored cursor is no longer the one it saw.
func (store *Store) Sync(ctx context.Context, events *eventstore.Store, projectID, root string) (int, error) {
	applied, err := store.sync(ctx, events, projectID, root)
	if err != nil {
		return applied, fmt.Errorf("sync projection of project %q: %w", projectID, err)
	}
	return applied, nil
}

func (store *Store) sync(ctx context.Context, events *eventstore.Store, projectID, root string) (int, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	var version int
	var after uint64
	var lastHash string
	err = tx.QueryRowContext(ctx,
		`SELECT projector_version, last_sequence, last_event_hash FROM projection_state WHERE project_id = ?`,
		projectID,
	).Scan(&version, &after, &lastHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("read projection state: %w", err)
	}
	rebuild := errors.Is(err, sql.ErrNoRows) || version != Version
	if !rebuild && after > 0 {
		cursor, err := events.List(ctx, projectID, after-1, 1)
		if err != nil {
			return 0, err
		}
		rebuild = len(cursor) != 1 || cursor[0].Sequence != after || cursor[0].EventHash != lastHash
	}
	if rebuild {
		for _, table := range []string{"graph_nodes", "graph_edges", "projection_sources", "projection_state"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE project_id = ?`, projectID); err != nil {
				return 0, fmt.Errorf("clear %s: %w", table, err)
			}
		}
		after, lastHash = 0, ""
	}

	applied := 0
	for {
		batch, err := events.List(ctx, projectID, after, 500)
		if err != nil {
			return applied, err
		}
		if len(batch) == 0 {
			break
		}
		for _, event := range batch {
			if err := apply(ctx, tx, projectID, root, event); err != nil {
				return applied, fmt.Errorf("apply event %d (%s): %w", event.Sequence, event.Type, err)
			}
			after, lastHash = event.Sequence, event.EventHash
			applied++
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO projection_state (project_id, projector_version, last_sequence, last_event_hash) VALUES (?, ?, ?, ?)
		ON CONFLICT (project_id) DO UPDATE SET projector_version = excluded.projector_version,
			last_sequence = excluded.last_sequence, last_event_hash = excluded.last_event_hash`,
		projectID, Version, int64(after), lastHash,
	); err != nil {
		return applied, fmt.Errorf("record projection state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return applied, fmt.Errorf("commit: %w", err)
	}
	return applied, nil
}

func apply(ctx context.Context, tx *sql.Tx, projectID, root string, event eventstore.Event) error {
	switch event.Type {
	case "action.completed", "action.failed":
		var payload struct {
			ToolName  string          `json:"tool_name"`
			Resources []string        `json:"resources"`
			ToolInput json.RawMessage `json:"tool_input"`
			Error     string          `json:"error"`
			ErrorRef  json.RawMessage `json:"error_ref"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode payload: %w", err)
		}
		resources := payload.Resources
		if resources == nil {
			resources = legacyResources(payload.ToolInput)
		}
		paths := projectPaths(root, resources)
		for _, filePath := range paths {
			if err := touchArtifact(ctx, tx, projectID, filePath, event); err != nil {
				return err
			}
		}
		if event.Type == "action.failed" {
			text := payload.Error
			if text == "" && len(payload.ErrorRef) > 0 {
				text = "[error stored as object]"
			}
			return recordFailure(ctx, tx, projectID, payload.ToolName, text, paths, event)
		}
	case "repair.failed":
		var payload struct {
			PlanID        string   `json:"plan_id"`
			StepID        string   `json:"step_id"`
			Argv          []string `json:"argv"`
			ExecutorError string   `json:"executor_error"`
			Outcome       struct {
				ExitCode int    `json:"exit_code"`
				Stderr   string `json:"stderr"`
			} `json:"outcome"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode payload: %w", err)
		}
		text := payload.ExecutorError
		if text == "" {
			text = fmt.Sprintf("exit %d: %s", payload.Outcome.ExitCode, payload.Outcome.Stderr)
		}
		return recordFailure(ctx, tx, projectID, "repair:"+payload.PlanID+"/"+payload.StepID, text, nil, event)
	case verification.EventPassed, verification.EventFailed:
		var result verification.Result
		if err := json.Unmarshal(event.Payload, &result); err != nil {
			return fmt.Errorf("decode payload: %w", err)
		}
		state := verification.StateFailed
		if event.Type == verification.EventPassed {
			state = verification.StateValid
			if !result.Stable {
				state = verification.StateUnstable
			}
		}
		attributes := VerificationAttributes{Subject: result.Subject, State: state, Patterns: result.Inputs.Patterns,
			Digest: result.Inputs.Digest, ExitCode: result.ExitCode, EventID: event.EventID}
		// Each new result starts a new validity period for the subject.
		return putNode(ctx, tx, projectID, Node{ID: "verification:" + result.Subject, Type: NodeVerification, Key: result.Subject,
			Confidence: "verified", ValidFromSequence: event.Sequence}, attributes, event, true)
	case verification.EventInvalidated:
		var invalidation verification.Invalidation
		if err := json.Unmarshal(event.Payload, &invalidation); err != nil {
			return fmt.Errorf("decode payload: %w", err)
		}
		node, attributes, ok, err := getVerification(ctx, tx, projectID, "verification:"+invalidation.Subject)
		if err != nil || !ok || attributes.EventID != invalidation.VerificationEventID || attributes.State != verification.StateValid {
			return err
		}
		attributes.State = verification.StateInvalidated
		node.ValidUntilSequence = event.Sequence
		return putNode(ctx, tx, projectID, node, attributes, event, false)
	}
	return nil
}

// legacyResources reads the path fields of events recorded before tool
// outcomes carried their resources.
func legacyResources(raw json.RawMessage) []string {
	var input map[string]any
	if json.Unmarshal(raw, &input) != nil {
		return nil
	}
	resources := []string{}
	for _, field := range []string{"file_path", "path"} {
		if value, ok := input[field].(string); ok && value != "" {
			resources = append(resources, value)
		}
	}
	return resources
}

// projectPaths maps resources onto clean project-relative slash paths and
// drops anything outside the project root.
func projectPaths(root string, resources []string) []string {
	roots := []string{root}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != root {
		roots = append(roots, resolved)
	}
	seen := map[string]bool{}
	paths := []string{}
	for _, resource := range resources {
		candidate := filepath.Clean(resource)
		if filepath.IsAbs(candidate) {
			relative := ""
			for _, base := range roots {
				if value, err := filepath.Rel(base, candidate); err == nil && value != ".." && !strings.HasPrefix(value, ".."+string(filepath.Separator)) {
					relative = value
					break
				}
			}
			if relative == "" {
				continue
			}
			candidate = relative
		}
		candidate = filepath.ToSlash(candidate)
		if candidate == "." || candidate == ".." || strings.HasPrefix(candidate, "../") || seen[candidate] {
			continue
		}
		seen[candidate] = true
		paths = append(paths, candidate)
	}
	sort.Strings(paths)
	return paths
}

func touchArtifact(ctx context.Context, tx *sql.Tx, projectID, filePath string, event eventstore.Event) error {
	if err := putNode(ctx, tx, projectID, Node{ID: "artifact:" + filePath, Type: NodeArtifact, Key: filePath,
		Confidence: "observed", ValidFromSequence: event.Sequence}, map[string]string{"path": filePath}, event, false); err != nil {
		return err
	}
	directory := path.Dir(filePath)
	if err := putNode(ctx, tx, projectID, Node{ID: "module:" + directory, Type: NodeModule, Key: directory,
		Confidence: "observed", ValidFromSequence: event.Sequence}, map[string]string{"directory": directory}, event, false); err != nil {
		return err
	}
	return putEdge(ctx, tx, projectID, Edge{FromID: "module:" + directory, Type: EdgeContains, ToID: "artifact:" + filePath, SourceEventID: event.EventID})
}

var (
	digitsPattern     = regexp.MustCompile(`[0-9]+`)
	whitespacePattern = regexp.MustCompile(`\s+`)
)

// failureSignature is the first non-empty line of an error with digits and
// whitespace normalized, so repeats that differ only in numbers group.
func failureSignature(text string) string {
	line := ""
	for _, candidate := range strings.Split(text, "\n") {
		if strings.TrimSpace(candidate) != "" {
			line = candidate
			break
		}
	}
	line = whitespacePattern.ReplaceAllString(strings.TrimSpace(line), " ")
	line = digitsPattern.ReplaceAllString(line, "#")
	if len(line) > 200 {
		line = line[:200]
	}
	return line
}

func recordFailure(ctx context.Context, tx *sql.Tx, projectID, tool, text string, paths []string, event eventstore.Event) error {
	signature := failureSignature(text)
	sum := sha256.Sum256([]byte(tool + "\x00" + signature))
	id := "failure:" + hex.EncodeToString(sum[:8])

	attributes := FailureAttributes{Tool: tool, Signature: signature}
	node := Node{ID: id, Type: NodeFailure, Key: tool + ": " + signature, Confidence: "observed", ValidFromSequence: event.Sequence}
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT attributes FROM graph_nodes WHERE project_id = ? AND node_id = ?`, projectID, id).Scan(&raw)
	if err == nil {
		if err := json.Unmarshal([]byte(raw), &attributes); err != nil {
			return fmt.Errorf("decode failure %s: %w", id, err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read failure %s: %w", id, err)
	}
	attributes.Occurrences++
	attributes.LastEventID = event.EventID
	attributes.LastSequence = event.Sequence
	attributes.Paths = mergeSorted(attributes.Paths, paths)
	if err := putNode(ctx, tx, projectID, node, attributes, event, false); err != nil {
		return err
	}
	for _, filePath := range paths {
		if err := putEdge(ctx, tx, projectID, Edge{FromID: id, Type: EdgeFailedIn, ToID: "artifact:" + filePath, SourceEventID: event.EventID}); err != nil {
			return err
		}
	}
	return nil
}

func mergeSorted(current, added []string) []string {
	values := append(append([]string{}, current...), added...)
	sort.Strings(values)
	result := []string{}
	for i, value := range values {
		if i == 0 || value != values[i-1] {
			result = append(result, value)
		}
	}
	return result
}

// putNode inserts or updates a node and records event as one of its sources.
// restart begins a new validity period; otherwise the original start stays.
func putNode(ctx context.Context, tx *sql.Tx, projectID string, node Node, attributes any, event eventstore.Event, restart bool) error {
	encoded, err := json.Marshal(attributes)
	if err != nil {
		return fmt.Errorf("encode node %s: %w", node.ID, err)
	}
	var validUntil any
	if node.ValidUntilSequence != 0 {
		validUntil = int64(node.ValidUntilSequence)
	}
	conflict := `attributes = excluded.attributes, updated_sequence = excluded.updated_sequence, valid_until_sequence = excluded.valid_until_sequence`
	if restart {
		conflict += `, valid_from_sequence = excluded.valid_from_sequence`
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO graph_nodes (project_id, node_id, type, key, attributes, confidence, valid_from_sequence, valid_until_sequence, updated_sequence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (project_id, node_id) DO UPDATE SET `+conflict,
		projectID, node.ID, node.Type, node.Key, string(encoded), node.Confidence, int64(node.ValidFromSequence), validUntil, int64(event.Sequence),
	); err != nil {
		return fmt.Errorf("write node %s: %w", node.ID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO projection_sources (project_id, node_id, event_id, sequence) VALUES (?, ?, ?, ?)`,
		projectID, node.ID, event.EventID, int64(event.Sequence),
	); err != nil {
		return fmt.Errorf("write source of node %s: %w", node.ID, err)
	}
	return nil
}

func putEdge(ctx context.Context, tx *sql.Tx, projectID string, edge Edge) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO graph_edges (project_id, from_id, type, to_id, source_event_id) VALUES (?, ?, ?, ?, ?)`,
		projectID, edge.FromID, edge.Type, edge.ToID, edge.SourceEventID,
	); err != nil {
		return fmt.Errorf("write edge %s -%s-> %s: %w", edge.FromID, edge.Type, edge.ToID, err)
	}
	return nil
}

func getVerification(ctx context.Context, tx *sql.Tx, projectID, id string) (Node, VerificationAttributes, bool, error) {
	nodes, err := queryNodes(ctx, tx, projectID, `node_id = ?`, id)
	if err != nil || len(nodes) == 0 {
		return Node{}, VerificationAttributes{}, false, err
	}
	var attributes VerificationAttributes
	if err := json.Unmarshal(nodes[0].Attributes, &attributes); err != nil {
		return Node{}, VerificationAttributes{}, false, fmt.Errorf("decode verification %s: %w", id, err)
	}
	return nodes[0], attributes, true, nil
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// queryNodes returns nodes matching condition, each with its source events.
func queryNodes(ctx context.Context, source querier, projectID, condition string, args ...any) ([]Node, error) {
	rows, err := source.QueryContext(ctx,
		`SELECT node_id, type, key, attributes, confidence, valid_from_sequence, valid_until_sequence, updated_sequence
		FROM graph_nodes WHERE project_id = ? AND `+condition+` ORDER BY node_id`,
		append([]any{projectID}, args...)...,
	)
	if err != nil {
		return nil, fmt.Errorf("query nodes: %w", err)
	}
	nodes := []Node{}
	for rows.Next() {
		var node Node
		var attributes string
		var validUntil sql.NullInt64
		var from, updated int64
		if err := rows.Scan(&node.ID, &node.Type, &node.Key, &attributes, &node.Confidence, &from, &validUntil, &updated); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan node: %w", err)
		}
		node.Attributes = json.RawMessage(attributes)
		node.ValidFromSequence, node.UpdatedSequence = uint64(from), uint64(updated)
		node.ValidUntilSequence = uint64(validUntil.Int64)
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("query nodes: %w", err)
	}
	rows.Close()

	for i := range nodes {
		sources, err := source.QueryContext(ctx,
			`SELECT event_id FROM projection_sources WHERE project_id = ? AND node_id = ? ORDER BY sequence`,
			projectID, nodes[i].ID,
		)
		if err != nil {
			return nil, fmt.Errorf("query sources of %s: %w", nodes[i].ID, err)
		}
		nodes[i].SourceEventIDs = []string{}
		for sources.Next() {
			var id string
			if err := sources.Scan(&id); err != nil {
				sources.Close()
				return nil, fmt.Errorf("scan source of %s: %w", nodes[i].ID, err)
			}
			nodes[i].SourceEventIDs = append(nodes[i].SourceEventIDs, id)
		}
		if err := sources.Err(); err != nil {
			sources.Close()
			return nil, err
		}
		sources.Close()
	}
	return nodes, nil
}

// Nodes returns every projected node of a project ordered by id.
func (store *Store) Nodes(ctx context.Context, projectID string) ([]Node, error) {
	return queryNodes(ctx, store.db, projectID, `1 = 1`)
}

// Edges returns every projected edge of a project in a stable order.
func (store *Store) Edges(ctx context.Context, projectID string) ([]Edge, error) {
	rows, err := store.db.QueryContext(ctx,
		`SELECT from_id, type, to_id, source_event_id FROM graph_edges WHERE project_id = ? ORDER BY from_id, type, to_id`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("query edges: %w", err)
	}
	defer rows.Close()
	edges := []Edge{}
	for rows.Next() {
		var edge Edge
		if err := rows.Scan(&edge.FromID, &edge.Type, &edge.ToID, &edge.SourceEventID); err != nil {
			return nil, fmt.Errorf("scan edge: %w", err)
		}
		edges = append(edges, edge)
	}
	return edges, rows.Err()
}
