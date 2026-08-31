package adapter

import (
	"testing"

	"github.com/agent-contract-runtime/acr/internal/protocol"
)

func TestNormalizePreToolUseProducesSameProtocolForCodex(t *testing.T) {
	raw := []byte(`{
  "session_id":"session-1",
  "turn_id":"turn-1",
  "cwd":"/workspace/project",
  "hook_event_name":"PreToolUse",
  "tool_name":"apply_patch",
  "tool_use_id":"call-1",
  "tool_input":{"path":"/workspace/project/main.go","command":"*** Begin Patch"},
  "model":"gpt-codex",
  "permission_mode":"default",
  "future_field":"accepted"
}`)
	input, err := DecodePreToolUse(raw)
	if err != nil {
		t.Fatalf("DecodePreToolUse() error = %v", err)
	}
	action := NormalizePreToolUse(protocol.AgentCodex, input, ProjectContext{ProjectID: "project-1", WorkspaceID: "workspace-1"})
	if action.OperationKind != "filesystem.write" || len(action.Resources) != 1 {
		t.Fatalf("normalized action = %#v", action)
	}
	if action.Agent.NativeTurnID != "turn-1" || action.Tool.CallID != "call-1" {
		t.Fatalf("native correlation fields were not preserved: %#v", action)
	}
}

func TestDecodePreToolUseRejectsWrongEvent(t *testing.T) {
	_, err := DecodePreToolUse([]byte(`{"session_id":"s","cwd":"/p","hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{}}`))
	if err == nil {
		t.Fatal("DecodePreToolUse() succeeded for PostToolUse")
	}
}

func TestNormalizeApplyPatchExtractsOnlyDirectiveResources(t *testing.T) {
	raw := []byte(`{
  "session_id":"session-1",
  "cwd":"/workspace/project",
  "hook_event_name":"PreToolUse",
  "tool_name":"apply_patch",
  "tool_use_id":"call-1",
  "tool_input":{"command":"*** Begin Patch\n*** Update File: generated/schema.go\n@@\n-old\n+new\n*** End Patch"}
}`)
	input, err := DecodePreToolUse(raw)
	if err != nil {
		t.Fatalf("DecodePreToolUse() error = %v", err)
	}
	action := NormalizePreToolUse(protocol.AgentCodex, input, ProjectContext{})
	if len(action.Resources) != 1 || action.Resources[0] != "generated/schema.go" {
		t.Fatalf("resources = %#v", action.Resources)
	}
}
