// Package claude translates ACR decisions to Claude Code hook responses.
package claude

import (
	"encoding/json"
	"fmt"

	"github.com/agent-contract-runtime/acr/internal/protocol"
)

type preToolUseOutput struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

type hookSpecificOutput struct {
	HookEventName           string          `json:"hookEventName"`
	PermissionDecision      string          `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string         `json:"permissionDecisionReason,omitempty"`
	UpdatedInput            json.RawMessage `json:"updatedInput,omitempty"`
	AdditionalContext       string          `json:"additionalContext,omitempty"`
}

func EncodePreToolUseDecision(decision protocol.Decision) ([]byte, error) {
	output := preToolUseOutput{HookSpecificOutput: hookSpecificOutput{HookEventName: "PreToolUse"}}
	specific := &output.HookSpecificOutput

	switch decision.Kind {
	case protocol.DecisionPass:
		specific.AdditionalContext = decision.AdditionalContext
	case protocol.DecisionRewrite:
		if len(decision.EffectiveInput) == 0 || !json.Valid(decision.EffectiveInput) {
			return nil, fmt.Errorf("encode Claude rewrite: effective_input is not valid JSON")
		}
		specific.PermissionDecision = "allow"
		specific.PermissionDecisionReason = decision.Reason
		specific.UpdatedInput = decision.EffectiveInput
	case protocol.DecisionAsk:
		specific.PermissionDecision = "ask"
		specific.PermissionDecisionReason = decision.Reason
	case protocol.DecisionRepair:
		specific.PermissionDecision = "deny"
		specific.PermissionDecisionReason = "ACR requires guarded repair before retrying: " + decision.Reason
	case protocol.DecisionDeny:
		specific.PermissionDecision = "deny"
		specific.PermissionDecisionReason = decision.Reason
	default:
		return nil, fmt.Errorf("encode Claude decision: unsupported kind %q", decision.Kind)
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return nil, fmt.Errorf("encode Claude PreToolUse output: %w", err)
	}
	return encoded, nil
}
