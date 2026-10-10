package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/coverage"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/project"
	"github.com/agent-contract-runtime/acr/internal/protocol"
)

// Only structured write evidence explains a change: a path merely appearing
// in a command, a read, or command output does not.
func TestExplainChangesAcceptsOnlyWriteEvidence(t *testing.T) {
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	changes := []coverage.Change{{Path: "src/app.go"}, {Path: "deployed.txt"}, {Path: "tested.log"}, {Path: "notes.md"}}
	cases := map[string]struct {
		eventType string
		payload   string
		want      []string
	}{
		"write tool with absolute resource": {"action.completed", `{"tool_name":"Write","resources":["` + filepath.Join(root, "src", "app.go") + `"]}`, []string{"src/app.go"}},
		"edit tool with relative resource":  {"action.failed", `{"tool_name":"Edit","resources":["src/app.go"]}`, []string{"src/app.go"}},
		"apply_patch markers":               {"action.completed", `{"tool_name":"apply_patch","resources":["notes.md"]}`, []string{"notes.md"}},
		"shell command naming a path":       {"action.completed", `{"tool_name":"Bash","resources":[],"tool_input":{"command":"cat deployed.txt"}}`, nil},
		"read naming a path":                {"action.completed", `{"tool_name":"Read","resources":["` + filepath.Join(root, "notes.md") + `"]}`, nil},
		"legacy event without tool name":    {"action.completed", `{"tool_input":{"path":"notes.md"}}`, nil},
		"guarded exec observed changes":     {"action.completed", `{"tool_name":"Bash","observed_changes":["deployed.txt"],"tool_input":{"command":"./deploy.sh"}}`, []string{"deployed.txt"}},
		"repair argv":                       {"repair.step_completed", `{"argv":["/bin/sh","-c","echo tested >> tested.log"],"outcome":{"stdout":"src/app.go"}}`, []string{"tested.log"}},
		"repair output naming a path":       {"repair.failed", `{"argv":["make","check"],"outcome":{"stdout":"wrote notes.md"}}`, nil},
		"write outside the project":         {"action.completed", `{"tool_name":"Write","resources":["/etc/src/app.go"]}`, nil},
	}
	for name, test := range cases {
		matched := map[string]bool{}
		explainChanges(eventstore.Event{Type: test.eventType, Payload: []byte(test.payload)}, resolved, changes, matched)
		if len(matched) != len(test.want) {
			t.Fatalf("%s: matched %v, want %v", name, matched, test.want)
		}
		for _, path := range test.want {
			if !matched[path] {
				t.Fatalf("%s: matched %v, want %v", name, matched, test.want)
			}
		}
	}
}

// A file created through ACR's guarded exec is explained by what ACR saw
// change; the same file created by a host shell command is not.
func TestGuardedExecChangesNeedNoManualReconciliation(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"exec","arguments":{"command":"./deploy.sh 2>/dev/null; echo deployed > deployed.txt"}}}` + "\n"
	if code := run([]string{"mcp", "serve", "--root", root}, strings.NewReader(call), &stdout, &stderr); code != 0 {
		t.Fatalf("mcp serve code = %d, stderr = %s", code, stderr.String())
	}
	native, _ := json.Marshal(map[string]any{
		"session_id": "s1", "cwd": root, "hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_use_id": "c1",
		"tool_input": map[string]string{"command": "echo built > built.txt"}, "tool_response": map[string]string{"stdout": ""},
	})
	if err := os.WriteFile(filepath.Join(root, "built.txt"), []byte("built\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"hook", "claude", "post-tool-use", "--event-db", paths.EventDB, "--project-id", config.ProjectID}, bytes.NewReader(native), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}

	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := reconcileWorkspaceCoverage(context.Background(), store, root, config.ProjectID, config.WorkspaceID, protocol.AgentClaudeCode); err != nil {
		t.Fatal(err)
	}
	events, err := store.List(context.Background(), config.ProjectID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	var gap struct {
		Confidence      string            `json:"confidence"`
		ObservedChanges []coverage.Change `json:"observed_changes"`
	}
	if last.Type != "coverage.gap_detected" || json.Unmarshal(last.Payload, &gap) != nil {
		t.Fatalf("last event = %s %s", last.Type, last.Payload)
	}
	if len(gap.ObservedChanges) != 1 || gap.ObservedChanges[0].Path != "built.txt" || gap.Confidence != "suspected" {
		t.Fatalf("gap = %+v; only the host shell's file should need review", gap)
	}
}
