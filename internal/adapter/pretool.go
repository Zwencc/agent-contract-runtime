// Package adapter converts native agent hook messages to ACR protocol types.
package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/agent-contract-runtime/acr/internal/protocol"
)

type PreToolUseInput struct {
	SessionID      string          `json:"session_id"`
	TurnID         string          `json:"turn_id,omitempty"`
	PromptID       string          `json:"prompt_id,omitempty"` // Claude Code's turn identifier
	AgentID        string          `json:"agent_id,omitempty"`  // set when a subagent made the call
	CWD            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolUseID      string          `json:"tool_use_id"`
	ToolInput      json.RawMessage `json:"tool_input"`
	Model          string          `json:"model,omitempty"`
	PermissionMode string          `json:"permission_mode,omitempty"`
}

type ProjectContext struct {
	ProjectID   string
	WorkspaceID string
}

func DecodePreToolUse(data []byte) (PreToolUseInput, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	var input PreToolUseInput
	if err := decoder.Decode(&input); err != nil {
		return PreToolUseInput{}, fmt.Errorf("decode PreToolUse input: %w", err)
	}
	if input.HookEventName != "PreToolUse" {
		return PreToolUseInput{}, fmt.Errorf("hook_event_name must be PreToolUse, got %q", input.HookEventName)
	}
	if strings.TrimSpace(input.SessionID) == "" {
		return PreToolUseInput{}, errors.New("PreToolUse session_id is required")
	}
	if strings.TrimSpace(input.CWD) == "" {
		return PreToolUseInput{}, errors.New("PreToolUse cwd is required")
	}
	if strings.TrimSpace(input.ToolName) == "" {
		return PreToolUseInput{}, errors.New("PreToolUse tool_name is required")
	}
	if input.TurnID == "" {
		input.TurnID = input.PromptID
	}
	var toolInput map[string]any
	if err := json.Unmarshal(input.ToolInput, &toolInput); err != nil || toolInput == nil {
		return PreToolUseInput{}, errors.New("PreToolUse tool_input must be a JSON object")
	}
	return input, nil
}

func NormalizePreToolUse(kind protocol.AgentKind, input PreToolUseInput, project ProjectContext) protocol.Action {
	action := protocol.NewAction(
		protocol.AgentContext{
			Kind:            kind,
			NativeSessionID: input.SessionID,
			NativeTurnID:    input.TurnID,
			Version:         input.Model,
		},
		protocol.WorkspaceContext{
			CWD:         input.CWD,
			ProjectID:   project.ProjectID,
			WorkspaceID: project.WorkspaceID,
		},
		protocol.ToolCall{
			Name:   input.ToolName,
			Input:  input.ToolInput,
			CallID: input.ToolUseID,
		},
	)
	action.OperationKind = operationKind(input.ToolName)
	action.Resources = resourcesOf(input.ToolName, input.ToolInput)
	if input.PermissionMode != "" || input.AgentID != "" {
		action.Attributes = map[string]string{}
	}
	if input.PermissionMode != "" {
		action.Attributes["permission_mode"] = input.PermissionMode
	}
	if input.AgentID != "" {
		action.Attributes["subagent_id"] = input.AgentID
	}
	return action
}

func operationKind(toolName string) string {
	switch toolName {
	case "Bash", "PowerShell":
		return "process.exec"
	case "apply_patch", "Edit", "Write", "NotebookEdit":
		return "filesystem.write"
	case "Read", "Glob", "Grep":
		return "filesystem.read"
	}
	if strings.HasPrefix(toolName, "mcp__") {
		return "mcp.call"
	}
	return "tool.call"
}

func resourcesOf(toolName string, raw json.RawMessage) []string {
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	var resources []string
	for _, field := range []string{"file_path", "path"} {
		value, ok := input[field].(string)
		if !ok || value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		resources = append(resources, value)
	}
	if toolName == "apply_patch" {
		if command, ok := input["command"].(string); ok {
			for _, line := range strings.Split(command, "\n") {
				for _, marker := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "} {
					if !strings.HasPrefix(line, marker) {
						continue
					}
					value := strings.TrimSpace(strings.TrimPrefix(line, marker))
					if value != "" {
						if _, exists := seen[value]; !exists {
							seen[value] = struct{}{}
							resources = append(resources, value)
						}
					}
					break
				}
			}
		}
	}
	return resources
}
