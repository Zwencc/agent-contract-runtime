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
	"github.com/agent-contract-runtime/acr/internal/repair"
)

func TestRepairCLIPreviewsThenExecutesPlanOnce(t *testing.T) {
	root := t.TempDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	planPath := filepath.Join(root, "repair.json")
	plan := `{
  "schema_version":"acr.repair-plan/v1alpha1",
  "id":"test-before-deploy",
  "idempotency_key":"test-before-deploy-v1",
  "resource_keys":["deployment:staging"],
  "steps":[
    {"id":"test","argv":["/bin/true"],"timeout_seconds":10},
    {"id":"deploy","argv":["/bin/true"],"timeout_seconds":10}
  ]
}`
	if err := os.WriteFile(planPath, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	_, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"repair", "run", "--root", root, "--plan", planPath}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("preview code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "preview only") {
		t.Fatalf("preview output = %s", stdout.String())
	}
	if _, err := os.Stat(paths.EventDB); !os.IsNotExist(err) {
		t.Fatalf("preview created event database: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"repair", "run", "--root", root, "--plan", planPath, "--apply"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("apply code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"completed_steps": 2`) {
		t.Fatalf("apply output = %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"repair", "run", "--root", root, "--plan", planPath, "--apply"}, strings.NewReader(""), &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "already completed") {
		t.Fatalf("replay code = %d, stderr = %s", code, stderr.String())
	}
}

func TestRepairStatusResolveAndResumeCLI(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	planPath := writeRepairPlan(t, "release", []map[string]any{
		{"id": "build", "argv": []string{"/bin/true"}, "timeout_seconds": 10},
		{"id": "publish", "argv": []string{"/bin/true"}, "timeout_seconds": 10},
	})
	file, _ := os.Open(planPath)
	plan, err := repair.Decode(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := plan.Hash()

	// The process died while "build" was running.
	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		kind    string
		payload map[string]any
	}{
		{"repair.started", map[string]any{"plan_hash": hash}},
		{"repair.step_started", map[string]any{"step_id": "build"}},
	} {
		event.payload["plan_id"], event.payload["idempotency_key"] = plan.ID, plan.IdempotencyKey
		encoded, _ := json.Marshal(event.payload)
		if _, err := store.Append(context.Background(), eventstore.AppendRequest{ProjectID: config.ProjectID, OccurredAt: time.Now(), Type: event.kind, Actor: eventstore.Actor{Kind: "runtime"}, Payload: encoded}); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()

	status := func() string {
		t.Helper()
		stdout.Reset()
		if code := run([]string{"repair", "status", "--root", root, "--plan", planPath}, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("status code = %d, stderr = %s", code, stderr.String())
		}
		return stdout.String()
	}
	if got := status(); !strings.Contains(got, `"run": "in_progress"`) || !strings.Contains(got, `"state": "unknown"`) {
		t.Fatalf("status = %s", got)
	}
	stderr.Reset()
	if code := run([]string{"repair", "run", "--root", root, "--plan", planPath, "--apply"}, strings.NewReader(""), &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "acr repair resolve") {
		t.Fatalf("run before resolve code = %d, stderr = %s", code, stderr.String())
	}

	resolve := []string{"repair", "resolve", "--root", root, "--plan", planPath, "--step", "build", "--outcome", "succeeded", "--reason", "artifact exists in the registry"}
	stdout.Reset()
	if code := run(resolve, strings.NewReader(""), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "preview only") {
		t.Fatalf("resolve preview code = %d, stdout = %s", code, stdout.String())
	}
	if got := status(); !strings.Contains(got, `"state": "unknown"`) {
		t.Fatalf("preview changed state: %s", got)
	}
	if code := run(append(resolve, "--apply"), strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("resolve code = %d, stderr = %s", code, stderr.String())
	}
	stdout.Reset()
	if code := run([]string{"repair", "run", "--root", root, "--plan", planPath, "--apply"}, strings.NewReader(""), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"completed_steps": 2`) {
		t.Fatalf("resumed run code = %d, stdout = %s, stderr = %s", code, stdout.String(), stderr.String())
	}
	if got := status(); !strings.Contains(got, `"run": "completed"`) {
		t.Fatalf("final status = %s", got)
	}
}
