package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/memory"
)

func TestMemoryCLIAddActivateAndRetract(t *testing.T) {
	root := t.TempDir()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"init", "--root", root, "--name", "test"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("init code = %d, stderr = %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code := run([]string{
		"memory", "add", "--root", root, "--kind", "user_correction", "--priority", "P0",
		"--claim", "Do not modify production data", "--evidence", "human:test", "--scope", `{}`,
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("add code = %d, stderr = %s", code, stderr.String())
	}
	var created memory.Memory
	if err := json.Unmarshal(stdout.Bytes(), &created); err != nil {
		t.Fatalf("decode created memory: %v", err)
	}
	if created.Status != memory.StatusCandidate || created.Priority != memory.P0 {
		t.Fatalf("created = %#v", created)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"memory", "activate", "--root", root, "--reason", "reviewed", created.MemoryID}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("activate code = %d, stderr = %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"memory", "list", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("list code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Do not modify production data") {
		t.Fatalf("active memory missing from list: %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"memory", "retract", "--root", root, "--reason", "incorrect", created.MemoryID}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("retract code = %d, stderr = %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"memory", "list", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("second list code = %d, stderr = %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "Do not modify production data") {
		t.Fatalf("retracted memory remains retrievable: %s", stdout.String())
	}
}
