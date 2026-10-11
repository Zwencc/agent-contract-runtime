package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/project"
)

func hookContext(t *testing.T, stdout string) string {
	t.Helper()
	var output struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("hook output %q: %v", stdout, err)
	}
	return output.HookSpecificOutput.AdditionalContext
}

func promptSubmit(t *testing.T, agent, root, prompt string, config project.Config, paths project.Paths) string {
	t.Helper()
	return promptInSession(t, agent, "codex-session", root, prompt, config, paths)
}

func promptInSession(t *testing.T, agent, session, root, prompt string, config project.Config, paths project.Paths) string {
	t.Helper()
	input, _ := json.Marshal(map[string]string{"session_id": session, "cwd": root, "hook_event_name": "UserPromptSubmit", "prompt": prompt})
	var stdout, stderr bytes.Buffer
	code := run([]string{"hook", agent, "user-prompt-submit", "--event-db", paths.EventDB, "--project-id", config.ProjectID, "--root", root},
		bytes.NewReader(input), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("user-prompt-submit code = %d, stderr = %s", code, stderr.String())
	}
	return stdout.String()
}

// Scenario B of the development plan: a failure recorded from Claude Code
// reaches a new Codex session working on the same module, as evidence.
func TestFailureEvidenceReachesOtherAgent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal", "auth"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"internal/auth/login.go", "internal/auth/token.go", "README.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}

	failure, _ := json.Marshal(map[string]any{
		"session_id": "claude-session", "cwd": root, "hook_event_name": "PostToolUseFailure", "tool_name": "Edit", "tool_use_id": "call-1",
		"tool_input": map[string]string{"file_path": filepath.Join(root, "internal/auth/login.go"), "old_string": "a", "new_string": "b"},
		"error":      "login_test.go:31: approach A: expected 200, got 500",
	})
	if code := run([]string{"hook", "claude", "post-tool-use-failure", "--event-db", paths.EventDB, "--project-id", config.ProjectID},
		bytes.NewReader(failure), &stdout, &stderr); code != 0 {
		t.Fatalf("post-tool-use-failure code = %d, stderr = %s", code, stderr.String())
	}

	session := sessionStart(t, root, config, paths)
	if context := hookContext(t, session); !strings.Contains(context, "## Known failure patterns") ||
		!strings.Contains(context, "approach A: expected #, got #") {
		t.Fatalf("session context lacks the failure pattern:\n%s", context)
	}

	secret := "sk-" + strings.Repeat("z", 24)
	output := promptSubmit(t, "codex", root, "修复 `internal/auth/login.go` 的登录问题，token "+secret+"，参考 /etc/passwd 和 ../outside", config, paths)
	promptContext := hookContext(t, output)
	if !strings.Contains(promptContext, "# ACR state for internal/auth/login.go") || !strings.Contains(promptContext, "file match] login_test.go:#: approach A") {
		t.Fatalf("prompt context lacks file-matched failure evidence:\n%s", promptContext)
	}

	moduleOutput := promptSubmit(t, "claude", root, "look at internal/auth/token.go.", config, paths)
	if context := hookContext(t, moduleOutput); !strings.Contains(context, "module match] login_test.go:#: approach A") {
		t.Fatalf("same-module failure missing:\n%s", context)
	}
	if plain := promptSubmit(t, "codex", root, "what is the plan for today?", config, paths); strings.TrimSpace(plain) != "{}" {
		t.Fatalf("prompt without paths got context: %s", plain)
	}

	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.List(context.Background(), config.ProjectID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var intents []eventstore.Event
	for _, event := range events {
		if event.Type == "intent.observed" {
			intents = append(intents, event)
		}
	}
	if len(intents) != 3 {
		t.Fatalf("recorded %d intents, want 3", len(intents))
	}
	first := string(intents[0].Payload)
	if strings.Contains(first, secret) || !strings.Contains(first, "[REDACTED]") || !strings.Contains(first, `"mentioned_paths":["internal/auth/login.go"]`) {
		t.Fatalf("intent payload = %s", first)
	}
}

func TestMentionedPathsStaysInsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".acr"), 0o700); err != nil {
		t.Fatal(err)
	}
	got := mentionedPaths(root, "edit src/main.go and "+filepath.Join(root, "src")+", not escape, .acr, "+outside+" or https://example.com/src")
	if strings.Join(got, ",") != "src/main.go,src" {
		t.Fatalf("mentionedPaths() = %v", got)
	}
}

func TestInstallHooksIncludesUserPromptSubmit(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	stdout.Reset()
	if code := run([]string{"install-hooks", "--agent", "all", "--root", root, "--dry-run"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("install-hooks code = %d, stderr = %s", code, stderr.String())
	}
	if strings.Count(stdout.String(), `"UserPromptSubmit"`) != 2 || !strings.Contains(stdout.String(), "user-prompt-submit") {
		t.Fatalf("install preview lacks UserPromptSubmit for both agents:\n%s", stdout.String())
	}
}

// Real Claude Code input: the user's prompt is recorded as the user's intent
// under its prompt_id turn, while a background task notification submitted
// through the same hook is recorded as a host message.
func TestPromptHookSeparatesUserPromptsFromTaskNotifications(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"UserPromptSubmit.json", "UserPromptSubmit-task-notification.json"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "internal", "adapter", "testdata", "live", "claude-code-2.1.303", name))
		if err != nil {
			t.Fatal(err)
		}
		if code := run([]string{"hook", "claude", "user-prompt-submit", "--event-db", paths.EventDB, "--project-id", config.ProjectID, "--root", root},
			bytes.NewReader(data), &stdout, &stderr); code != 0 {
			t.Fatalf("%s code = %d, stderr = %s", name, code, stderr.String())
		}
	}
	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.List(context.Background(), config.ProjectID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != "intent.observed" || events[0].Actor.Kind != "user" || events[0].Causality.NativeTurnID == "" ||
		events[1].Type != "host.notification" || events[1].Actor.Kind != "host" {
		t.Fatalf("events = %+v", events)
	}
}
