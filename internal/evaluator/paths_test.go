package evaluator

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/adapter"
	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/protocol"
)

func sharedPathsEngine(t *testing.T) *Engine {
	t.Helper()
	data, err := os.ReadFile("../../examples/contracts/shared-generated-to-src.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := contract.DecodeBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	return newTestEngine(t, value)
}

// hookAction builds the action exactly as a PreToolUse hook does.
func hookAction(t *testing.T, kind protocol.AgentKind, cwd, tool string, input any) protocol.Action {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"session_id": "s", "cwd": cwd, "hook_event_name": "PreToolUse", "tool_name": tool, "tool_use_id": "t", "tool_input": input})
	decoded, err := adapter.DecodePreToolUse(raw)
	if err != nil {
		t.Fatal(err)
	}
	action := adapter.NormalizePreToolUse(kind, decoded, adapter.ProjectContext{ProjectID: "p"})
	action.Workspace.Root = "/workspace/project"
	return action
}

func effectiveInput(t *testing.T, decision protocol.Decision) map[string]any {
	t.Helper()
	if decision.Kind != protocol.DecisionRewrite {
		t.Fatalf("decision = %s: %s", decision.Kind, decision.Reason)
	}
	var input map[string]any
	if err := json.Unmarshal(decision.EffectiveInput, &input); err != nil {
		t.Fatal(err)
	}
	return input
}

// One contract on project paths corrects Claude Code's Write and Edit and
// Codex's apply_patch the same way, and the corrected call passes the
// re-check instead of still matching.
func TestPathsContractCorrectsEveryAgentTheSameWay(t *testing.T) {
	engine := sharedPathsEngine(t)

	decision, err := engine.Evaluate(hookAction(t, protocol.AgentClaudeCode, "/workspace/project", "Write",
		map[string]string{"file_path": "/workspace/project/generated/api/types.go", "content": "x"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := effectiveInput(t, decision)["file_path"]; got != "/workspace/project/src/api/types.go" {
		t.Fatalf("claude write corrected to %v", got)
	}

	decision, err = engine.Evaluate(hookAction(t, protocol.AgentClaudeCode, "/workspace/project", "Edit",
		map[string]string{"file_path": "/workspace/project/src/../generated/a.go", "old_string": "a", "new_string": "b"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := effectiveInput(t, decision)["file_path"]; got != "/workspace/project/src/a.go" {
		t.Fatalf("claude edit through .. corrected to %v", got)
	}

	// Codex patches are relative to its working directory, here a subdirectory.
	patch := "*** Begin Patch\n*** Update File: ../generated/a.go\n@@\n-generated/old\n+generated/new\n*** Update File: ../README.md\n@@\n-a\n+b\n*** End Patch\n"
	decision, err = engine.Evaluate(hookAction(t, protocol.AgentCodex, "/workspace/project/cmd", "apply_patch", map[string]string{"command": patch}))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(patch, "*** Update File: ../generated/a.go", "*** Update File: ../src/a.go", 1)
	if got := effectiveInput(t, decision)["command"]; got != want {
		t.Fatalf("codex patch corrected to %q", got)
	}
}

func TestPathsContractLeavesOtherPathsAlone(t *testing.T) {
	engine := sharedPathsEngine(t)
	for name, action := range map[string]protocol.Action{
		"similar prefix":   hookAction(t, protocol.AgentClaudeCode, "/workspace/project", "Write", map[string]string{"file_path": "/workspace/project/generated-docs/a.md", "content": "x"}),
		"other project":    hookAction(t, protocol.AgentClaudeCode, "/workspace/project", "Write", map[string]string{"file_path": "/workspace/other/generated/a.go", "content": "x"}),
		"nested generated": hookAction(t, protocol.AgentClaudeCode, "/workspace/project", "Write", map[string]string{"file_path": "/workspace/project/src/generated/a.go", "content": "x"}),
		"read":             hookAction(t, protocol.AgentClaudeCode, "/workspace/project", "Read", map[string]string{"file_path": "/workspace/project/generated/a.go"}),
		"command":          hookAction(t, protocol.AgentCodex, "/workspace/project", "Bash", map[string]string{"command": "cat generated/a.go"}),
	} {
		decision, err := engine.Evaluate(action)
		if err != nil || decision.Kind != protocol.DecisionPass {
			t.Errorf("%s: decision = %s %s, %v", name, decision.Kind, decision.Reason, err)
		}
	}
}

// A paths correction and a correction of the same tool argument cannot both
// apply without an order, so the engine asks.
func TestPathsCorrectionConflictsWithInputPathCorrection(t *testing.T) {
	shared := sharedPathsEngine(t).contracts[0]
	other := rewriteContract("other", "/workspace/project/generated/*", "/workspace/project/x.go", 800)
	other.Scope = contract.Scope{}
	other.Applicability.All[0].Field = "input.file_path"
	other.Correction.Transform.Values = map[string]any{"input.file_path": "/workspace/project/x.go"}
	engine := newTestEngine(t, shared, other)
	decision, err := engine.Evaluate(hookAction(t, protocol.AgentClaudeCode, "/workspace/project", "Write",
		map[string]string{"file_path": "/workspace/project/generated/a.go", "content": "x"}))
	if err != nil || decision.Kind != protocol.DecisionAsk || !strings.Contains(decision.Reason, "paths") {
		t.Fatalf("decision = %s %s, %v", decision.Kind, decision.Reason, err)
	}
}

func TestPathsTransformRequiresProjectRelativePrefixes(t *testing.T) {
	base := sharedPathsEngine(t).contracts[0]
	for _, from := range []string{"/workspace/project/generated", "../generated", "generated/", "a\\b"} {
		value := base
		transform := *base.Correction.Transform
		transform.From = from
		value.Correction.Transform = &transform
		if err := value.Validate(); err == nil {
			t.Errorf("from %q was accepted", from)
		}
	}
}
