package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-contract-runtime/acr/internal/bootstrap"
	"github.com/agent-contract-runtime/acr/internal/coverage"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/protocol"
)

func TestRunEvaluateReturnsRewriteJSON(t *testing.T) {
	directory := t.TempDir()
	contractPath := filepath.Join(directory, "rewrite.json")
	contractJSON := `{
  "apiVersion":"acr.dev/v1alpha1",
  "kind":"Contract",
  "metadata":{"id":"rewrite","version":1,"title":"rewrite","status":"active","severity":"high","priority":900},
  "scope":{"agent_kinds":["codex"],"tool_names":["write_file"]},
  "applicability":{"all":[{"field":"input.path","operator":"glob","value":"generated/*"}]},
  "behavior":{"instruction":"write source","enforcement":"hard"},
  "correction":{"mode":"rewrite","transform":{"type":"set_fields","values":{"input.path":"src/schema.go"}}},
  "failure_mode":"closed"
}`
	if err := os.WriteFile(contractPath, []byte(contractJSON), 0o600); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	actionJSON := `{
  "schema_version":"acr.action/v1alpha1",
  "agent":{"kind":"codex","native_session_id":"session-1"},
  "workspace":{"cwd":"/workspace/project","project_id":"project-1"},
  "tool":{"name":"write_file","input":{"path":"generated/schema.go"}},
  "operation_kind":"tool.call"
}`

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"evaluate", "--contracts", contractPath}, strings.NewReader(actionJSON), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"kind": "rewrite"`) || !strings.Contains(stdout.String(), `"path": "src/schema.go"`) {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestRunCodexHookReturnsNativeRewrite(t *testing.T) {
	directory := t.TempDir()
	contractPath := writeTestContract(t, directory)
	hookInput := `{
  "session_id":"session-1",
  "turn_id":"turn-1",
  "cwd":"/workspace/project",
  "hook_event_name":"PreToolUse",
  "tool_name":"write_file",
  "tool_use_id":"call-1",
  "tool_input":{"path":"generated/schema.go"}
}`

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"hook", "codex", "pre-tool-use", "--contracts", contractPath}, strings.NewReader(hookInput), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"permissionDecision":"allow"`) || !strings.Contains(stdout.String(), `"updatedInput":{"path":"src/schema.go"}`) {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestRunHookEvaluationFailureReturnsValidDenial(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"hook", "claude", "pre-tool-use", "--contracts", "/missing/contracts"}, strings.NewReader(`{}`), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want valid fail-closed response; stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"permissionDecision":"deny"`) || !strings.Contains(stdout.String(), "could not load contracts") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestRunHookWithEmptyContractDirectoryPasses(t *testing.T) {
	directory := t.TempDir()
	hookInput := `{"session_id":"session-1","cwd":"/workspace/project","hook_event_name":"PreToolUse","tool_name":"read_file","tool_input":{"path":"README.md"}}`
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"hook", "codex", "pre-tool-use", "--contracts", directory}, strings.NewReader(hookInput), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"hookEventName":"PreToolUse"`) || strings.Contains(stdout.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestHookFileContainsQuotedInstalledCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	data := `{"hooks":{"PreToolUse":[{"hooks":[{"command":"'/tmp/acr' 'hook' 'codex' 'pre-tool-use'"}]}]}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if !hookFileContains(path, "codex") {
		t.Fatal("hookFileContains() did not detect installed command")
	}
}

func TestRunBootstrapSourcesReturnsSourceInventory(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "AGENTS.md"), []byte("# Rules\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"bootstrap", "sources", "--root", directory}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"path": "AGENTS.md"`) || !strings.Contains(stdout.String(), `"status": "indexed"`) {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestRefreshBootstrapSourcesRecordsDiffBeforeAdvancingSnapshot(t *testing.T) {
	directory := t.TempDir()
	state := filepath.Join(directory, ".acr", "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "AGENTS.md"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous, err := bootstrap.Discover(directory, bootstrap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	snapshotPath := filepath.Join(state, "bootstrap-sources.json")
	if err := bootstrap.WriteSnapshot(snapshotPath, previous); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "AGENTS.md"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := eventstore.Open(filepath.Join(state, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := refreshBootstrapSources(context.Background(), store, directory, "project-1", "workspace-1", protocol.AgentCodex); err != nil {
		t.Fatalf("refreshBootstrapSources() error = %v", err)
	}
	events, err := store.List(context.Background(), "project-1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "source.snapshot_changed" || !strings.Contains(string(events[0].Payload), `"AGENTS.md"`) {
		t.Fatalf("events = %#v", events)
	}
	current, err := bootstrap.ReadSnapshot(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	if current.Sources[0].SHA256 == previous.Sources[0].SHA256 {
		t.Fatal("snapshot was not advanced")
	}
	if err := refreshBootstrapSources(context.Background(), store, directory, "project-1", "workspace-1", protocol.AgentCodex); err != nil {
		t.Fatal(err)
	}
	events, err = store.List(context.Background(), "project-1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("unchanged refresh added events: %#v", events)
	}
}

func TestReconcileWorkspaceCoverageRecordsUnexplainedChange(t *testing.T) {
	directory := t.TempDir()
	state := filepath.Join(directory, ".acr", "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(directory, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	baseline, err := coverage.Scan(directory, 0, coverage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := coverage.Write(filepath.Join(state, "workspace-snapshot.json"), baseline); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tracked, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := eventstore.Open(filepath.Join(state, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := reconcileWorkspaceCoverage(context.Background(), store, directory, "project-1", "workspace-1", protocol.AgentCodex); err != nil {
		t.Fatal(err)
	}
	events, err := store.List(context.Background(), "project-1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "coverage.gap_detected" || !strings.Contains(string(events[0].Payload), "tracked.txt") {
		t.Fatalf("events = %#v", events)
	}
}

func TestReconcileWorkspaceCoverageAcceptsRecordedToolOutcome(t *testing.T) {
	directory := t.TempDir()
	state := filepath.Join(directory, ".acr", "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(directory, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	baseline, err := coverage.Scan(directory, 0, coverage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := coverage.Write(filepath.Join(state, "workspace-snapshot.json"), baseline); err != nil {
		t.Fatal(err)
	}
	store, err := eventstore.Open(filepath.Join(state, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Append(context.Background(), eventstore.AppendRequest{ProjectID: "project-1", WorkspaceID: "workspace-1", OccurredAt: time.Now(), Type: "action.completed", Actor: eventstore.Actor{Kind: "agent"}, Payload: json.RawMessage(`{"tool_name":"Write","resources":["` + tracked + `"],"tool_input":{"file_path":"` + tracked + `"}}`)}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tracked, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reconcileWorkspaceCoverage(context.Background(), store, directory, "project-1", "workspace-1", protocol.AgentCodex); err != nil {
		t.Fatal(err)
	}
	events, err := store.List(context.Background(), "project-1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "action.completed" {
		t.Fatalf("reconciliation created a false gap: %#v", events)
	}
}

func TestReconcileWorkspaceCoverageReportsUnattributedChangeAsSuspected(t *testing.T) {
	directory := t.TempDir()
	state := filepath.Join(directory, ".acr", "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(directory, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	baseline, err := coverage.Scan(directory, 0, coverage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := coverage.Write(filepath.Join(state, "workspace-snapshot.json"), baseline); err != nil {
		t.Fatal(err)
	}
	store, err := eventstore.Open(filepath.Join(state, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Append(context.Background(), eventstore.AppendRequest{ProjectID: "project-1", WorkspaceID: "workspace-1", OccurredAt: time.Now(), Type: "action.completed", Actor: eventstore.Actor{Kind: "agent"}, Payload: json.RawMessage(`{"tool_input":{"path":"other.txt"}}`)}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tracked, []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reconcileWorkspaceCoverage(context.Background(), store, directory, "project-1", "workspace-1", protocol.AgentCodex); err != nil {
		t.Fatal(err)
	}
	events, err := store.List(context.Background(), "project-1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Type != "coverage.gap_detected" || !strings.Contains(string(events[1].Payload), `"confidence":"suspected"`) {
		t.Fatalf("events = %#v", events)
	}
}

func writeTestContract(t *testing.T, directory string) string {
	t.Helper()
	contractPath := filepath.Join(directory, "rewrite.json")
	contractJSON := `{
  "apiVersion":"acr.dev/v1alpha1",
  "kind":"Contract",
  "metadata":{"id":"rewrite","version":1,"title":"rewrite","status":"active","severity":"high","priority":900},
  "scope":{"agent_kinds":["codex","claude-code"],"tool_names":["write_file"]},
  "applicability":{"all":[{"field":"input.path","operator":"glob","value":"generated/*"}]},
  "behavior":{"instruction":"write source","enforcement":"hard"},
  "correction":{"mode":"rewrite","transform":{"type":"set_fields","values":{"input.path":"src/schema.go"}}},
  "failure_mode":"closed"
}`
	if err := os.WriteFile(contractPath, []byte(contractJSON), 0o600); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	return contractPath
}
