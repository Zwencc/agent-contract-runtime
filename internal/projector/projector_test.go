package projector

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-contract-runtime/acr/internal/coverage"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/protocol"
	"github.com/agent-contract-runtime/acr/internal/repair"
	acrruntime "github.com/agent-contract-runtime/acr/internal/runtime"
	"github.com/agent-contract-runtime/acr/internal/verification"
)

const projectID = "project-1"

type fixture struct {
	root     string
	events   *eventstore.Store
	service  *acrruntime.Service
	projects string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	directory := t.TempDir()
	root := filepath.Join(directory, "repo")
	if err := os.MkdirAll(filepath.Join(root, "internal", "auth"), 0o700); err != nil {
		t.Fatal(err)
	}
	events, err := eventstore.Open(filepath.Join(directory, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { events.Close() })
	service, err := acrruntime.NewWithEventStore(nil, events)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{root: root, events: events, service: service, projects: filepath.Join(directory, "projections.db")}
}

func (f *fixture) tool(t *testing.T, name string, input map[string]string, failed bool, errorText string) {
	t.Helper()
	raw, _ := json.Marshal(input)
	action := protocol.NewAction(
		protocol.AgentContext{Kind: protocol.AgentClaudeCode, NativeSessionID: "s1"},
		protocol.WorkspaceContext{CWD: f.root, ProjectID: projectID},
		protocol.ToolCall{Name: name, Input: raw, CallID: "call"},
	)
	for _, field := range []string{"file_path", "path"} {
		if value := input[field]; value != "" {
			action.Resources = append(action.Resources, value)
		}
	}
	outcome := acrruntime.ToolOutcome{Action: action, Failed: failed, Error: errorText}
	if !failed {
		outcome.Response = json.RawMessage(`{"ok":true}`)
	}
	if err := f.service.RecordToolOutcome(context.Background(), outcome); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) verify(t *testing.T, subject string, patterns []string) {
	t.Helper()
	snapshot, err := coverage.Scan(f.root, 0, coverage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verification.Record(context.Background(), f.events, projectID, "", true, verification.Result{
		Subject: subject, Inputs: verification.Compute(snapshot, patterns), Stable: true,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// populate records a realistic history: edits, a repeated failure that differs
// only in numbers, a failed repair step, verifications and an invalidation.
func (f *fixture) populate(t *testing.T) {
	t.Helper()
	f.write(t, "internal/auth/login.go", "package auth\n")
	f.write(t, "internal/auth/token.go", "package auth\n")
	f.write(t, "README.md", "# repo\n")
	f.tool(t, "Write", map[string]string{"file_path": filepath.Join(f.root, "internal/auth/login.go")}, false, "")
	f.tool(t, "Edit", map[string]string{"file_path": "internal/auth/login.go"}, true, "\nlogin_test.go:12: expected 200, got 500\nmore")
	f.tool(t, "Edit", map[string]string{"file_path": "internal/auth/login.go"}, true, "login_test.go:47: expected 200, got 501")
	f.tool(t, "Write", map[string]string{"file_path": "internal/auth/token.go"}, true, "permission denied")
	f.tool(t, "Write", map[string]string{"file_path": "/etc/outside.conf"}, false, "")

	runner := repair.Runner{Store: f.events, ProjectID: projectID, Root: f.root, LockDir: filepath.Join(t.TempDir(), "locks"), Executor: repair.CommandExecutor{}}
	if _, err := runner.Run(context.Background(), repair.Plan{
		SchemaVersion: repair.SchemaVersion, ID: "migrate", IdempotencyKey: "migrate-v1", ResourceKeys: []string{"db"},
		Steps: []repair.Step{{ID: "apply", Argv: []string{"/bin/sh", "-c", "echo boom >&2; exit 3"}, TimeoutSeconds: 10}},
	}); err == nil {
		t.Fatal("failing repair step succeeded")
	}

	f.verify(t, "auth-tests", []string{"internal/auth/**"})
	f.verify(t, "docs", []string{"*.md"})
	f.write(t, "internal/auth/token.go", "package auth\n\nvar changed = true\n")
	snapshot, err := coverage.Scan(f.root, 0, coverage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := verification.Reconcile(context.Background(), f.events, projectID, "", snapshot, time.Now()); err != nil || count != 1 {
		t.Fatalf("Reconcile() = %d, %v", count, err)
	}
}

func openProjections(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func exportGraph(t *testing.T, store *Store) string {
	t.Helper()
	nodes, err := store.Nodes(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	edges, err := store.Edges(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(map[string]any{"nodes": nodes, "edges": edges})
	return string(encoded)
}

func nodeByID(t *testing.T, store *Store, id string) Node {
	t.Helper()
	nodes, err := store.Nodes(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if node.ID == id {
			return node
		}
	}
	t.Fatalf("node %s not projected", id)
	return Node{}
}

func TestSyncProjectsEventsIntoTraceableGraph(t *testing.T) {
	f := newFixture(t)
	f.populate(t)
	store := openProjections(t, f.projects)
	if _, err := store.Sync(context.Background(), f.events, projectID, f.root); err != nil {
		t.Fatal(err)
	}

	nodeByID(t, store, "artifact:internal/auth/login.go")
	nodeByID(t, store, "module:internal/auth")
	nodes, _ := store.Nodes(context.Background(), projectID)
	for _, node := range nodes {
		if strings.Contains(node.Key, "outside.conf") {
			t.Fatalf("path outside the project was projected: %+v", node)
		}
	}

	var repeated, repairFailure *FailureAttributes
	for _, node := range nodes {
		if node.Type != NodeFailure {
			continue
		}
		var attributes FailureAttributes
		if err := json.Unmarshal(node.Attributes, &attributes); err != nil {
			t.Fatal(err)
		}
		switch {
		case attributes.Tool == "Edit":
			repeated = &attributes
			if len(node.SourceEventIDs) != 2 {
				t.Fatalf("repeated failure sources = %v", node.SourceEventIDs)
			}
		case attributes.Tool == "repair:migrate/apply":
			repairFailure = &attributes
		}
	}
	if repeated == nil || repeated.Occurrences != 2 || repeated.Signature != "login_test.go:#: expected #, got #" {
		t.Fatalf("repeated failure = %+v", repeated)
	}
	if repairFailure == nil || !strings.Contains(repairFailure.Signature, "boom") {
		t.Fatalf("repair failure = %+v", repairFailure)
	}

	auth := nodeByID(t, store, "verification:auth-tests")
	var authAttributes VerificationAttributes
	json.Unmarshal(auth.Attributes, &authAttributes)
	if authAttributes.State != verification.StateInvalidated || auth.ValidUntilSequence == 0 || len(auth.SourceEventIDs) != 2 {
		t.Fatalf("auth verification = %+v %+v", auth, authAttributes)
	}
	docs := nodeByID(t, store, "verification:docs")
	if !strings.Contains(string(docs.Attributes), `"state":"valid"`) || docs.ValidUntilSequence != 0 {
		t.Fatalf("docs verification = %+v", docs)
	}

	// Every derived node traces back to recorded events.
	recorded := map[string]bool{}
	all, err := f.events.List(context.Background(), projectID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range all {
		recorded[event.EventID] = true
	}
	for _, node := range nodes {
		if len(node.SourceEventIDs) == 0 {
			t.Fatalf("node %s has no source events", node.ID)
		}
		for _, id := range node.SourceEventIDs {
			if !recorded[id] {
				t.Fatalf("node %s cites unknown event %s", node.ID, id)
			}
		}
	}
	edges, _ := store.Edges(context.Background(), projectID)
	for _, edge := range edges {
		if !recorded[edge.SourceEventID] {
			t.Fatalf("edge %+v cites unknown event", edge)
		}
	}
}

func TestDeletedProjectionRebuildsIdentically(t *testing.T) {
	f := newFixture(t)
	f.populate(t)
	first := openProjections(t, f.projects)
	if _, err := first.Sync(context.Background(), f.events, projectID, f.root); err != nil {
		t.Fatal(err)
	}
	want := exportGraph(t, first)
	first.Close()

	for _, suffix := range []string{"", "-wal", "-shm"} {
		os.Remove(f.projects + suffix)
	}
	rebuilt := openProjections(t, f.projects)
	if _, err := rebuilt.Sync(context.Background(), f.events, projectID, f.root); err != nil {
		t.Fatal(err)
	}
	if got := exportGraph(t, rebuilt); got != want {
		t.Fatalf("rebuilt graph differs:\n got %s\nwant %s", got, want)
	}
}

func TestIncrementalSyncMatchesFullRebuild(t *testing.T) {
	f := newFixture(t)
	incremental := openProjections(t, f.projects)
	f.write(t, "internal/auth/login.go", "package auth\n")
	f.tool(t, "Edit", map[string]string{"file_path": "internal/auth/login.go"}, true, "expected 200, got 500")
	if _, err := incremental.Sync(context.Background(), f.events, projectID, f.root); err != nil {
		t.Fatal(err)
	}
	f.populate(t)
	applied, err := incremental.Sync(context.Background(), f.events, projectID, f.root)
	if err != nil || applied == 0 {
		t.Fatalf("incremental Sync() = %d, %v", applied, err)
	}
	if again, err := incremental.Sync(context.Background(), f.events, projectID, f.root); err != nil || again != 0 {
		t.Fatalf("idle Sync() = %d, %v; want nothing new", again, err)
	}

	full := openProjections(t, filepath.Join(t.TempDir(), "full.db"))
	if _, err := full.Sync(context.Background(), f.events, projectID, f.root); err != nil {
		t.Fatal(err)
	}
	if exportGraph(t, incremental) != exportGraph(t, full) {
		t.Fatal("incremental projection differs from a full rebuild")
	}
}

func TestSyncRebuildsWhenEventLogIsReplaced(t *testing.T) {
	f := newFixture(t)
	f.populate(t)
	store := openProjections(t, f.projects)
	if _, err := store.Sync(context.Background(), f.events, projectID, f.root); err != nil {
		t.Fatal(err)
	}

	other := newFixture(t)
	other.write(t, "README.md", "# other\n")
	other.tool(t, "Write", map[string]string{"file_path": "README.md"}, false, "")
	for range 30 {
		other.tool(t, "Read", map[string]string{"path": "README.md"}, false, "")
	}
	if _, err := store.Sync(context.Background(), other.events, projectID, other.root); err != nil {
		t.Fatal(err)
	}
	nodes, _ := store.Nodes(context.Background(), projectID)
	for _, node := range nodes {
		if strings.Contains(node.ID, "auth") {
			t.Fatalf("projection kept state from the replaced event log: %s", node.ID)
		}
	}
}

func TestRelevantIsPathFirst(t *testing.T) {
	f := newFixture(t)
	f.populate(t)
	store := openProjections(t, f.projects)
	if _, err := store.Sync(context.Background(), f.events, projectID, f.root); err != nil {
		t.Fatal(err)
	}

	relevant, err := store.Relevant(context.Background(), projectID, []string{"internal/auth/login.go"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(relevant.Failures) != 2 || relevant.Failures[0].Match != "file" || relevant.Failures[0].Attributes.Tool != "Edit" ||
		relevant.Failures[1].Match != "module" || relevant.Failures[1].Attributes.Signature != "permission denied" {
		t.Fatalf("failures = %+v", relevant.Failures)
	}
	if len(relevant.Verifications) != 1 || relevant.Verifications[0].Attributes.Subject != "auth-tests" {
		t.Fatalf("verifications = %+v", relevant.Verifications)
	}

	docs, err := store.Relevant(context.Background(), projectID, []string{"README.md"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs.Failures) != 0 || len(docs.Verifications) != 1 || docs.Verifications[0].Attributes.Subject != "docs" {
		t.Fatalf("README relevance = %+v", docs)
	}

	recent, err := store.Relevant(context.Background(), projectID, nil, 1)
	if err != nil || len(recent.Failures) != 1 || recent.Failures[0].Match != "recent" || recent.Failures[0].Attributes.Tool != "repair:migrate/apply" {
		t.Fatalf("recent failures = %+v, %v", recent.Failures, err)
	}
	if invalid := recent.Verifications; len(invalid) != 1 || invalid[0].Attributes.State == verification.StateValid {
		t.Fatalf("non-valid verifications must come first: %+v", invalid)
	}
	if _, err := store.Relevant(context.Background(), projectID, nil, MaximumRelevant+1); err == nil {
		t.Fatal("Relevant() accepted an unbounded limit")
	}
}

func TestOpenRejectsForeignDatabase(t *testing.T) {
	f := newFixture(t)
	if _, err := Open(filepath.Join(filepath.Dir(f.projects), "events.db")); err == nil {
		t.Fatal("Open() accepted the event database")
	}
}
