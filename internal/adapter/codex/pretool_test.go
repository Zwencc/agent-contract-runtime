package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/protocol"
)

func TestEncodeRewriteUsesSupportedCodexShape(t *testing.T) {
	decision := protocol.NewDecision(protocol.DecisionRewrite, "corrected path")
	decision.EffectiveInput = json.RawMessage(`{"path":"src/main.go"}`)
	encoded, err := EncodePreToolUseDecision(decision)
	if err != nil {
		t.Fatalf("EncodePreToolUseDecision() error = %v", err)
	}
	if !strings.Contains(string(encoded), `"permissionDecision":"allow"`) || !strings.Contains(string(encoded), `"updatedInput":{"path":"src/main.go"}`) {
		t.Fatalf("encoded output = %s", encoded)
	}
}

func TestEncodeAskFailsClosedBecauseCodexAskIsUnsupported(t *testing.T) {
	decision := protocol.NewDecision(protocol.DecisionAsk, "choose a target")
	encoded, err := EncodePreToolUseDecision(decision)
	if err != nil {
		t.Fatalf("EncodePreToolUseDecision() error = %v", err)
	}
	if !strings.Contains(string(encoded), `"permissionDecision":"deny"`) || !strings.Contains(string(encoded), "does not support ask") {
		t.Fatalf("encoded output = %s", encoded)
	}
}
