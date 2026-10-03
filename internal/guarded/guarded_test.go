package guarded

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/protocol"
	"github.com/agent-contract-runtime/acr/internal/repair"
)

type fixture struct {
	root     string
	executor Executor
	store    *eventstore.Store
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{"src", "contracts", ".acr", "plans"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	state := t.TempDir()
	store, err := eventstore.Open(filepath.Join(state, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &fixture{root: root, store: store, executor: Executor{
		Root: root, ProjectID: "project-1", Agent: protocol.AgentClaudeCode, SessionID: "guarded-test",
		ContractsPath: filepath.Join(root, "contracts"), Events: store, LockDir: filepath.Join(state, "locks"),
		Commands: repair.CommandExecutor{},
	}}
}

func (f *fixture) contract(t *testing.T, id string, body map[string]any) {
	t.Helper()
	value := map[string]any{
		"apiVersion": "acr.dev/v1alpha1", "kind": "Contract",
		"metadata":     map[string]any{"id": id, "version": 1, "title": id, "status": "active", "severity": "high", "priority": 900},
		"behavior":     map[string]any{"instruction": id, "enforcement": "hard"},
		"correction":   map[string]any{"mode": "deny"},
		"failure_mode": "closed",
	}
	for key, item := range body {
		value[key] = item
	}
	encoded, _ := json.Marshal(value)
	if err := os.WriteFile(filepath.Join(f.root, "contracts", id+".json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) call(t *testing.T, tool string, arguments map[string]any) Result {
	t.Helper()
	encoded, _ := json.Marshal(arguments)
	return f.executor.Call(context.Background(), tool, encoded)
}

func (f *fixture) exists(name string) bool {
	_, err := os.Stat(filepath.Join(f.root, name))
	return err == nil
}

func (f *fixture) eventTypes(t *testing.T) []string {
	t.Helper()
	events, err := f.store.List(context.Background(), "project-1", 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	types := []string{}
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func when(field, operator, value string) map[string]any {
	return map[string]any{"all": []map[string]any{{"field": field, "operator": operator, "value": value}}}
}

func TestExecRunsAllowedCommandsAndRecordsThem(t *testing.T) {
	f := newFixture(t)
	result := f.call(t, ToolExec, map[string]any{"command": "echo hello > greeting.txt && cat greeting.txt", "cwd": "src"})
	if result.IsError || !strings.Contains(result.Text, "exit code 0") || !strings.Contains(result.Text, "hello") {
		t.Fatalf("result = %+v", result)
	}
	if !f.exists("src/greeting.txt") {
		t.Fatal("command did not run in the requested cwd")
	}
	if types := strings.Join(f.eventTypes(t), ","); types != "action.proposed,action.passed,action.completed" {
		t.Fatalf("events = %s", types)
	}
	events, _ := f.store.List(context.Background(), "project-1", 2, 1)
	if !strings.Contains(string(events[0].Payload), `"observed_changes":["src/greeting.txt"]`) {
		t.Fatalf("exec outcome does not record the file it created: %s", events[0].Payload)
	}
	failed := f.call(t, ToolExec, map[string]any{"command": "exit 7"})
	if !failed.IsError || !strings.Contains(failed.Text, "exit code 7") {
		t.Fatalf("failing command result = %+v", failed)
	}
	if !strings.HasSuffix(strings.Join(f.eventTypes(t), ","), "action.failed") {
		t.Fatal("failing command was not recorded as action.failed")
	}
}

func TestDeniedAndUnconfirmableCallsNeverRun(t *testing.T) {
	f := newFixture(t)
	f.contract(t, "no-force-push", map[string]any{"scope": map[string]any{"tool_names": []string{"Bash"}}, "applicability": when("input.command", "contains", "--force")})
	f.contract(t, "ask-before-migrate", map[string]any{"scope": map[string]any{"tool_names": []string{"Bash"}},
		"applicability": when("input.command", "contains", "migrate"), "correction": map[string]any{"mode": "ask"}})

	denied := f.call(t, ToolExec, map[string]any{"command": "touch pushed.txt # --force"})
	if !denied.IsError || !strings.Contains(denied.Text, "ACR denied the call: no-force-push") || f.exists("pushed.txt") {
		t.Fatalf("denied result = %+v", denied)
	}
	asked := f.call(t, ToolExec, map[string]any{"command": "touch migrated.txt # migrate"})
	if !asked.IsError || !strings.Contains(asked.Text, "human confirmation") || f.exists("migrated.txt") {
		t.Fatalf("ask result = %+v", asked)
	}

	// Contracts that cannot be loaded fail closed.
	if err := os.WriteFile(filepath.Join(f.root, "contracts", "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := f.call(t, ToolExec, map[string]any{"command": "touch ran.txt"})
	if !broken.IsError || !strings.Contains(broken.Text, "did not run") || f.exists("ran.txt") {
		t.Fatalf("broken contracts result = %+v", broken)
	}
}

func TestRewriteIsRevalidatedBeforeWriting(t *testing.T) {
	f := newFixture(t)
	f.contract(t, "source-not-generated", map[string]any{
		"scope": map[string]any{"tool_names": []string{"Write"}}, "applicability": when("input.file_path", "contains", "/generated/"),
		"correction": map[string]any{"mode": "rewrite", "transform": map[string]any{"type": "set_fields",
			"values": map[string]any{"input.file_path": filepath.Join(f.root, "src", "schema.go")}}},
	})
	result := f.call(t, ToolWriteFile, map[string]any{"file_path": "generated/schema.go", "content": "package schema\n"})
	if result.IsError || !strings.Contains(result.Text, "corrected the call") || !f.exists("src/schema.go") || f.exists("generated/schema.go") {
		t.Fatalf("rewrite result = %+v", result)
	}

	f.contract(t, "source-not-generated", map[string]any{
		"scope": map[string]any{"tool_names": []string{"Write"}}, "applicability": when("input.file_path", "contains", "/generated/"),
		"correction": map[string]any{"mode": "rewrite", "transform": map[string]any{"type": "set_fields",
			"values": map[string]any{"input.file_path": filepath.Join(filepath.Dir(f.root), "escaped.go")}}},
	})
	escaped := f.call(t, ToolWriteFile, map[string]any{"file_path": "generated/other.go", "content": "x"})
	if !escaped.IsError || !strings.Contains(escaped.Text, "correction of the call is not allowed") {
		t.Fatalf("escaping rewrite result = %+v", escaped)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.root), "escaped.go")); !os.IsNotExist(err) {
		t.Fatal("a rewrite wrote outside the project root")
	}
}

func TestFileToolsStayInsideTheProject(t *testing.T) {
	f := newFixture(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(f.root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "target.txt"), filepath.Join(f.root, "src", "link.txt")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../escape.txt", filepath.Join(outside, "abs.txt"), "linked/via-symlink.txt", "src/link.txt", ".acr/contracts/forged.json", ""} {
		result := f.call(t, ToolWriteFile, map[string]any{"file_path": path, "content": "x"})
		if !result.IsError {
			t.Fatalf("write to %q succeeded: %+v", path, result)
		}
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatalf("files were written outside the project: %v", entries)
	}
	if unknown := f.call(t, ToolWriteFile, map[string]any{"file_path": "a.txt", "content": "x", "mode": "0777"}); !unknown.IsError {
		t.Fatal("unknown argument was accepted")
	}
}

func TestEditFileReplacesExactlyWhatWasAsked(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.root, "src", "config.txt")
	if err := os.WriteFile(path, []byte("port=8080\nhost=a\nhost=b\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if result := f.call(t, ToolEditFile, map[string]any{"file_path": "src/config.txt", "old_string": "port=8080", "new_string": "port=9090"}); result.IsError {
		t.Fatalf("edit result = %+v", result)
	}
	ambiguous := f.call(t, ToolEditFile, map[string]any{"file_path": "src/config.txt", "old_string": "host=", "new_string": "server="})
	if !ambiguous.IsError || !strings.Contains(ambiguous.Text, "occurs 2 times") {
		t.Fatalf("ambiguous edit = %+v", ambiguous)
	}
	if result := f.call(t, ToolEditFile, map[string]any{"file_path": "src/config.txt", "old_string": "host=", "new_string": "server=", "replace_all": true}); result.IsError {
		t.Fatalf("replace_all edit = %+v", result)
	}
	data, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(data) != "port=9090\nserver=a\nserver=b\n" || info.Mode().Perm() != 0o640 {
		t.Fatalf("file = %q mode %v", data, info.Mode().Perm())
	}
}

// The guarded prerequisite loop: a deploy missing test evidence makes ACR run
// the declared repeatable plan itself, and the original deploy then runs.
func TestMissingEvidenceIsProducedBeforeTheOriginalCall(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, "src", "app.go"), []byte("package app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writePlan := func(name string, repeatable bool) {
		plan, _ := json.Marshal(map[string]any{
			"schema_version": "acr.repair-plan/v1alpha1", "id": name, "idempotency_key": name + "-v1", "resource_keys": []string{name},
			"repeatable": repeatable,
			"steps": []map[string]any{{"id": "unit", "argv": []string{"/bin/sh", "-c", "echo ran >> " + name + ".log"}, "timeout_seconds": 10,
				"verification": map[string]any{"inputs": []string{"src/**"}}}},
		})
		if err := os.WriteFile(filepath.Join(f.root, "plans", name+".json"), plan, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writePlan("checks", true)
	f.contract(t, "tests-before-deploy", map[string]any{
		"scope": map[string]any{"tool_names": []string{"Bash"}}, "applicability": when("input.command", "contains", "deploy.sh"),
		"correction": map[string]any{"mode": "none"},
		"verifiers": map[string]any{"evidence": []map[string]string{{"subject": "repair:checks/unit", "plan": "plans/checks.json"}}},
	})

	result := f.call(t, ToolExec, map[string]any{"command": "echo deploy.sh >> deployed.log"})
	if result.IsError || !strings.Contains(result.Text, "ACR ran prerequisite plan plans/checks.json") {
		t.Fatalf("deploy result = %+v", result)
	}
	if !f.exists("checks.log") || !f.exists("deployed.log") {
		t.Fatal("the plan or the original deploy did not run")
	}
	types := strings.Join(f.eventTypes(t), ",")
	if !strings.Contains(types, "action.denied,repair.started") || !strings.HasSuffix(types, "action.passed,action.completed") {
		t.Fatalf("events = %s", types)
	}

	// With fresh evidence the deploy runs without re-running the plan.
	f.call(t, ToolExec, map[string]any{"command": "echo deploy.sh >> deployed.log"})
	if data, _ := os.ReadFile(filepath.Join(f.root, "checks.log")); strings.Count(string(data), "ran") != 1 {
		t.Fatalf("plan ran again with valid evidence: %q", data)
	}

	// Plans with side effects are never run automatically.
	writePlan("risky", false)
	f.contract(t, "tests-before-deploy", map[string]any{
		"scope": map[string]any{"tool_names": []string{"Bash"}}, "applicability": when("input.command", "contains", "release.sh"),
		"correction": map[string]any{"mode": "none"},
		"verifiers": map[string]any{"evidence": []map[string]string{{"subject": "repair:risky/unit", "plan": "plans/risky.json"}}},
	})
	refused := f.call(t, ToolExec, map[string]any{"command": "echo release.sh >> released.log"})
	if !refused.IsError || !strings.Contains(refused.Text, "not a repeatable verification plan") || f.exists("risky.log") || f.exists("released.log") {
		t.Fatalf("non-repeatable plan result = %+v", refused)
	}
}

func TestServeSpeaksMCP(t *testing.T) {
	f := newFixture(t)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"exec","arguments":{"command":"echo from-mcp"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"delete_everything","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"resources/list"}`,
		`not json`,
		`{"jsonrpc":"2.0","id":6,"method":"ping"}`,
	}, "\n") + "\n"
	var output strings.Builder
	if err := Serve(context.Background(), strings.NewReader(input), &output, f.executor, "test"); err != nil {
		t.Fatal(err)
	}
	var responses []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(output.String()))
	for scanner.Scan() {
		var response map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			t.Fatalf("response %q: %v", scanner.Text(), err)
		}
		responses = append(responses, response)
	}
	if len(responses) != 7 {
		t.Fatalf("got %d responses, want 7 (no reply to the notification): %s", len(responses), output.String())
	}
	initialize := responses[0]["result"].(map[string]any)
	if initialize["protocolVersion"] != "2025-03-26" {
		t.Fatalf("initialize = %v", initialize)
	}
	if tools := responses[1]["result"].(map[string]any)["tools"].([]any); len(tools) != 3 {
		t.Fatalf("tools = %v", tools)
	}
	call := responses[2]["result"].(map[string]any)
	if call["isError"] != false || !strings.Contains(call["content"].([]any)[0].(map[string]any)["text"].(string), "from-mcp") {
		t.Fatalf("exec call = %v", call)
	}
	if unknown := responses[3]["result"].(map[string]any); unknown["isError"] != true {
		t.Fatalf("unknown tool = %v", unknown)
	}
	if responses[4]["error"].(map[string]any)["code"].(float64) != -32601 || responses[5]["error"].(map[string]any)["code"].(float64) != -32700 {
		t.Fatalf("error responses = %v / %v", responses[4], responses[5])
	}
}
