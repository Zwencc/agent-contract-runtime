package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/project"
)

func preToolUse(t *testing.T, root, command string, config project.Config, paths project.Paths) (string, string) {
	t.Helper()
	input, _ := json.Marshal(map[string]any{
		"session_id": "s1", "cwd": filepath.Join(root, "src"), "hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_use_id": "call",
		"tool_input": map[string]string{"command": command},
	})
	var stdout, stderr bytes.Buffer
	// Without --root, the project root is found from the agent's cwd.
	code := run([]string{"hook", "claude", "pre-tool-use", "--contracts", paths.Contracts, "--event-db", paths.EventDB, "--project-id", config.ProjectID},
		bytes.NewReader(input), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("pre-tool-use code = %d, stderr = %s", code, stderr.String())
	}
	var output struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("hook output %q: %v", stdout.String(), err)
	}
	return output.HookSpecificOutput.PermissionDecision, output.HookSpecificOutput.PermissionDecisionReason
}

// The prerequisite loop of the practical runtime: a deploy is blocked until
// the unit tests have passed on the current inputs, the agent runs the
// named repair plan, and the retried deploy goes through.
func TestDeployRequiresCurrentTestEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "app.go"), []byte("package app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}

	plan, _ := json.Marshal(map[string]any{
		"schema_version": "acr.repair-plan/v1alpha1", "id": "checks", "idempotency_key": "checks-v1", "resource_keys": []string{"tests"},
		"repeatable": true,
		"steps": []map[string]any{{"id": "unit", "argv": []string{"/bin/true"}, "timeout_seconds": 10, "verification": map[string]any{"inputs": []string{"src/**"}}}},
	})
	planPath := filepath.Join(paths.ACRDirectory, "checks-plan.json")
	if err := os.WriteFile(planPath, plan, 0o600); err != nil {
		t.Fatal(err)
	}
	remedy := "acr repair run --plan " + planPath + " --apply"
	contractJSON, _ := json.Marshal(map[string]any{
		"apiVersion": "acr.dev/v1alpha1", "kind": "Contract",
		"metadata":      map[string]any{"id": "tests-before-deploy", "version": 1, "title": "Tests before deploy", "status": "active", "severity": "high", "priority": 900},
		"scope":         map[string]any{"tool_names": []string{"Bash"}},
		"applicability": map[string]any{"all": []map[string]any{{"field": "input.command", "operator": "contains", "value": "deploy.sh"}}},
		"behavior":      map[string]any{"instruction": "Deploy only after the unit tests passed on the current sources", "enforcement": "hard"},
		"correction":    map[string]any{"mode": "none"},
		"verifiers":     map[string]any{"evidence": []map[string]string{{"subject": "repair:checks/unit", "remedy": remedy}}},
		"failure_mode":  "closed",
	})
	if err := os.WriteFile(filepath.Join(paths.Contracts, "tests-before-deploy.json"), contractJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	decision, reason := preToolUse(t, root, "./deploy.sh staging", config, paths)
	if decision != "deny" || !strings.Contains(reason, "repair:checks/unit is missing") || !strings.Contains(reason, "run: "+remedy) {
		t.Fatalf("deploy without evidence = %s: %s", decision, reason)
	}
	if decision, _ := preToolUse(t, root, "go build ./...", config, paths); decision != "" {
		t.Fatalf("unrelated command decision = %s", decision)
	}

	runPlan := func() {
		t.Helper()
		if code := run([]string{"repair", "run", "--root", root, "--plan", planPath, "--apply"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("repair code = %d, stderr = %s", code, stderr.String())
		}
	}
	runPlan()
	if decision, reason := preToolUse(t, root, "./deploy.sh staging", config, paths); decision != "" {
		t.Fatalf("deploy with fresh evidence = %s: %s", decision, reason)
	}

	// Editing a source makes the evidence stale immediately, even before the
	// next SessionStart records the invalidation.
	if err := os.WriteFile(filepath.Join(root, "src", "app.go"), []byte("package app\n\nvar edited = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if decision, reason := preToolUse(t, root, "./deploy.sh staging", config, paths); decision != "deny" || !strings.Contains(reason, "stale") {
		t.Fatalf("deploy with stale evidence = %s: %s", decision, reason)
	}
	runPlan()
	if decision, reason := preToolUse(t, root, "./deploy.sh staging", config, paths); decision != "" {
		t.Fatalf("deploy after re-verification = %s: %s", decision, reason)
	}

	// The whole transaction history is replayable by its idempotency key.
	stdout.Reset()
	if code := run([]string{"replay", "--root", root, "--correlation", "checks-v1", "--format", "json"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("replay code = %d, stderr = %s", code, stderr.String())
	}
	var entries []replayEntry
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil || len(entries) != 8 {
		t.Fatalf("replayed %d repair events, want 8 (two runs of started, step_started, step_completed, completed): %v", len(entries), err)
	}
}

func TestRepeatablePlanMustOnlyVerify(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	plan := writeRepairPlan(t, "deploy", []map[string]any{
		{"id": "test", "argv": []string{"/bin/true"}, "timeout_seconds": 10, "verification": map[string]any{}},
		{"id": "deploy", "argv": []string{"/bin/true"}, "timeout_seconds": 10},
	})
	data, _ := os.ReadFile(plan)
	data = bytes.Replace(data, []byte(`"id":"deploy"`), []byte(`"id":"deploy","repeatable":true`), 1)
	os.WriteFile(plan, data, 0o600)
	if code := run([]string{"repair", "run", "--root", root, "--plan", plan}, strings.NewReader(""), &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "must be a verification step") {
		t.Fatalf("repeatable plan with side effects code = %d, stderr = %s", code, stderr.String())
	}
}
