package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/objectstore"
	"github.com/agent-contract-runtime/acr/internal/project"
)

// A large tool response travels from the PostToolUse hook into the project
// object store, the event keeps only the reference, and doctor verifies it.
func TestPostToolUseHookStoresLargeResponseAsObject(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}

	response, _ := json.Marshal(map[string]string{"output": strings.Repeat("PASS\n", 10000)})
	input, _ := json.Marshal(map[string]any{
		"session_id":      "session-1",
		"cwd":             root,
		"hook_event_name": "PostToolUse",
		"tool_name":       "Bash",
		"tool_use_id":     "call-1",
		"tool_input":      map[string]string{"command": "go test ./..."},
		"tool_response":   json.RawMessage(response),
	})
	stdout.Reset()
	stderr.Reset()
	code := run([]string{"hook", "claude", "post-tool-use", "--event-db", paths.EventDB, "--project-id", config.ProjectID, "--workspace-id", config.WorkspaceID},
		bytes.NewReader(input), &stdout, &stderr)
	if code != 0 || strings.TrimSpace(stdout.String()) != "{}" {
		t.Fatalf("hook code = %d, stdout = %s, stderr = %s", code, stdout.String(), stderr.String())
	}

	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.List(context.Background(), config.ProjectID, 0, 10)
	store.Close()
	if err != nil || len(events) != 1 || events[0].Type != "action.completed" {
		t.Fatalf("events = %#v, %v", events, err)
	}
	var payload struct {
		ToolResponseRef *objectstore.Ref `json:"tool_response_ref"`
	}
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil || payload.ToolResponseRef == nil {
		t.Fatalf("payload = %s, %v", events[0].Payload, err)
	}
	if len(events[0].Payload) > 1024 {
		t.Fatalf("event payload is %d bytes", len(events[0].Payload))
	}
	objects, err := objectstore.Open(paths.Objects)
	if err != nil {
		t.Fatal(err)
	}
	if stored, err := objects.Get(*payload.ToolResponseRef); err != nil || !bytes.Contains(stored, []byte("PASS")) {
		t.Fatalf("stored response missing: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	run([]string{"doctor", "--root", root}, strings.NewReader(""), &stdout, &stderr)
	if !strings.Contains(stdout.String(), "OK objects: 1 verified") {
		t.Fatalf("doctor output = %s, stderr = %s", stdout.String(), stderr.String())
	}
}
