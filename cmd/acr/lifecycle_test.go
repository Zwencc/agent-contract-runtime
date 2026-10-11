package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/project"
)

// The lifecycle inputs captured from real Claude Code and Codex sessions are
// recorded, a subagent's tool calls carry its ID, and a file written without
// any hook surfaces as a gap when the session ends.
func TestLifecycleHooksWithCapturedInputs(t *testing.T) {
	for agent, version := range map[string]string{"claude": "claude-code-2.1.303", "codex": "codex-cli-0.148.0"} {
		t.Run(agent, func(t *testing.T) {
			root := t.TempDir()
			var stdout, stderr bytes.Buffer
			if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
				t.Fatal(stderr.String())
			}
			config, paths, err := project.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			fixture := func(name string) []byte {
				t.Helper()
				data, err := os.ReadFile(filepath.Join("..", "..", "internal", "adapter", "testdata", "live", version, name+".json"))
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			hook := func(command string, data []byte, extra ...string) string {
				t.Helper()
				stdout.Reset()
				args := append([]string{"hook", agent, command, "--event-db", paths.EventDB, "--project-id", config.ProjectID}, extra...)
				if code := run(args, bytes.NewReader(data), &stdout, &stderr); code != 0 {
					t.Fatalf("%s code = %d, stderr = %s", command, code, stderr.String())
				}
				return stdout.String()
			}
			hook("pre-tool-use", fixture("PreToolUse-subagent"), "--contracts", paths.Contracts, "--root", root)
			for _, name := range []string{"SubagentStart", "SubagentStop", "Stop"} {
				if out := hook("lifecycle", fixture(name), "--root", root); strings.TrimSpace(out) != "{}" {
					t.Fatalf("%s output = %s", name, out)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "unhooked.txt"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			hook("lifecycle", fixture("SessionEnd"), "--root", root)

			store, err := eventstore.Open(paths.EventDB)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			events, err := store.List(context.Background(), config.ProjectID, 0, 50)
			if err != nil {
				t.Fatal(err)
			}
			types := []string{}
			subagent, gap := false, false
			for _, event := range events {
				types = append(types, event.Type)
				subagent = subagent || event.Type == "action.proposed" && strings.Contains(string(event.Payload), `"subagent_id"`)
				gap = gap || event.Type == "coverage.gap_detected" && strings.Contains(string(event.Payload), "unhooked.txt")
			}
			joined := strings.Join(types, ",")
			for _, want := range []string{"subagent.started", "subagent.stopped", "turn.stopped", "session.ended"} {
				if !strings.Contains(joined, want) {
					t.Errorf("events %s lack %s", joined, want)
				}
			}
			if !subagent || !gap {
				t.Fatalf("subagent call tagged = %v, gap at session end = %v; events %s", subagent, gap, joined)
			}
		})
	}
}
