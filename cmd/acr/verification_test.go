package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/project"
	"github.com/agent-contract-runtime/acr/internal/verification"
)

func writeRepairPlan(t *testing.T, id string, steps []map[string]any) string {
	t.Helper()
	plan, err := json.Marshal(map[string]any{
		"schema_version": "acr.repair-plan/v1alpha1", "id": id, "idempotency_key": id + "-v1",
		"resource_keys": []string{"workspace:" + id}, "steps": steps,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), id+".json")
	if err := os.WriteFile(path, plan, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func sessionStart(t *testing.T, root string, config project.Config, paths project.Paths) string {
	t.Helper()
	input, _ := json.Marshal(map[string]string{"session_id": "s1", "cwd": root, "hook_event_name": "SessionStart", "source": "startup"})
	var stdout, stderr bytes.Buffer
	code := run([]string{"hook", "codex", "session-start", "--contracts", paths.Contracts, "--event-db", paths.EventDB,
		"--project-id", config.ProjectID, "--workspace-id", config.WorkspaceID, "--root", root}, bytes.NewReader(input), &stdout, &stderr)
	if code != 0 || strings.Contains(stderr.String(), "warning") {
		t.Fatalf("session-start code = %d, stderr = %s", code, stderr.String())
	}
	return stdout.String()
}

func verificationStates(t *testing.T, root string) map[string]verification.State {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"verification", "list", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("verification list code = %d, stderr = %s", code, stderr.String())
	}
	var statuses []verification.Status
	if err := json.Unmarshal(stdout.Bytes(), &statuses); err != nil {
		t.Fatal(err)
	}
	states := map[string]verification.State{}
	for _, status := range statuses {
		states[status.Subject] = status.State
	}
	return states
}

// Scenario C of the development plan: a test that passed on input digest X
// must not be reused once its inputs change to Y, while checks over
// untouched inputs stay valid.
func TestVerificationInvalidatedWhenInputsChange(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{"src/a.go": "package a\n", "docs/guide.md": "# guide\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o600); err != nil {
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

	checks := writeRepairPlan(t, "checks", []map[string]any{
		{"id": "unit", "argv": []string{"/bin/true"}, "timeout_seconds": 10, "verification": map[string]any{"inputs": []string{"src/**"}}},
		{"id": "docs", "argv": []string{"/bin/true"}, "timeout_seconds": 10, "verification": map[string]any{"inputs": []string{"docs/**"}}},
	})
	if code := run([]string{"repair", "run", "--root", root, "--plan", checks, "--apply"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("repair code = %d, stderr = %s", code, stderr.String())
	}
	context := sessionStart(t, root, config, paths)
	if states := verificationStates(t, root); states["repair:checks/unit"] != verification.StateValid || states["repair:checks/docs"] != verification.StateValid {
		t.Fatalf("states before change = %v", states)
	}
	if !strings.Contains(context, "[valid] repair:checks/unit passed on 1 input files") {
		t.Fatalf("context lacks valid verification: %s", context)
	}

	if err := os.WriteFile(filepath.Join(root, "src/a.go"), []byte("package a\n\nfunc Changed() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	context = sessionStart(t, root, config, paths)
	states := verificationStates(t, root)
	if states["repair:checks/unit"] != verification.StateInvalidated || states["repair:checks/docs"] != verification.StateValid {
		t.Fatalf("states after change = %v", states)
	}
	if !strings.Contains(context, "[invalidated] repair:checks/unit") || !strings.Contains(context, "[valid] repair:checks/docs") {
		t.Fatalf("context after change: %s", context)
	}

	// Failing checks and checks that rewrite their own inputs never count.
	other := writeRepairPlan(t, "others", []map[string]any{
		{"id": "regenerate", "argv": []string{"/bin/sh", "-c", "echo changed >> src/a.go"}, "timeout_seconds": 10, "verification": map[string]any{}},
		{"id": "lint", "argv": []string{"/bin/sh", "-c", "exit 4"}, "timeout_seconds": 10, "verification": map[string]any{"inputs": []string{"src/**"}}},
	})
	if code := run([]string{"repair", "run", "--root", root, "--plan", other, "--apply"}, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Fatalf("repair with failing check code = %d, stderr = %s", code, stderr.String())
	}
	states = verificationStates(t, root)
	if states["repair:others/regenerate"] != verification.StateUnstable || states["repair:others/lint"] != verification.StateFailed {
		t.Fatalf("states = %v", states)
	}
}

func TestRepairRejectsUnsafeVerificationInputs(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	plan := writeRepairPlan(t, "unsafe", []map[string]any{
		{"id": "escape", "argv": []string{"/bin/true"}, "timeout_seconds": 10, "verification": map[string]any{"inputs": []string{"../secrets/**"}}},
	})
	if code := run([]string{"repair", "run", "--root", root, "--plan", plan}, strings.NewReader(""), &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "verification input") {
		t.Fatalf("unsafe inputs code = %d, stderr = %s", code, stderr.String())
	}
}
