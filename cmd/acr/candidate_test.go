package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/distill"
	"github.com/agent-contract-runtime/acr/internal/project"
)

func TestCandidateProposeAcceptStaysDraft(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "schema.go"), []byte("x"), 0o600); err != nil {
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
	for _, line := range []string{"12", "57"} {
		failure, _ := json.Marshal(map[string]any{
			"session_id": "s1", "cwd": root, "hook_event_name": "PostToolUseFailure", "tool_name": "Edit", "tool_use_id": "call-" + line,
			"tool_input": map[string]string{"file_path": filepath.Join(root, "schema.go")},
			"error":      "schema.go:" + line + ": generated file is overwritten by go generate",
		})
		if code := run([]string{"hook", "claude", "post-tool-use-failure", "--event-db", paths.EventDB, "--project-id", config.ProjectID}, bytes.NewReader(failure), &stdout, &stderr); code != 0 {
			t.Fatal(stderr.String())
		}
	}

	stdout.Reset()
	if code := run([]string{"candidate", "propose", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("propose code = %d, stderr = %s", code, stderr.String())
	}
	var proposed []distill.Candidate
	if err := json.Unmarshal(stdout.Bytes(), &proposed); err != nil || len(proposed) != 1 {
		t.Fatalf("proposed = %s, %v", stdout.String(), err)
	}
	id := proposed[0].CandidateID

	stdout.Reset()
	if code := run([]string{"candidate", "accept", id, "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 ||
		!strings.Contains(stdout.String(), "preview only") || !strings.Contains(stdout.String(), `"passed": true`) || !strings.Contains(stdout.String(), "assumptions") {
		t.Fatalf("accept preview code = %d, stdout = %s", code, stdout.String())
	}
	contractPath := filepath.Join(paths.Contracts, proposed[0].Contract.Metadata.ID+".json")
	if _, err := os.Stat(contractPath); !os.IsNotExist(err) {
		t.Fatal("preview wrote the contract")
	}
	if code := run([]string{"candidate", "accept", id, "--root", root, "--apply"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("accept code = %d, stderr = %s", code, stderr.String())
	}
	data, err := os.ReadFile(contractPath)
	if err != nil || !strings.Contains(string(data), `"status": "draft"`) {
		t.Fatalf("draft contract = %s, %v", data, err)
	}

	stdout.Reset()
	casesPath := filepath.Join(paths.ACRDirectory, "candidates", proposed[0].Contract.Metadata.ID+".cases.json")
	if code := run([]string{"contract", "simulate", contractPath, "--cases", casesPath}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("simulate code = %d, stdout = %s, stderr = %s", code, stdout.String(), stderr.String())
	}

	// The accepted draft is not enforced: the same edit gets no advice.
	edit, _ := json.Marshal(map[string]any{
		"session_id": "s2", "cwd": root, "hook_event_name": "PreToolUse", "tool_name": "Edit", "tool_use_id": "call-x",
		"tool_input": map[string]string{"file_path": filepath.Join(root, "schema.go")},
	})
	stdout.Reset()
	if code := run([]string{"hook", "claude", "pre-tool-use", "--contracts", paths.Contracts}, bytes.NewReader(edit), &stdout, &stderr); code != 0 || strings.Contains(stdout.String(), "failed 2 times") {
		t.Fatalf("draft contract affected the runtime: %s", stdout.String())
	}

	stdout.Reset()
	if code := run([]string{"candidate", "list", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 || strings.TrimSpace(stdout.String()) != "[]" {
		t.Fatalf("open candidates after accept = %s", stdout.String())
	}
	if code := run([]string{"candidate", "accept", id, "--root", root, "--apply"}, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Fatal("a candidate was accepted twice")
	}
}
