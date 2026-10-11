package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/coverage"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/project"
	"github.com/agent-contract-runtime/acr/internal/protocol"
)

// Changes made by an applied repair plan are explained by its recorded step
// outcomes, while unrelated manual edits still surface as coverage gaps.
func TestReconcileWorkspaceCoverageAcceptsRepairOutcome(t *testing.T) {
	cases := map[string]struct {
		script    string
		wantApply int
	}{
		"completed step": {script: "echo after > tracked.txt", wantApply: 0},
		"failed step":    {script: "echo after > tracked.txt; exit 3", wantApply: 1},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			for _, file := range []string{"tracked.txt", "manual.txt"} {
				if err := os.WriteFile(filepath.Join(root, file), []byte("before\n"), 0o600); err != nil {
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

			plan, err := json.Marshal(map[string]any{
				"schema_version":  "acr.repair-plan/v1alpha1",
				"id":              "regenerate",
				"idempotency_key": "regenerate-v1",
				"resource_keys":   []string{"workspace:tracked"},
				"steps": []map[string]any{
					{"id": "rewrite", "argv": []string{"/bin/sh", "-c", test.script}, "timeout_seconds": 10},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			planPath := filepath.Join(t.TempDir(), "plan.json")
			if err := os.WriteFile(planPath, plan, 0o600); err != nil {
				t.Fatal(err)
			}
			if code := run([]string{"repair", "run", "--root", root, "--plan", planPath, "--apply"}, strings.NewReader(""), &stdout, &stderr); code != test.wantApply {
				t.Fatalf("repair apply code = %d, stderr = %s", code, stderr.String())
			}
			if err := os.WriteFile(filepath.Join(root, "manual.txt"), []byte("edited by hand\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			store, err := eventstore.Open(paths.EventDB)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := reconcileWorkspaceCoverage(context.Background(), store, root, config.ProjectID, config.WorkspaceID, protocol.AgentCodex); err != nil {
				t.Fatal(err)
			}
			events, err := store.List(context.Background(), config.ProjectID, 0, 50)
			if err != nil {
				t.Fatal(err)
			}
			last := events[len(events)-1]
			if last.Type != "coverage.gap_detected" {
				t.Fatalf("last event = %s, want coverage.gap_detected", last.Type)
			}
			var gap struct {
				ObservedChanges []coverage.Change `json:"observed_changes"`
			}
			if err := json.Unmarshal(last.Payload, &gap); err != nil {
				t.Fatal(err)
			}
			if len(gap.ObservedChanges) != 1 || gap.ObservedChanges[0].Path != "manual.txt" {
				t.Fatalf("gap observed %+v, want only manual.txt", gap.ObservedChanges)
			}
		})
	}
}
