package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type SessionStartInput struct {
	SessionID     string `json:"session_id"`
	CWD           string `json:"cwd"`
	HookEventName string `json:"hook_event_name"`
	Source        string `json:"source,omitempty"`
	Model         string `json:"model,omitempty"`
}

func DecodeSessionStart(data []byte) (SessionStartInput, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var input SessionStartInput
	if err := decoder.Decode(&input); err != nil {
		return SessionStartInput{}, fmt.Errorf("decode SessionStart input: %w", err)
	}
	if input.HookEventName != "SessionStart" {
		return SessionStartInput{}, fmt.Errorf("hook_event_name must be SessionStart, got %q", input.HookEventName)
	}
	if strings.TrimSpace(input.SessionID) == "" || strings.TrimSpace(input.CWD) == "" {
		return SessionStartInput{}, errors.New("SessionStart requires session_id and cwd")
	}
	return input, nil
}

func EncodeSessionContext(context string) ([]byte, error) {
	return EncodeHookContext("SessionStart", context)
}

// EncodeHookContext returns the hook output that injects context for
// SessionStart or UserPromptSubmit on both Claude Code and Codex.
func EncodeHookContext(eventName, context string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":    eventName,
			"additionalContext": context,
		},
	})
}

type UserPromptSubmitInput struct {
	SessionID     string `json:"session_id"`
	TurnID        string `json:"turn_id,omitempty"`
	PromptID      string `json:"prompt_id,omitempty"` // Claude Code's turn identifier
	CWD           string `json:"cwd"`
	HookEventName string `json:"hook_event_name"`
	Prompt        string `json:"prompt"`
}

func DecodeUserPromptSubmit(data []byte) (UserPromptSubmitInput, error) {
	var input UserPromptSubmitInput
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&input); err != nil {
		return UserPromptSubmitInput{}, fmt.Errorf("decode UserPromptSubmit input: %w", err)
	}
	if input.HookEventName != "UserPromptSubmit" {
		return UserPromptSubmitInput{}, fmt.Errorf("hook_event_name must be UserPromptSubmit, got %q", input.HookEventName)
	}
	if strings.TrimSpace(input.SessionID) == "" || strings.TrimSpace(input.CWD) == "" {
		return UserPromptSubmitInput{}, errors.New("UserPromptSubmit requires session_id and cwd")
	}
	if input.TurnID == "" {
		input.TurnID = input.PromptID
	}
	return input, nil
}
