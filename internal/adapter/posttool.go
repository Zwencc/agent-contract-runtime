package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/agent-contract-runtime/acr/internal/protocol"
)

type PostToolUseInput struct {
	SessionID      string          `json:"session_id"`
	TurnID         string          `json:"turn_id,omitempty"`
	PromptID       string          `json:"prompt_id,omitempty"` // Claude Code's turn identifier
	AgentID        string          `json:"agent_id,omitempty"`  // set when a subagent made the call
	CWD            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolUseID      string          `json:"tool_use_id"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolResponse   json.RawMessage `json:"tool_response,omitempty"`
	Error          string          `json:"error,omitempty"`
	IsInterrupt    bool            `json:"is_interrupt,omitempty"`
	DurationMS     int64           `json:"duration_ms,omitempty"`
	Model          string          `json:"model,omitempty"`
	PermissionMode string          `json:"permission_mode,omitempty"`
}

func NormalizePostToolUse(kind protocol.AgentKind, input PostToolUseInput, project ProjectContext) protocol.Action {
	return NormalizePreToolUse(kind, PreToolUseInput{
		SessionID:      input.SessionID,
		TurnID:         input.TurnID,
		CWD:            input.CWD,
		HookEventName:  "PreToolUse",
		ToolName:       input.ToolName,
		ToolUseID:      input.ToolUseID,
		ToolInput:      input.ToolInput,
		Model:          input.Model,
		PermissionMode: input.PermissionMode,
		AgentID:        input.AgentID,
	}, project)
}

func DecodePostToolUse(data []byte) (PostToolUseInput, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var input PostToolUseInput
	if err := decoder.Decode(&input); err != nil {
		return PostToolUseInput{}, fmt.Errorf("decode post-tool input: %w", err)
	}
	if input.HookEventName != "PostToolUse" && input.HookEventName != "PostToolUseFailure" {
		return PostToolUseInput{}, fmt.Errorf("hook_event_name must be PostToolUse or PostToolUseFailure, got %q", input.HookEventName)
	}
	if strings.TrimSpace(input.SessionID) == "" || strings.TrimSpace(input.CWD) == "" || strings.TrimSpace(input.ToolName) == "" {
		return PostToolUseInput{}, errors.New("post-tool input requires session_id, cwd, and tool_name")
	}
	var toolInput map[string]any
	if err := json.Unmarshal(input.ToolInput, &toolInput); err != nil || toolInput == nil {
		return PostToolUseInput{}, errors.New("post-tool tool_input must be a JSON object")
	}
	if input.TurnID == "" {
		input.TurnID = input.PromptID
	}
	if input.HookEventName == "PostToolUseFailure" && strings.TrimSpace(input.Error) == "" {
		return PostToolUseInput{}, errors.New("PostToolUseFailure error is required")
	}
	if input.HookEventName == "PostToolUse" && len(bytes.TrimSpace(input.ToolResponse)) == 0 {
		return PostToolUseInput{}, errors.New("PostToolUse tool_response is required")
	}
	return input, nil
}
