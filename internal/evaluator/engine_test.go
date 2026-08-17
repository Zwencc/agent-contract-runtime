package evaluator

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/protocol"
)

func TestEvaluateRewritesAndRechecksAction(t *testing.T) {
	engine := newTestEngine(t, rewriteContract("rewrite-generated", "generated/*", "src/schema.go", 900))
	action := testAction(`{"path":"generated/schema.go","content":"package schema"}`)

	decision, err := engine.Evaluate(action)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if decision.Kind != protocol.DecisionRewrite {
		t.Fatalf("decision.Kind = %s, want rewrite: %s", decision.Kind, decision.Reason)
	}

	var corrected map[string]any
	if err := json.Unmarshal(decision.EffectiveInput, &corrected); err != nil {
		t.Fatalf("decode effective input: %v", err)
	}
	if corrected["path"] != "src/schema.go" {
		t.Fatalf("corrected path = %v", corrected["path"])
	}
}

func TestEvaluateIgnoresInactiveContract(t *testing.T) {
	value := rewriteContract("draft-contract", "generated/*", "src/schema.go", 900)
	value.Metadata.Status = contract.StatusDraft
	engine := newTestEngine(t, value)

	decision, err := engine.Evaluate(testAction(`{"path":"generated/schema.go"}`))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if decision.Kind != protocol.DecisionPass {
		t.Fatalf("decision.Kind = %s, want pass", decision.Kind)
	}
}

func TestEvaluateAsksOnConflictingRewrites(t *testing.T) {
	engine := newTestEngine(t,
		rewriteContract("rewrite-a", "generated/*", "src/a.go", 900),
		rewriteContract("rewrite-b", "generated/*", "src/b.go", 800),
	)

	decision, err := engine.Evaluate(testAction(`{"path":"generated/schema.go"}`))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if decision.Kind != protocol.DecisionAsk || !strings.Contains(decision.Reason, "conflicting") {
		t.Fatalf("decision = %#v, want conflicting ask", decision)
	}
}

func TestEvaluateDenyTakesPrecedenceOverRewrite(t *testing.T) {
	rewrite := rewriteContract("rewrite-generated", "generated/*", "src/schema.go", 900)
	deny := rewriteContract("deny-generated", "generated/*", "unused", 100)
	deny.Correction = contract.Correction{Mode: contract.CorrectionDeny}
	deny.Behavior.Instruction = "generated output is immutable"
	engine := newTestEngine(t, rewrite, deny)

	decision, err := engine.Evaluate(testAction(`{"path":"generated/schema.go"}`))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if decision.Kind != protocol.DecisionDeny {
		t.Fatalf("decision.Kind = %s, want deny", decision.Kind)
	}
}

func TestEvaluateRejectsCorrectionThatTriggersAnotherContract(t *testing.T) {
	rewrite := rewriteContract("rewrite-generated", "generated/*", "prod/schema.go", 900)
	deny := rewriteContract("protect-prod", "prod/*", "unused", 800)
	deny.Correction = contract.Correction{Mode: contract.CorrectionDeny}
	deny.Behavior.Instruction = "production path is protected"
	engine := newTestEngine(t, rewrite, deny)

	decision, err := engine.Evaluate(testAction(`{"path":"generated/schema.go"}`))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if decision.Kind != protocol.DecisionDeny || !strings.Contains(decision.Reason, "corrected action") {
		t.Fatalf("decision = %#v, want corrected-action denial", decision)
	}
}

func TestNewRejectsMultipleActiveVersions(t *testing.T) {
	first := rewriteContract("rewrite-generated", "generated/*", "src/schema.go", 900)
	second := first
	second.Metadata.Version = 2
	_, err := New([]contract.Contract{first, second})
	if err == nil || !strings.Contains(err.Error(), "multiple active versions") {
		t.Fatalf("New() error = %v, want multiple active versions", err)
	}
}

func TestEvaluatePreservesLargeIntegerInput(t *testing.T) {
	engine := newTestEngine(t, rewriteContract("rewrite-generated", "generated/*", "src/schema.go", 900))
	action := testAction(`{"path":"generated/schema.go","identifier":9007199254740993}`)

	decision, err := engine.Evaluate(action)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !strings.Contains(string(decision.EffectiveInput), `9007199254740993`) {
		t.Fatalf("effective input lost integer precision: %s", decision.EffectiveInput)
	}
}

func TestEvaluateRewritesOnlyApplyPatchPathDirectives(t *testing.T) {
	value := rewriteContract("rewrite-generated", "*generated/*", "unused", 900)
	value.Scope.ToolNames = []string{"apply_patch"}
	value.Applicability.All[0].Field = "input.command"
	value.Applicability.All[0].Operator = contract.OperatorContains
	value.Applicability.All[0].Value = json.RawMessage(`"*** Update File: generated/"`)
	value.Correction.Transform = &contract.Transform{
		Type:  contract.TransformReplacePath,
		Field: "input.command",
		From:  "generated",
		To:    "src",
	}
	engine := newTestEngine(t, value)
	action := testAction(`{"command":"*** Begin Patch\n*** Update File: generated/schema.go\n@@\n-content generated/schema.go\n+content generated/schema.go\n*** End Patch"}`)
	action.Tool.Name = "apply_patch"

	decision, err := engine.Evaluate(action)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if decision.Kind != protocol.DecisionRewrite {
		t.Fatalf("decision = %#v, want rewrite", decision)
	}
	output := string(decision.EffectiveInput)
	if !strings.Contains(output, `*** Update File: src/schema.go`) {
		t.Fatalf("patch directive was not corrected: %s", output)
	}
	if !strings.Contains(output, `content generated/schema.go`) {
		t.Fatalf("patch content was unexpectedly rewritten: %s", output)
	}
}

func newTestEngine(t *testing.T, values ...contract.Contract) *Engine {
	t.Helper()
	engine, err := New(values)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return engine
}

func rewriteContract(id, sourceGlob, target string, priority int) contract.Contract {
	return contract.Contract{
		APIVersion: contract.APIVersion,
		Kind:       "Contract",
		Metadata: contract.Metadata{
			ID:       id,
			Version:  1,
			Title:    id,
			Status:   contract.StatusActive,
			Severity: "high",
			Priority: priority,
		},
		Scope: contract.Scope{
			AgentKinds: []string{"codex", "claude-code"},
			ToolNames:  []string{"write_file"},
		},
		Applicability: contract.Applicability{All: []contract.Condition{{
			Field:    "input.path",
			Operator: contract.OperatorGlob,
			Value:    json.RawMessage(`"` + sourceGlob + `"`),
		}}},
		Behavior: contract.Behavior{
			Instruction: "write to the approved source path",
			Enforcement: contract.EnforcementHard,
		},
		Correction: contract.Correction{
			Mode: contract.CorrectionRewrite,
			Transform: &contract.Transform{
				Type:   contract.TransformSetFields,
				Values: map[string]any{"input.path": target},
			},
		},
		FailureMode: contract.FailureClosed,
	}
}

func testAction(input string) protocol.Action {
	return protocol.NewAction(
		protocol.AgentContext{Kind: protocol.AgentCodex, NativeSessionID: "session-1"},
		protocol.WorkspaceContext{CWD: "/workspace/project", ProjectID: "project-1"},
		protocol.ToolCall{Name: "write_file", Input: json.RawMessage(input)},
	)
}
