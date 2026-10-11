package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/project"
)

// A launched session's boundary is recorded, and file changes the agent made
// without any hook event surface as a coverage gap when it exits.
func TestLaunchRecordsSessionBoundaryAndUnhookedChanges(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(t.TempDir(), "fake-claude")
	script := "#!/bin/sh\necho \"$ACR_LAUNCH_ID unattended=$ACR_UNATTENDED $*\" > launch.txt\nexit 3\n"
	if err := os.WriteFile(agent, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	stderr.Reset()
	if code := run([]string{"launch", "--root", root, "--binary", agent, "claude"}, strings.NewReader(""), &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "acr install-hooks --agent claude") {
		t.Fatalf("launch without hooks code = %d, stderr = %s", code, stderr.String())
	}
	if code := run([]string{"install-hooks", "--agent", "claude", "--root", root, "--apply"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("install-hooks code = %d, stderr = %s", code, stderr.String())
	}
	if code := run([]string{"launch", "--root", root, "--mode", "proxy", "--binary", agent, "claude"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("unknown mode launch code = %d", code)
	}
	code := run([]string{"launch", "--root", root, "--binary", agent, "claude", "--", "--print", "hello"}, strings.NewReader(""), &stdout, &stderr)
	if code != 3 {
		t.Fatalf("launch code = %d, want the agent's exit code 3; stderr = %s", code, stderr.String())
	}
	written, err := os.ReadFile(filepath.Join(root, "launch.txt"))
	if err != nil || !strings.HasSuffix(strings.TrimSpace(string(written)), " unattended=1 --print hello") {
		t.Fatalf("agent did not run with its arguments: %q, %v", written, err)
	}

	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.List(context.Background(), config.ProjectID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	// install-hooks recorded the configuration it wrote.
	if len(events) == 0 || events[0].Type != "config.installed" {
		t.Fatalf("events = %v", events)
	}
	events = events[1:]
	types := []string{}
	for _, event := range events {
		types = append(types, event.Type)
	}
	if strings.Join(types, ",") != "runtime.launch_started,coverage.gap_detected,runtime.launch_ended" {
		t.Fatalf("events = %v", types)
	}
	launchID := events[0].EventID
	if !strings.HasPrefix(string(written), launchID+" ") {
		t.Fatalf("agent saw launch id %q, want %s", written, launchID)
	}
	if !strings.Contains(string(events[1].Payload), "launch.txt") {
		t.Fatalf("gap payload = %s", events[1].Payload)
	}
	var ended struct {
		LaunchEventID string `json:"launch_event_id"`
		ExitCode      int    `json:"exit_code"`
	}
	if err := json.Unmarshal(events[2].Payload, &ended); err != nil || ended.ExitCode != 3 || ended.LaunchEventID != launchID {
		t.Fatalf("launch end = %s, %v", events[2].Payload, err)
	}
	if events[2].Actor.ACRSessionID != launchID {
		t.Fatalf("launch end acr session = %q, want %s", events[2].Actor.ACRSessionID, launchID)
	}
	// The fake agent never ran a hook, which the launch and doctor report.
	if !strings.Contains(stderr.String(), "no claude hook reported during this session") || !strings.Contains(string(events[2].Payload), `"hook_events":0`) {
		t.Fatalf("silent hooks not reported: stderr = %s, end = %s", stderr.String(), events[2].Payload)
	}
	stdout.Reset()
	run([]string{"doctor", "--root", root}, strings.NewReader(""), &stdout, &stderr)
	if !strings.Contains(stdout.String(), "WARN hooks: the last acr launch of claude") {
		t.Fatalf("doctor = %s", stdout.String())
	}
}

// Only events the agent's hooks recorded under the launch count; the guarded
// server's events and other sessions' events do not.
func TestCountHookEvents(t *testing.T) {
	store, err := eventstore.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	appendEvent := func(eventType string, actor eventstore.Actor) eventstore.Event {
		t.Helper()
		event, err := store.Append(ctx, eventstore.AppendRequest{ProjectID: "p", OccurredAt: time.Now(), Type: eventType, Actor: actor, Payload: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	appendEvent("session.started", eventstore.Actor{Kind: "agent", NativeSessionID: "before", ACRSessionID: "old-launch"})
	launch := appendEvent("runtime.launch_started", eventstore.Actor{Kind: "runtime"})
	appendEvent("action.proposed", eventstore.Actor{Kind: "agent", NativeSessionID: launch.EventID, ACRSessionID: launch.EventID})
	appendEvent("action.proposed", eventstore.Actor{Kind: "agent", NativeSessionID: "unlaunched"})
	if count, err := countHookEvents(ctx, store, "p", launch); err != nil || count != 0 {
		t.Fatalf("count before hooks = %d, %v", count, err)
	}
	appendEvent("session.started", eventstore.Actor{Kind: "agent", NativeSessionID: "claude-1", ACRSessionID: launch.EventID})
	appendEvent("action.completed", eventstore.Actor{Kind: "agent", NativeSessionID: "claude-1", ACRSessionID: launch.EventID})
	if count, err := countHookEvents(ctx, store, "p", launch); err != nil || count != 2 {
		t.Fatalf("count = %d, %v", count, err)
	}
}

func TestUnattendedLaunchDetection(t *testing.T) {
	for _, item := range []struct {
		agent string
		args  []string
		want  bool
	}{
		{"claude", []string{"-p", "fix it"}, true},
		{"claude", []string{"--model", "x", "--print", "fix it"}, true},
		{"claude", []string{"fix -p usage"}, false},
		{"claude", []string{"--", "-p"}, false},
		{"claude", nil, false},
		{"codex", []string{"exec", "fix it"}, true},
		{"codex", []string{"fix it"}, false},
		{"codex", []string{"-p"}, false},
	} {
		if got := unattendedLaunch(item.agent, item.args); got != item.want {
			t.Errorf("unattendedLaunch(%s, %q) = %v", item.agent, item.args, got)
		}
	}
}

// doctor reports a configured hook as working only after that agent's hook
// has recorded an event; guarded tool events do not count.
func TestDoctorReportsHookActivity(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"install-hooks", "--agent", "claude", "--root", root, "--apply"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("install-hooks code = %d, stderr = %s", code, stderr.String())
	}
	doctor := func() string {
		t.Helper()
		stdout.Reset()
		if code := run([]string{"doctor", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("doctor code = %d, stderr = %s", code, stderr.String())
		}
		return stdout.String()
	}
	t.Setenv("ACR_LAUNCH_ID", "launch-1")
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write_file","arguments":{"file_path":"notes.txt","content":"x"}}}` + "\n"
	if code := run([]string{"mcp", "serve", "--root", root, "--agent", "claude"}, strings.NewReader(call), &stdout, &stderr); code != 0 {
		t.Fatalf("mcp serve code = %d, stderr = %s", code, stderr.String())
	}
	if report := doctor(); !strings.Contains(report, "WARN claude hook: configured, but live execution is not verified") {
		t.Fatalf("guarded events counted as hook activity:\n%s", report)
	}
	input, _ := json.Marshal(map[string]any{"session_id": "claude-1", "cwd": root, "hook_event_name": "PostToolUse", "tool_name": "Read",
		"tool_use_id": "read-1", "tool_input": map[string]string{"file_path": "README.md"}, "tool_response": map[string]string{"output": "ok"}})
	if code := run([]string{"hook", "claude", "post-tool-use", "--event-db", paths.EventDB, "--project-id", config.ProjectID}, bytes.NewReader(input), &stdout, &stderr); code != 0 {
		t.Fatalf("hook code = %d, stderr = %s", code, stderr.String())
	}
	report := doctor()
	if !strings.Contains(report, "OK claude hook: configured; last recorded an event at ") || !strings.Contains(report, "WARN codex hook: not found") {
		t.Fatalf("doctor after a hook event:\n%s", report)
	}
}
