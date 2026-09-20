package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type sqlStep struct {
	statement string
	args      []any
}

func execSteps(t *testing.T, db *sql.DB, steps ...sqlStep) {
	t.Helper()
	for _, step := range steps {
		if _, err := db.Exec(step.statement, step.args...); err != nil {
			t.Fatalf("%s: %v", step.statement, err)
		}
	}
}

func dropAllTriggers(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, name := range append(append([]string{}, baseTriggerNames...), chainTriggerNames...) {
		execSteps(t, db, sqlStep{statement: `DROP TRIGGER IF EXISTS ` + name})
	}
}

// downgradeToV1 rewrites a v2 store into the exact schema v1 layout, keeping
// the original revision order, so migration runs against realistic data.
func downgradeToV1(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()
	dropAllTriggers(t, db)
	execSteps(t, db,
		sqlStep{statement: `ALTER TABLE memory_revisions RENAME TO memory_revisions_v2`},
		sqlStep{statement: `CREATE TABLE memory_revisions (` + snapshotColumnsSQL + revisionBodySQL + `
			PRIMARY KEY (project_id, memory_id, revision)) STRICT`},
		sqlStep{statement: `INSERT INTO memory_revisions SELECT ` + revisionColumnsV1 + `
			FROM memory_revisions_v2 ORDER BY project_id, project_sequence`},
		sqlStep{statement: `DROP TABLE memory_revisions_v2`},
		sqlStep{statement: `DROP TABLE memory_chain_heads`},
		sqlStep{statement: baseTriggersSQL},
		sqlStep{statement: `UPDATE memory_schema SET version = 1`},
	)
}

func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func schemaVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRow(`SELECT version FROM memory_schema`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	return version
}

// allHistories returns every revision history keyed by memory id as JSON.
func allHistories(t *testing.T, store *Store) map[string]string {
	t.Helper()
	rows, err := store.db.Query(`SELECT project_id, memory_id FROM memories`)
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}
	type key struct{ projectID, memoryID string }
	var keys []key
	for rows.Next() {
		var value key
		if err := rows.Scan(&value.projectID, &value.memoryID); err != nil {
			t.Fatalf("scan memory key: %v", err)
		}
		keys = append(keys, value)
	}
	rows.Close()

	histories := map[string]string{}
	for _, value := range keys {
		history, err := store.History(context.Background(), value.projectID, value.memoryID)
		if err != nil {
			t.Fatalf("History() error = %v", err)
		}
		encoded, _ := json.Marshal(history)
		histories[value.memoryID] = string(encoded)
	}
	return histories
}

func TestHistoryCarriesProjectChain(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	first := mustActive(t, store, candidateRequest("proj_a", P2, "first"))
	second := mustActive(t, store, candidateRequest("proj_a", P2, "second"))
	other := mustActive(t, store, candidateRequest("proj_b", P2, "other"))

	firstHistory, _ := store.History(ctx, "proj_a", first.MemoryID)
	secondHistory, _ := store.History(ctx, "proj_a", second.MemoryID)
	otherHistory, _ := store.History(ctx, "proj_b", other.MemoryID)
	chain := append(append([]Revision{}, firstHistory...), secondHistory...)
	previous := ""
	for i, revision := range chain {
		if revision.ProjectSequence != uint64(i+1) || revision.PreviousHash != previous || len(revision.RevisionHash) != 64 {
			t.Fatalf("revision %d chain fields = %d %q %q", i, revision.ProjectSequence, revision.PreviousHash, revision.RevisionHash)
		}
		previous = revision.RevisionHash
	}
	if otherHistory[0].ProjectSequence != 1 || otherHistory[0].PreviousHash != "" {
		t.Fatal("each project must start its own chain")
	}
}

func TestChainTriggersRejectDirectWrites(t *testing.T) {
	store := newTestStore(t)
	value := mustActive(t, store, candidateRequest("proj_a", P2, "claim"))

	_, err := store.db.Exec(`INSERT INTO memory_revisions
		SELECT project_id, 'mem_forged', 1, workspace_id, 'candidate', priority, kind, claim, scope,
			evidence_refs, source, confidence, valid_from, valid_until, created_at, updated_at,
			supersedes_memory_id, content_hash, 'create', reason, change_evidence_refs, related_memory_id, recorded_at,
			(SELECT head_sequence + 1 FROM memory_chain_heads WHERE project_id = 'proj_a'), 'wrong', 'forged'
		FROM memory_revisions WHERE memory_id = ? AND revision = 1`, value.MemoryID)
	if err == nil || !strings.Contains(err.Error(), "chain head") {
		t.Fatalf("inserting an unlinked revision error = %v, want the chain trigger", err)
	}
	for _, statement := range []string{
		`UPDATE memory_chain_heads SET head_sequence = head_sequence + 1`,
		`UPDATE memory_chain_heads SET head_hash = 'forged'`,
		`DELETE FROM memory_chain_heads`,
	} {
		if _, err := store.db.Exec(statement); err == nil {
			t.Fatalf("%q succeeded", statement)
		}
	}
	mustVerify(t, store, "proj_a")
}

func TestVerifyDetectsChainTampering(t *testing.T) {
	cases := map[string]func(fixture map[string]Memory) []sqlStep{
		"revision_hash": func(map[string]Memory) []sqlStep {
			return []sqlStep{{`UPDATE memory_revisions SET revision_hash = 'forged' WHERE project_id = 'proj_a' AND project_sequence = 3`, nil}}
		},
		"previous_hash": func(map[string]Memory) []sqlStep {
			return []sqlStep{{`UPDATE memory_revisions SET previous_hash = '' WHERE project_id = 'proj_a' AND project_sequence = 2`, nil}}
		},
		// reason and recorded_at are outside content_hash; only the chain covers them.
		"revision reason": func(map[string]Memory) []sqlStep {
			return []sqlStep{{`UPDATE memory_revisions SET reason = 'forged' WHERE action = 'supersede'`, nil}}
		},
		"revision recorded_at": func(f map[string]Memory) []sqlStep {
			return []sqlStep{{`UPDATE memory_revisions SET recorded_at = '2000-01-01T00:00:00.000000000Z' WHERE memory_id = ? AND revision = 1`,
				[]any{f["a"].MemoryID}}}
		},
		"whole memory deleted": func(f map[string]Memory) []sqlStep {
			return []sqlStep{
				{`DELETE FROM memory_revisions WHERE memory_id = ?`, []any{f["a"].MemoryID}},
				{`DELETE FROM memories WHERE memory_id = ?`, []any{f["a"].MemoryID}},
			}
		},
		"chain head hash": func(map[string]Memory) []sqlStep {
			return []sqlStep{{`UPDATE memory_chain_heads SET head_hash = 'forged' WHERE project_id = 'proj_a'`, nil}}
		},
		"chain head removed": func(map[string]Memory) []sqlStep {
			return []sqlStep{{`DELETE FROM memory_chain_heads WHERE project_id = 'proj_a'`, nil}}
		},
		"chain reordered": func(map[string]Memory) []sqlStep {
			return []sqlStep{
				{`UPDATE memory_revisions SET project_sequence = 1000000 WHERE project_id = 'proj_a' AND project_sequence = 2`, nil},
				{`UPDATE memory_revisions SET project_sequence = 2 WHERE project_id = 'proj_a' AND project_sequence = 3`, nil},
				{`UPDATE memory_revisions SET project_sequence = 3 WHERE project_id = 'proj_a' AND project_sequence = 1000000`, nil},
			}
		},
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			store := newTestStore(t)
			fixture := buildVerifiedProject(t, store)
			mustVerify(t, store, "proj_a")

			dropAllTriggers(t, store.db)
			execSteps(t, store.db, tamper(fixture)...)
			if err := store.Verify(context.Background(), "proj_a"); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("Verify() error = %v, want ErrIntegrity", err)
			}
			mustVerify(t, store, "proj_b")
		})
	}
}

func TestMigrateV1AddsChainAndKeepsHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	original := openTestStore(t, path)
	fixture := buildVerifiedProject(t, original)
	before := allHistories(t, original)
	if err := original.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	downgradeToV1(t, path)
	db := rawDB(t, path)
	if schemaVersion(t, db) != 1 {
		t.Fatal("test setup: store is not schema v1")
	}
	if _, err := db.Exec(`SELECT project_sequence FROM memory_revisions`); err == nil {
		t.Fatal("test setup: v1 revisions still have chain columns")
	}

	migrated := openTestStore(t, path)
	if schemaVersion(t, db) != storeSchemaVersion {
		t.Fatalf("schema version after migration = %d, want %d", schemaVersion(t, db), storeSchemaVersion)
	}
	after := allHistories(t, migrated)
	if len(after) != len(before) {
		t.Fatalf("migrated %d memories, want %d", len(after), len(before))
	}
	for id, history := range before {
		if after[id] != history {
			t.Fatalf("memory %s history changed by migration:\n got %s\nwant %s", id, after[id], history)
		}
	}
	mustVerify(t, migrated, "proj_a")
	mustVerify(t, migrated, "proj_b")

	// The migrated store keeps appending to the same chain and guards it.
	if _, err := migrated.Confirm(context.Background(), "proj_a", fixture["a"].MemoryID, []string{"event:after"}); err != nil {
		t.Fatalf("Confirm() after migration error = %v", err)
	}
	mustVerify(t, migrated, "proj_a")
	if _, err := migrated.db.Exec(`DELETE FROM memory_chain_heads`); err == nil {
		t.Fatal("chain triggers missing after migration")
	}

	// Reopening a migrated store is a no-op.
	if err := migrated.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened := openTestStore(t, path)
	mustVerify(t, reopened, "proj_a")
}

func TestMigrateV1RefusesInconsistentHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	original := openTestStore(t, path)
	fixture := buildVerifiedProject(t, original)
	original.Close()
	downgradeToV1(t, path)

	db := rawDB(t, path)
	execSteps(t, db,
		sqlStep{statement: `DROP TRIGGER memories_update_guard`},
		sqlStep{statement: `UPDATE memories SET claim = 'forged' WHERE memory_id = ?`, args: []any{fixture["a"].MemoryID}},
	)

	if _, err := Open(path); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Open() of inconsistent v1 store error = %v, want ErrIntegrity", err)
	}
	if schemaVersion(t, db) != 1 {
		t.Fatal("failed migration changed the schema version")
	}
	if _, err := db.Exec(`SELECT action FROM memory_revisions LIMIT 1`); err != nil {
		t.Fatalf("failed migration lost v1 revisions: %v", err)
	}
	if _, err := db.Exec(`SELECT project_sequence FROM memory_revisions`); err == nil {
		t.Fatal("failed migration left chain columns behind")
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	store := openTestStore(t, path)
	store.Close()
	execSteps(t, rawDB(t, path), sqlStep{statement: `UPDATE memory_schema SET version = ?`, args: []any{storeSchemaVersion + 1}})

	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "unsupported schema version") {
		t.Fatalf("Open() of newer schema error = %v", err)
	}
}
