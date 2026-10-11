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

// install-hooks registers ACR's memory and rule tools for both agents next to
// the user's own MCP servers, does it once, and accounts for the files it
// wrote so they are not reported as unexplained changes.
func TestInstallHooksRegistersDirectiveTools(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{"mcpServers": {"docs": {"command": "docs-server"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".codex", "config.toml"), []byte("model = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	install := func() string {
		t.Helper()
		stdout.Reset()
		if code := run([]string{"install-hooks", "--agent", "all", "--root", root, "--binary", "/opt/acr/bin/acr", "--apply"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("install-hooks code = %d, stderr = %s", code, stderr.String())
		}
		return stdout.String()
	}
	if out := install(); !strings.Contains(out, "approve the project MCP server acr") {
		t.Fatalf("install output = %s", out)
	}

	var claude struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	data, _ := os.ReadFile(filepath.Join(root, ".mcp.json"))
	if err := json.Unmarshal(data, &claude); err != nil || claude.MCPServers["docs"].Command != "docs-server" ||
		claude.MCPServers["acr"].Command != "/opt/acr/bin/acr" || strings.Join(claude.MCPServers["acr"].Args, " ") != "mcp serve --root "+root+" --agent claude --toolset directives" {
		t.Fatalf(".mcp.json = %s, %v", data, err)
	}
	codex, _ := os.ReadFile(filepath.Join(root, ".codex", "config.toml"))
	if !strings.HasPrefix(string(codex), "model = \"x\"\n\n[mcp_servers.acr]\n") || !strings.Contains(string(codex), `"--toolset", "directives"]`) || !strings.Contains(string(codex), `default_tools_approval_mode = "approve"`) {
		t.Fatalf("config.toml = %s", codex)
	}

	if out := install(); strings.Contains("\n"+out, "\ninstalled ") {
		t.Fatalf("second install changed files: %s", out)
	}
	if again, _ := os.ReadFile(filepath.Join(root, ".codex", "config.toml")); string(again) != string(codex) {
		t.Fatalf("config.toml rewritten: %s", again)
	}

	config, paths, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := reconcileWorkspaceCoverage(context.Background(), store, paths.Root, config.ProjectID, config.WorkspaceID, protocol.AgentClaudeCode); err != nil {
		t.Fatal(err)
	}
	events, err := store.List(context.Background(), config.ProjectID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "coverage.gap_detected" {
			t.Fatalf("ACR's own configuration reported as a gap: %s", event.Payload)
		}
	}
}
