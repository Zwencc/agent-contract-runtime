package claude

import (
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/protocol"
)

func TestEncodeAskUsesClaudeNativeAsk(t *testing.T) {
	decision := protocol.NewDecision(protocol.DecisionAsk, "choose a target")
	encoded, err := EncodePreToolUseDecision(decision)
	if err != nil {
		t.Fatalf("EncodePreToolUseDecision() error = %v", err)
	}
	if !strings.Contains(string(encoded), `"permissionDecision":"ask"`) {
		t.Fatalf("encoded output = %s", encoded)
	}
}
