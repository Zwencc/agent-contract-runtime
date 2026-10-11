package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/project"
)

// Scenario A of the development plan: one contract, written against project
// paths, corrects Claude Code's Write, Codex's apply_patch and a guarded
// write the same way, and all three decisions land in one event chain.
func TestScenarioOneContractCorrectsBothAgents(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := os.ReadFile("../../examples/contracts/shared-generated-to-src.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.Contracts, "shared-generated-to-src.json"), shared, 0o600); err != nil {
		t.Fatal(err)
	}
	preToolUse := func(agent, cwd, tool string, input any) string {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"session_id": agent + "-session", "cwd": cwd, "hook_event_name": "PreToolUse",
			"tool_name": tool, "tool_use_id": agent + "-call", "tool_input": input})
		stdout.Reset()
		if code := run([]string{"hook", agent, "pre-tool-use", "--contracts", paths.Contracts, "--event-db", paths.EventDB, "--project-id", config.ProjectID},
			bytes.NewReader(raw), &stdout, &stderr); code != 0 {
			t.Fatalf("%s hook code = %d, stderr = %s", agent, code, stderr.String())
		}
		return stdout.String()
	}

	// Claude Code runs from a subdirectory; paths stay relative to the project.
	claude := preToolUse("claude", filepath.Join(root, "src"), "Write", map[string]string{"file_path": filepath.Join(root, "generated", "a.go"), "content": "package a\n"})
	if !strings.Contains(claude, `"permissionDecision":"allow"`) || !strings.Contains(claude, `"file_path":"`+filepath.Join(root, "src", "a.go")+`"`) {
		t.Fatalf("claude hook = %s", claude)
	}
	codex := preToolUse("codex", root, "apply_patch", map[string]string{"command": "*** Begin Patch\n*** Add File: generated/b.go\n+package b\n*** End Patch\n"})
	if !strings.Contains(codex, `"permissionDecision":"allow"`) || !strings.Contains(codex, `*** Add File: src/b.go`) || strings.Contains(codex, "generated/b.go") {
		t.Fatalf("codex hook = %s", codex)
	}

	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write_file","arguments":{"file_path":"generated/c.go","content":"package c\n"}}}` + "\n"
	stdout.Reset()
	if code := run([]string{"mcp", "serve", "--root", root, "--agent", "codex"}, strings.NewReader(call), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"isError":false`) {
		t.Fatalf("guarded write code = %d, stdout = %s, stderr = %s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "src", "c.go")); err != nil {
		t.Fatalf("guarded write did not land in src/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "generated", "c.go")); !os.IsNotExist(err) {
		t.Fatalf("guarded write reached generated/: %v", err)
	}

	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Verify(context.Background(), config.ProjectID); err != nil {
		t.Fatal(err)
	}
	events, err := store.List(context.Background(), config.ProjectID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	corrected := map[string]int{}
	for _, event := range events {
		if event.Actor.Kind == "runtime" && strings.Contains(string(event.Payload), `"kind":"rewrite"`) && strings.Contains(string(event.Payload), "shared-generated-to-src") {
			corrected[string(event.Actor.AgentKind)]++
		}
	}
	if corrected["claude-code"] != 1 || corrected["codex"] != 2 {
		t.Fatalf("rewrite decisions by agent = %v", corrected)
	}
}

// exampleProject is an initialized project with every example contract active.
func exampleProject(t *testing.T) (string, project.Config, project.Paths) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("../../examples/contracts/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("example contracts: %v, %v", files, err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(paths.Contracts, filepath.Base(file)), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root, config, paths
}

// hookDecision runs a PreToolUse hook and returns the host-visible decision,
// its reason and the rewritten input, if any.
func hookDecision(t *testing.T, agent string, root string, config project.Config, paths project.Paths, tool string, input any, mode string) (string, string, string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"session_id": agent + "-session", "cwd": root, "hook_event_name": "PreToolUse",
		"tool_name": tool, "tool_use_id": agent + "-call", "tool_input": input, "permission_mode": mode})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"hook", agent, "pre-tool-use", "--contracts", paths.Contracts, "--event-db", paths.EventDB, "--project-id", config.ProjectID},
		bytes.NewReader(raw), &stdout, &stderr); code != 0 {
		t.Fatalf("%s hook code = %d, stderr = %s", agent, code, stderr.String())
	}
	var output struct {
		HookSpecificOutput struct {
			PermissionDecision       string          `json:"permissionDecision"`
			PermissionDecisionReason string          `json:"permissionDecisionReason"`
			UpdatedInput             json.RawMessage `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("%s hook output %q: %v", agent, stdout.String(), err)
	}
	return output.HookSpecificOutput.PermissionDecision, output.HookSpecificOutput.PermissionDecisionReason, string(output.HookSpecificOutput.UpdatedInput)
}

// Scenario D of the development plan: a production write with no configured
// alternative is never rewritten to a guessed target. A person is asked when
// one can answer; otherwise the call is denied with what is missing.
func TestScenarioUnsafeCorrectionAsksOrDenies(t *testing.T) {
	root, config, paths := exampleProject(t)
	command := map[string]string{"command": `touch ran.txt; psql "$PROD_DATABASE_URL" -c "delete from sessions"`}

	decision, reason, updated := hookDecision(t, "claude", root, config, paths, "Bash", command, "default")
	if decision != "ask" || updated != "" || !strings.Contains(reason, "STAGING_DATABASE_URL") {
		t.Fatalf("interactive claude = %s %q %s", decision, reason, updated)
	}
	for _, mode := range []string{"bypassPermissions", "dontAsk"} {
		decision, reason, updated = hookDecision(t, "claude", root, config, paths, "Bash", command, mode)
		if decision != "deny" || updated != "" || !strings.Contains(reason, "nobody answers prompts") || !strings.Contains(reason, "STAGING_DATABASE_URL") {
			t.Fatalf("unattended claude (%s) = %s %q %s", mode, decision, reason, updated)
		}
	}
	// acr launch marks claude -p and codex exec as unattended for the hooks.
	t.Setenv("ACR_UNATTENDED", "1")
	decision, reason, _ = hookDecision(t, "claude", root, config, paths, "Bash", command, "default")
	if decision != "deny" || !strings.Contains(reason, "runs unattended") {
		t.Fatalf("claude launched unattended = %s %q", decision, reason)
	}
	t.Setenv("ACR_UNATTENDED", "")
	decision, reason, _ = hookDecision(t, "codex", root, config, paths, "Bash", command, "default")
	if decision != "deny" || !strings.Contains(reason, "STAGING_DATABASE_URL") {
		t.Fatalf("codex = %s %q", decision, reason)
	}

	call, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "exec", "arguments": command}})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"mcp", "serve", "--root", root, "--agent", "claude"}, bytes.NewReader(append(call, '\n')), &stdout, &stderr); code != 0 ||
		!strings.Contains(stdout.String(), `"isError":true`) || !strings.Contains(stdout.String(), "human confirmation") {
		t.Fatalf("guarded exec code = %d, stdout = %s, stderr = %s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "ran.txt")); !os.IsNotExist(err) {
		t.Fatalf("the refused command ran: %v", err)
	}
}

// Every example contract does what its title says, for both agents.
func TestExampleContractsHoldForBothAgents(t *testing.T) {
	root, config, paths := exampleProject(t)
	cases := []struct {
		name, agent, tool string
		input             any
		decision, reason  string
	}{
		{"claude writes .env", "claude", "Write", map[string]string{"file_path": filepath.Join(root, ".env"), "content": "KEY=1"}, "deny", "Agent 不得修改"},
		{"codex patches nested .env", "codex", "apply_patch", map[string]string{"command": "*** Begin Patch\n*** Update File: config/.env.local\n@@\n-a\n+b\n*** End Patch\n"}, "deny", "Agent 不得修改"},
		{"claude force-pushes", "claude", "Bash", map[string]string{"command": "git push --force origin main"}, "deny", "不得强制推送"},
		{"codex force-pushes", "codex", "Bash", map[string]string{"command": "git push --force origin main"}, "deny", "不得强制推送"},
		{"claude deploys untested", "claude", "Bash", map[string]string{"command": "./deploy.sh"}, "deny", "acr repair run --plan .acr/plans/checks.json"},
		{"codex deploys untested", "codex", "Bash", map[string]string{"command": "./deploy.sh"}, "deny", "acr repair run --plan .acr/plans/checks.json"},
		{"claude writes a contract", "claude", "Write", map[string]string{"file_path": filepath.Join(root, ".acr", "contracts", "x.json"), "content": "{}"}, "deny", "Do not write .acr/ directly"},
		{"codex adds a repair plan", "codex", "apply_patch", map[string]string{"command": "*** Begin Patch\n*** Add File: .acr/plans/p.json\n+{}\n*** End Patch\n"}, "deny", "Do not write .acr/ directly"},
		{"claude activates a rule itself", "claude", "Bash", map[string]string{"command": "acr contract activate /tmp/x.json --apply"}, "deny", "for the person at the terminal"},
		{"codex retires a rule itself", "codex", "Bash", map[string]string{"command": "acr contract retire deny-force-push --reason x --apply"}, "deny", "for the person at the terminal"},
		{"claude edits source", "claude", "Write", map[string]string{"file_path": filepath.Join(root, "src", "a.go"), "content": "x"}, "", ""},
		{"codex runs tests", "codex", "Bash", map[string]string{"command": "go test ./..."}, "", ""},
	}
	for _, item := range cases {
		decision, reason, _ := hookDecision(t, item.agent, root, config, paths, item.tool, item.input, "default")
		if decision != item.decision || !strings.Contains(reason, item.reason) {
			t.Errorf("%s: %s %q, want %s containing %q", item.name, decision, reason, item.decision, item.reason)
		}
	}
}
