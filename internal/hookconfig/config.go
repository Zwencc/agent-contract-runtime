// Package hookconfig builds idempotent project-local hook configurations.
package hookconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type Commands struct {
	SessionStart       string
	UserPromptSubmit   string
	PreToolUse         string
	PostToolUse        string
	PostToolUseFailure string
	// Lifecycle handles Stop, SubagentStart, SubagentStop and SessionEnd.
	Lifecycle string
}

func Merge(existing []byte, agent string, commands Commands) ([]byte, error) {
	root := map[string]any{}
	if len(bytes.TrimSpace(existing)) != 0 {
		decoder := json.NewDecoder(bytes.NewReader(existing))
		decoder.UseNumber()
		if err := decoder.Decode(&root); err != nil {
			return nil, fmt.Errorf("decode existing hook config: %w", err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return nil, errors.New("decode existing hook config: multiple JSON values")
		}
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		if _, exists := root["hooks"]; exists {
			return nil, errors.New("existing hook config field hooks must be an object")
		}
		hooks = map[string]any{}
		root["hooks"] = hooks
	}
	if agent == "codex" {
		if _, exists := root["description"]; !exists {
			root["description"] = "Project hooks including Agent Contract Runtime"
		}
	} else if agent != "claude" {
		return nil, fmt.Errorf("unsupported hook agent %q", agent)
	}

	appendCommand(hooks, "SessionStart", commands.SessionStart, "ACR is loading project context")
	appendCommand(hooks, "UserPromptSubmit", commands.UserPromptSubmit, "ACR is adding task context")
	appendCommand(hooks, "PreToolUse", commands.PreToolUse, "ACR is evaluating contracts")
	appendCommand(hooks, "PostToolUse", commands.PostToolUse, "ACR is recording tool outcome")
	if agent == "claude" {
		appendCommand(hooks, "PostToolUseFailure", commands.PostToolUseFailure, "ACR is recording tool failure")
	}
	for _, event := range []string{"Stop", "SubagentStart", "SubagentStop", "SessionEnd"} {
		appendCommand(hooks, event, commands.Lifecycle, "ACR is recording the session")
	}
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode merged hook config: %w", err)
	}
	return append(encoded, '\n'), nil
}

func appendCommand(hooks map[string]any, eventName, command, status string) {
	if command == "" || containsCommand(hooks[eventName], command) {
		return
	}
	groups, _ := hooks[eventName].([]any)
	handler := map[string]any{
		"type":          "command",
		"command":       command,
		"timeout":       5,
		"statusMessage": status,
	}
	group := map[string]any{
		"matcher": "*",
		"hooks":   []any{handler},
	}
	hooks[eventName] = append(groups, group)
}

func containsCommand(value any, command string) bool {
	switch typed := value.(type) {
	case map[string]any:
		if current, ok := typed["command"].(string); ok && current == command {
			return true
		}
		for _, child := range typed {
			if containsCommand(child, command) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsCommand(child, command) {
				return true
			}
		}
	}
	return false
}
