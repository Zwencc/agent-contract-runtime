package protocol

import (
	"encoding/json"
	"testing"
)

func TestActionJSONUsesStableSchema(t *testing.T) {
	action := NewAction(
		AgentContext{Kind: AgentCodex, NativeSessionID: "session-1"},
		WorkspaceContext{CWD: "/workspace/project", ProjectID: "project-1"},
		ToolCall{Name: "shell", Input: json.RawMessage(`{"command":"go test ./..."}`)},
	)

	encoded, err := json.Marshal(action)
	if err != nil {
		t.Fatalf("marshal action: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal action: %v", err)
	}

	if got := decoded["schema_version"]; got != ActionSchemaVersion {
		t.Fatalf("schema_version = %v, want %s", got, ActionSchemaVersion)
	}
	if got := decoded["operation_kind"]; got != "tool.call" {
		t.Fatalf("operation_kind = %v, want tool.call", got)
	}
}

func TestDecisionJSONOmitsUnusedCorrectionFields(t *testing.T) {
	decision := NewDecision(DecisionPass, "no applicable contract")

	encoded, err := json.Marshal(decision)
	if err != nil {
		t.Fatalf("marshal decision: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal decision: %v", err)
	}

	if _, ok := decoded["effective_input"]; ok {
		t.Fatal("effective_input must be omitted for pass decisions")
	}
	if got := decoded["kind"]; got != string(DecisionPass) {
		t.Fatalf("kind = %v, want %s", got, DecisionPass)
	}
}
