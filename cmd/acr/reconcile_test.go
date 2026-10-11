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
	"github.com/agent-contract-runtime/acr/internal/protocol"
)

func TestReconcileCLIClosesCoverageGapWithHumanEvidence(t *testing.T) {
	root := t.TempDir()
	tracked := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("init code = %d, stderr = %s", code, stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tracked, []byte("outside hook"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcileWorkspaceCoverage(context.Background(), store, root, config.ProjectID, config.WorkspaceID, protocol.AgentCodex); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"reconcile", "status", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("status code = %d, stderr = %s", code, stderr.String())
	}
	var gaps []eventstore.Event
	if err := json.Unmarshal(stdout.Bytes(), &gaps); err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 {
		t.Fatalf("gaps = %#v", gaps)
	}

	stdout.Reset()
	stderr.Reset()
	code := run([]string{"reconcile", "accept", "--root", root, "--event-id", gaps[0].EventID, "--resolution", "documented_external_change", "--reason", "reviewed editor change"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("accept code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"type": "coverage.gap_reconciled"`) || !strings.Contains(stdout.String(), gaps[0].EventID) {
		t.Fatalf("accept output = %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"reconcile", "status", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("second status code = %d, stderr = %s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "[]" {
		t.Fatalf("open gaps after reconciliation = %s", stdout.String())
	}
}

func TestReconcileAcceptRejectsNewWorkspaceChanges(t *testing.T) {
	root := t.TempDir()
	tracked := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tracked, []byte("first change"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcileWorkspaceCoverage(context.Background(), store, root, config.ProjectID, config.WorkspaceID, protocol.AgentCodex); err != nil {
		t.Fatal(err)
	}
	events, err := store.List(context.Background(), config.ProjectID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err := os.WriteFile(tracked, []byte("second change"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code := run([]string{"reconcile", "accept", "--root", root, "--event-id", events[0].EventID, "--resolution", "current_state_accepted", "--reason", "reviewed"}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "workspace changed again") {
		t.Fatalf("accept code = %d, stderr = %s", code, stderr.String())
	}
}
