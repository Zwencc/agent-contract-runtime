package distill

import (
	"fmt"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/evaluator"
	"github.com/agent-contract-runtime/acr/internal/protocol"
)

type CaseResult struct {
	Name        string                `json:"name"`
	ExpectMatch bool                  `json:"expect_match"`
	Matched     bool                  `json:"matched"`
	Decision    protocol.DecisionKind `json:"decision"`
	Passed      bool                  `json:"passed"`
}

// Simulate evaluates cases against value as if it were the only active
// contract, whatever its stored status, and reports whether it matched each.
func Simulate(value contract.Contract, cases []Case) ([]CaseResult, error) {
	value.Metadata.Status = contract.StatusActive
	engine, err := evaluator.New([]contract.Contract{value})
	if err != nil {
		return nil, err
	}
	results := make([]CaseResult, 0, len(cases))
	for _, item := range cases {
		decision, err := engine.Evaluate(item.Action)
		if err != nil {
			return nil, fmt.Errorf("simulate case %q: %w", item.Name, err)
		}
		matched := false
		for _, match := range decision.Matches {
			if match.ContractID == value.Metadata.ID {
				matched = true
			}
		}
		results = append(results, CaseResult{Name: item.Name, ExpectMatch: item.ExpectMatch, Matched: matched, Decision: decision.Kind, Passed: matched == item.ExpectMatch})
	}
	return results, nil
}
