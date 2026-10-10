package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/project"
)

// setupReplayProject records three tool outcomes through the real hook: two
// in session-1 (one with a large response) and one in session-2.
func setupReplayProject(t *testing.T) (string, project.Paths) {
	t.Helper()
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	calls := []struct{ session, callID, output string }{
		{"session-1", "call-1", strings.Repeat("PASS\n", 10000)},
		{"session-1", "call-2", "small"},
		{"session-2", "call-3", "other session"},
	}
	for _, call := range calls {
		response, _ := json.Marshal(map[string]string{"output": call.output})
		input, _ := json.Marshal(map[string]any{
			"session_id": call.session, "cwd": root, "hook_event_name": "PostToolUse",
			"tool_name": "Bash", "tool_use_id": call.callID,
			"tool_input":    map[string]string{"command": "run " + call.callID},
			"tool_response": json.RawMessage(response),
		})
		if code := run([]string{"hook", "claude", "post-tool-use", "--event-db", paths.EventDB, "--project-id", config.ProjectID},
			bytes.NewReader(input), &stdout, &stderr); code != 0 {
			t.Fatalf("hook code = %d, stderr = %s", code, stderr.String())
		}
	}
	return root, paths
}

func replay(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"replay"}, args...), strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestReplayFiltersBySessionAndToolUse(t *testing.T) {
	root, _ := setupReplayProject(t)

	code, stdout, stderr := replay(t, "--root", root, "--session", "session-1")
	if code != 0 {
		t.Fatalf("replay code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "tool_use=call-1") || !strings.Contains(stdout, "tool_use=call-2") || strings.Contains(stdout, "call-3") {
		t.Fatalf("session replay = %s", stdout)
	}
	if !strings.Contains(stdout, "tool_response_ref: object sha256:") || strings.Contains(stdout, "PASS") {
		t.Fatalf("object should be referenced, not inlined, without --with-objects:\n%s", stdout)
	}

	code, stdout, stderr = replay(t, "--root", root, "--correlation", "call-1", "--with-objects")
	if code != 0 || !strings.Contains(stdout, "PASS") || strings.Contains(stdout, "call-2") {
		t.Fatalf("correlation replay code = %d, stdout = %.500s, stderr = %s", code, stdout, stderr)
	}

	code, stdout, stderr = replay(t, "--root", root, "--tool-use", "call-3", "--format", "json")
	if code != 0 {
		t.Fatalf("json replay code = %d, stderr = %s", code, stderr)
	}
	var entries []replayEntry
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil || len(entries) != 1 || entries[0].Event.Causality.NativeToolUseID != "call-3" {
		t.Fatalf("json replay = %s, %v", stdout, err)
	}

	code, stdout, _ = replay(t, "--root", root, "--session", "missing")
	if code != 0 || !strings.Contains(stdout, "no matching events") {
		t.Fatalf("empty replay code = %d, stdout = %s", code, stdout)
	}
}

func TestReplayReportsLimit(t *testing.T) {
	root, _ := setupReplayProject(t)
	code, stdout, stderr := replay(t, "--root", root, "--all", "--limit", "2", "--format", "json")
	var entries []replayEntry
	if code != 0 || json.Unmarshal([]byte(stdout), &entries) != nil || len(entries) != 2 || !strings.Contains(stderr, "--limit 2") {
		t.Fatalf("limited replay code = %d, entries = %d, stderr = %s", code, len(entries), stderr)
	}
}

func TestReplayFailsOnMissingObject(t *testing.T) {
	root, paths := setupReplayProject(t)
	err := filepath.WalkDir(paths.Objects, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			return os.Remove(path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := replay(t, "--root", root, "--tool-use", "call-1", "--with-objects")
	if code != 1 || !strings.Contains(stdout, "object unavailable") || !strings.Contains(stderr, "could not be verified") {
		t.Fatalf("replay with missing object code = %d, stdout = %s, stderr = %s", code, stdout, stderr)
	}
}

func TestReplayRefusesTamperedChain(t *testing.T) {
	root, paths := setupReplayProject(t)
	db, err := sql.Open("sqlite", paths.EventDB)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TRIGGER events_no_update`,
		`UPDATE events SET payload = '{"tool_input":{"command":"forged"}}' WHERE sequence = 2`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	db.Close()

	code, stdout, stderr := replay(t, "--root", root, "--all")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "replay refused") {
		t.Fatalf("replay of tampered chain code = %d, stdout = %s, stderr = %s", code, stdout, stderr)
	}
}

func TestReplayValidatesArguments(t *testing.T) {
	root, _ := setupReplayProject(t)
	for _, args := range [][]string{
		{"--root", root},
		{"--root", root, "--session", "a", "--all"},
		{"--root", root, "--all", "--limit", "0"},
		{"--root", root, "--all", "--limit", "999999"},
		{"--root", root, "--all", "--format", "yaml"},
	} {
		if code, _, _ := replay(t, args...); code != 2 {
			t.Fatalf("replay %v code = %d, want 2", args, code)
		}
	}
}

// Within one acr launch, hooks record under the agent's session ID and the
// guarded server under the launch ID; replaying either one shows both sides.
func TestReplayLinksHookAndGuardedEventsOfALaunch(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	hook := func(session, callID string) {
		t.Helper()
		input, _ := json.Marshal(map[string]any{
			"session_id": session, "cwd": root, "hook_event_name": "PostToolUse", "tool_name": "Read", "tool_use_id": callID,
			"tool_input": map[string]string{"file_path": "README.md"}, "tool_response": map[string]string{"output": "ok"},
		})
		if code := run([]string{"hook", "claude", "post-tool-use", "--event-db", paths.EventDB, "--project-id", config.ProjectID},
			bytes.NewReader(input), &stdout, &stderr); code != 0 {
			t.Fatalf("hook code = %d, stderr = %s", code, stderr.String())
		}
	}
	t.Setenv("ACR_LAUNCH_ID", "launch-1")
	hook("claude-1", "hooked-call")
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write_file","arguments":{"file_path":"notes.txt","content":"guarded\n"}}}` + "\n"
	if code := run([]string{"mcp", "serve", "--root", root, "--agent", "claude"}, strings.NewReader(call), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"isError":false`) {
		t.Fatalf("mcp serve code = %d, stdout = %s, stderr = %s", code, stdout.String(), stderr.String())
	}
	t.Setenv("ACR_LAUNCH_ID", "")
	hook("claude-2", "unlaunched-call")

	for _, session := range []string{"launch-1", "claude-1"} {
		code, out, errOut := replay(t, "--root", root, "--session", session)
		if code != 0 || !strings.Contains(out, "tool_use=hooked-call") || !strings.Contains(out, "notes.txt") || strings.Contains(out, "unlaunched-call") {
			t.Fatalf("replay --session %s code = %d, stderr = %s, stdout = %s", session, code, errOut, out)
		}
		if !strings.Contains(out, "session=claude-1 launch=launch-1") {
			t.Fatalf("hook events should show their launch:\n%s", out)
		}
	}
	code, out, _ := replay(t, "--root", root, "--session", "claude-2")
	if code != 0 || !strings.Contains(out, "tool_use=unlaunched-call") || strings.Contains(out, "notes.txt") || strings.Contains(out, "hooked-call") {
		t.Fatalf("unlaunched session replay = %s", out)
	}
}
