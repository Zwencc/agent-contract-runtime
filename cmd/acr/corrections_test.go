package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/memory"
	"github.com/agent-contract-runtime/acr/internal/project"
)

func activeMemory(t *testing.T, store *memory.Store, projectID, claim string) memory.Memory {
	t.Helper()
	ctx := context.Background()
	value, err := store.CreateCandidate(ctx, memory.CreateRequest{ProjectID: projectID, Kind: "decision", Priority: memory.P1, Claim: claim,
		Scope: json.RawMessage(`{}`), EvidenceRefs: []string{"event:seed"}, Source: "user", Confidence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if value, err = store.Activate(ctx, projectID, value.MemoryID, "approved"); err != nil {
		t.Fatal(err)
	}
	return value
}

func claudePreToolUse(t *testing.T, root, session string, config project.Config, paths project.Paths) string {
	t.Helper()
	input, _ := json.Marshal(map[string]any{"session_id": session, "cwd": root, "hook_event_name": "PreToolUse", "tool_name": "Read",
		"tool_use_id": "read", "tool_input": map[string]string{"file_path": "README.md"}})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"hook", "claude", "pre-tool-use", "--contracts", paths.Contracts, "--event-db", paths.EventDB, "--project-id", config.ProjectID},
		bytes.NewReader(input), &stdout, &stderr); code != 0 {
		t.Fatalf("pre-tool-use code = %d, stderr = %s", code, stderr.String())
	}
	var output struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	return output.HookSpecificOutput.AdditionalContext
}

// A memory corrected while an agent session runs reaches that session once,
// through its next prompt or tool call, and is not repeated.
func TestRunningSessionLearnsMemoryCorrections(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	memories, err := memory.Open(paths.MemoryDB)
	if err != nil {
		t.Fatal(err)
	}
	defer memories.Close()
	wrong := activeMemory(t, memories, config.ProjectID, "Deploy with make release")
	doubtful := activeMemory(t, memories, config.ProjectID, "Staging uses database v12")
	stale := activeMemory(t, memories, config.ProjectID, "CI runs on Go 1.25")
	later := activeMemory(t, memories, config.ProjectID, "The API listens on port 8080")

	input, _ := json.Marshal(map[string]string{"session_id": "claude-1", "cwd": root, "hook_event_name": "SessionStart", "source": "startup"})
	if code := run([]string{"hook", "claude", "session-start", "--contracts", paths.Contracts, "--event-db", paths.EventDB,
		"--project-id", config.ProjectID, "--memory-db", paths.MemoryDB}, bytes.NewReader(input), &stdout, &stderr); code != 0 {
		t.Fatalf("session-start code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Deploy with make release") {
		t.Fatal("test setup: the session did not receive the memory")
	}

	ctx := context.Background()
	corrected, err := memories.Correct(ctx, config.ProjectID, wrong.MemoryID, memory.CorrectRequest{
		Claim: "Deploy with ./scripts/release.sh", EvidenceRefs: []string{"event:user"}, Source: "user", Confidence: 1, Reason: "make release was removed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memories.Retract(ctx, config.ProjectID, stale.MemoryID, "CI moved to Go 1.27"); err != nil {
		t.Fatal(err)
	}
	if _, err := memories.Contest(ctx, config.ProjectID, doubtful.MemoryID, "ops reports v13"); err != nil {
		t.Fatal(err)
	}

	notice := hookContext(t, promptInSession(t, "claude", "claude-1", root, "continue with the release", config, paths))
	for _, want := range []string{
		"# ACR memory corrections",
		`[corrected] ` + wrong.MemoryID + ` "Deploy with make release" is wrong; use ` + corrected.MemoryID + `: "Deploy with ./scripts/release.sh"`,
		"(reason: make release was removed)",
		`[retracted] ` + stale.MemoryID,
		`[contested] ` + doubtful.MemoryID,
	} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice lacks %q:\n%s", want, notice)
		}
	}
	if again := promptInSession(t, "claude", "claude-1", root, "next step", config, paths); strings.TrimSpace(again) != "{}" {
		t.Fatalf("corrections were repeated: %s", again)
	}

	if _, err := memories.MarkStale(ctx, config.ProjectID, later.MemoryID, "service moved behind the gateway"); err != nil {
		t.Fatal(err)
	}
	if context := claudePreToolUse(t, root, "claude-1", config, paths); !strings.Contains(context, "[stale] "+later.MemoryID) || strings.Contains(context, "[retracted]") {
		t.Fatalf("tool call context = %q", context)
	}
	if context := claudePreToolUse(t, root, "claude-1", config, paths); context != "" {
		t.Fatalf("stale notice repeated: %q", context)
	}

	// A session that starts after the corrections already sees current memory.
	if fresh := promptInSession(t, "codex", "codex-2", root, "hello", config, paths); strings.TrimSpace(fresh) != "{}" {
		t.Fatalf("new session got old corrections: %s", fresh)
	}
}
