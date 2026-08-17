package evaluator

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/protocol"
)

type fakeEvidence struct {
	states map[string]string
	err    error
	calls  int
}

func (fake *fakeEvidence) Satisfied(_ protocol.Action, subject string) (bool, string, error) {
	fake.calls++
	if fake.err != nil {
		return false, "", fake.err
	}
	state, ok := fake.states[subject]
	if !ok {
		return false, "missing", nil
	}
	return state == "valid", state, nil
}

func evidenceContract(enforcement contract.Enforcement, mode contract.CorrectionMode, failure contract.FailureMode) contract.Contract {
	value := rewriteContract("tests-before-deploy", "deploy/*", "unused", 900)
	value.Applicability = contract.Applicability{All: []contract.Condition{{
		Field: "input.path", Operator: contract.OperatorGlob, Value: json.RawMessage(`"deploy/*"`),
	}}}
	value.Behavior = contract.Behavior{Instruction: "Run the unit tests before deploying", Enforcement: enforcement}
	value.Correction = contract.Correction{Mode: mode}
	value.FailureMode = failure
	value.Verifiers = &contract.Verifiers{Evidence: []contract.EvidenceRequirement{
		{Subject: "repair:checks/unit", Remedy: "acr repair run --plan .acr/plans/checks.json --apply"},
		{Subject: "repair:checks/lint"},
	}}
	return value
}

func TestEvidenceContractDeniesUntilEvidenceIsValid(t *testing.T) {
	checker := &fakeEvidence{states: map[string]string{"repair:checks/unit": "stale because its inputs changed after it passed"}}
	engine := newTestEngine(t, evidenceContract(contract.EnforcementHard, contract.CorrectionNone, contract.FailureClosed)).WithEvidence(checker)

	decision, err := engine.Evaluate(testAction(`{"path":"deploy/prod"}`))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != protocol.DecisionDeny ||
		!strings.Contains(decision.Reason, "repair:checks/unit is stale") ||
		!strings.Contains(decision.Reason, "run: acr repair run --plan .acr/plans/checks.json --apply") ||
		!strings.Contains(decision.Reason, "repair:checks/lint is missing") {
		t.Fatalf("decision = %s: %s", decision.Kind, decision.Reason)
	}

	checker.states = map[string]string{"repair:checks/unit": "valid", "repair:checks/lint": "valid"}
	decision, err = engine.Evaluate(testAction(`{"path":"deploy/prod"}`))
	if err != nil || decision.Kind != protocol.DecisionPass || len(decision.Matches) != 0 {
		t.Fatalf("satisfied decision = %+v, %v", decision, err)
	}

	calls := checker.calls
	if decision, _ := engine.Evaluate(testAction(`{"path":"src/main.go"}`)); decision.Kind != protocol.DecisionPass || checker.calls != calls {
		t.Fatal("evidence was checked for an action the contract does not apply to")
	}
}

func TestEvidenceContractWithoutCheckerIsNeverSatisfied(t *testing.T) {
	engine := newTestEngine(t, evidenceContract(contract.EnforcementHard, contract.CorrectionDeny, contract.FailureClosed))
	decision, err := engine.Evaluate(testAction(`{"path":"deploy/prod"}`))
	if err != nil || decision.Kind != protocol.DecisionDeny || !strings.Contains(decision.Reason, "no evidence checker configured") {
		t.Fatalf("decision = %+v, %v", decision, err)
	}
}

func TestEvidenceCheckErrorsFollowFailureMode(t *testing.T) {
	broken := &fakeEvidence{err: errors.New("event store unreadable")}
	cases := map[contract.FailureMode]protocol.DecisionKind{
		contract.FailureClosed: protocol.DecisionDeny,
		contract.FailureAsk:    protocol.DecisionAsk,
		contract.FailureOpen:   protocol.DecisionPass,
	}
	for mode, want := range cases {
		engine := newTestEngine(t, evidenceContract(contract.EnforcementHard, contract.CorrectionNone, mode)).WithEvidence(broken)
		decision, err := engine.Evaluate(testAction(`{"path":"deploy/prod"}`))
		if err != nil || decision.Kind != want {
			t.Fatalf("failure mode %s decision = %+v, %v; want %s", mode, decision, err, want)
		}
	}
}

func TestAdvisoryEvidenceContractExplainsMissingEvidence(t *testing.T) {
	engine := newTestEngine(t, evidenceContract(contract.EnforcementAdvisory, contract.CorrectionNone, contract.FailureOpen)).
		WithEvidence(&fakeEvidence{states: map[string]string{}})
	decision, err := engine.Evaluate(testAction(`{"path":"deploy/prod"}`))
	if err != nil || decision.Kind != protocol.DecisionPass || !strings.Contains(decision.AdditionalContext, "missing evidence") {
		t.Fatalf("decision = %+v, %v", decision, err)
	}
}

func TestEvidenceVerifiersAreValidated(t *testing.T) {
	rewrite := evidenceContract(contract.EnforcementHard, contract.CorrectionNone, contract.FailureClosed)
	rewrite.Correction = rewriteContract("x", "a/*", "b", 1).Correction
	empty := evidenceContract(contract.EnforcementHard, contract.CorrectionNone, contract.FailureClosed)
	empty.Verifiers.Evidence = nil
	blank := evidenceContract(contract.EnforcementHard, contract.CorrectionNone, contract.FailureClosed)
	blank.Verifiers.Evidence[0].Subject = " "
	for name, value := range map[string]contract.Contract{"rewrite": rewrite, "empty": empty, "blank subject": blank} {
		if _, err := New([]contract.Contract{value}); err == nil {
			t.Fatalf("%s: New() error = nil", name)
		}
	}
}
