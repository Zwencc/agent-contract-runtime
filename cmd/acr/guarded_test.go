package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Guarded launch removes Claude Code's workspace-changing tools and points it
// at ACR's MCP server; the agent's own arguments still come last.
func TestGuardedLaunchRoutesClaudeThroughACR(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	if code := run([]string{"install-hooks", "--agent", "claude", "--root", root, "--apply"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	argsFile := filepath.Join(t.TempDir(), "args.json")
	agent := filepath.Join(t.TempDir(), "fake-claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	if err := os.WriteFile(agent, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"launch", "--root", root, "--mode", "guarded", "--binary", agent, "claude", "--", "-p", "fix the build"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("guarded launch code = %d, stderr = %s", code, stderr.String())
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if strings.Join(args[:8], " ") != "--tools Read,Glob,Grep,ToolSearch,WebFetch,WebSearch --strict-mcp-config --disallowedTools Bash Edit Write NotebookEdit" ||
		args[8] != "--mcp-config" || args[10] != "--append-system-prompt" {
		t.Fatalf("guarded args = %q", args)
	}
	if strings.Join(args[len(args)-2:], " ") != "-p fix the build" {
		t.Fatalf("agent arguments not last: %q", args)
	}
	var config struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(args[9]), &config); err != nil {
		t.Fatalf("mcp config %q: %v", args[9], err)
	}
	server := config.MCPServers["acr"]
	self, _ := os.Executable()
	if server.Command != self || strings.Join(server.Args[:2], " ") != "mcp serve" || server.Env["ACR_LAUNCH_ID"] == "" {
		t.Fatalf("mcp server = %+v", server)
	}
}

func TestGuardedLaunchRoutesCodexThroughACR(t *testing.T) {
	certifyCodexGuarded(t)
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	if code := run([]string{"install-hooks", "--agent", "codex", "--root", root, "--apply"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	argsFile := filepath.Join(t.TempDir(), "args.json")
	agent := filepath.Join(t.TempDir(), "fake-codex")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'codex-cli 0.148.0'; exit 0; fi\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	if err := os.WriteFile(agent, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"launch", "--root", root, "--mode", "guarded", "--binary", agent, "codex", "--", "--model", "test-model", "fix the build"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("guarded launch code = %d, stderr = %s", code, stderr.String())
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if args[0] != "--strict-config" || strings.Join(args[len(args)-3:], " ") != "--model test-model fix the build" {
		t.Fatalf("codex guarded arguments or ordering = %q", args)
	}
	for _, feature := range codexGuardedDisabledFeatures {
		if !hasArgumentPair(args, "--disable", feature) {
			t.Errorf("feature %q was not disabled: %q", feature, args)
		}
	}
	if hasArgumentPair(args, "--disable", "code_mode_host") {
		t.Fatal("code_mode_host must remain enabled so Codex can call ACR MCP tools")
	}
	if !hasArgumentPair(args, "--disable", "code_mode") {
		t.Fatal("code_mode must remain disabled; only its isolated host is required")
	}
	if !hasArgumentPair(args, "--disable", "shell_tool") {
		t.Fatal("shell_tool is the primary native execution boundary")
	}
	if !hasArgumentPair(args, "--sandbox", "read-only") || !hasArgumentPair(args, "--ask-for-approval", "never") {
		t.Fatalf("read-only fail-closed boundary missing: %q", args)
	}
	configs := argumentValues(args, "-c")
	if len(configs) < 7 || configs[0] != "mcp_servers={}" {
		t.Fatalf("MCP reset must be the first config override: %q", configs)
	}
	joined := strings.Join(configs, "\n")
	self, _ := os.Executable()
	for _, required := range []string{
		"mcp_servers.acr.command=" + strconv.Quote(self),
		`mcp_servers.acr.enabled_tools=["exec","write_file","edit_file","remember",`,
		"mcp_servers.acr.required=true", `mcp_servers.acr.default_tools_approval_mode="approve"`, "developer_instructions=",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("missing Codex config %q in %q", required, configs)
		}
	}
	if !strings.Contains(joined, `"mcp","serve","--root","`+root+`","--agent","codex"`) {
		t.Errorf("MCP server arguments = %q", configs)
	}
	unsupported := filepath.Join(t.TempDir(), "unsupported-codex")
	if err := os.WriteFile(unsupported, []byte("#!/bin/sh\necho 'codex-cli 0.149.0'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := run([]string{"launch", "--root", root, "--mode", "guarded", "--binary", unsupported, "codex"}, strings.NewReader(""), &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "certified only") {
		t.Fatalf("unsupported Codex version code = %d, stderr = %q", code, stderr.String())
	}
}

func TestGuardedLaunchRejectsCodexBoundaryOverrides(t *testing.T) {
	certifyCodexGuarded(t)
	for _, args := range [][]string{
		{"--sandbox", "danger-full-access"},
		{"--config", "features.shell_tool=true"},
		{"--enable=computer_use"},
		{"--ask-for-approval", "on-request"},
		{"-sdanger-full-access"},
		{"--full-auto"},
		{"apply", "a.patch"},
		{"--no-alt-screen", "apply", "a.patch"},
		{"exec", "--sandbox", "workspace-write", "task"},
		{"exec", "--config=features.shell_tool=true", "task"},
	} {
		var stdout, stderr bytes.Buffer
		launchArgs := append([]string{"launch", "--mode", "guarded", "codex", "--"}, args...)
		if code := run(launchArgs, strings.NewReader(""), &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "guarded") {
			t.Errorf("args %q: code = %d, stderr = %q", args, code, stderr.String())
		}
	}
}

func TestCodexGuardedAcceptsValidatedExec(t *testing.T) {
	certifyCodexGuarded(t)
	want := []string{"exec", "--skip-git-repo-check", "--json", "fix the build"}
	got, err := validateCodexGuardedAgentArgs(want)
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("validated exec = %q, %v; want %q", got, err, want)
	}
}

func TestCodexGuardedTreatsSingleCommandWordAsPrompt(t *testing.T) {
	certifyCodexGuarded(t)
	for _, test := range []struct {
		args []string
		want []string
	}{
		{args: []string{"review"}, want: []string{"--", "review"}},
		{args: []string{"--model", "test-model", "help"}, want: []string{"--model", "test-model", "--", "help"}},
	} {
		got, err := validateCodexGuardedAgentArgs(test.args)
		if err != nil || !slices.Equal(got, test.want) {
			t.Errorf("args %q: got %q, err %v; want %q", test.args, got, err, test.want)
		}
	}
}

func hasArgumentPair(args []string, name, value string) bool {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name && args[index+1] == value {
			return true
		}
	}
	return false
}

func argumentValues(args []string, name string) []string {
	values := []string{}
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			values = append(values, args[index+1])
			index++
		}
	}
	return values
}

func TestMCPServeRunsGuardedCalls(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{"elicitation":{"form":{},"url":{}}},"clientInfo":{"name":"codex-mcp-client","version":"0.148.0"}}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"write_file","arguments":{"file_path":"notes.txt","content":"guarded\n"}}}` + "\n"
	stdout.Reset()
	if code := run([]string{"mcp", "serve", "--root", root, "--agent", "codex"}, strings.NewReader(input), &stdout, &stderr); code != 0 {
		t.Fatalf("mcp serve code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"protocolVersion":"2025-06-18"`) || !strings.Contains(stdout.String(), `"name":"exec"`) || !strings.Contains(stdout.String(), `"name":"write_file"`) || !strings.Contains(stdout.String(), `"name":"edit_file"`) || !strings.Contains(stdout.String(), `"isError":false`) || !strings.Contains(stdout.String(), "wrote 8 bytes") {
		t.Fatalf("mcp output = %s", stdout.String())
	}
	if data, err := os.ReadFile(filepath.Join(root, "notes.txt")); err != nil || string(data) != "guarded\n" {
		t.Fatalf("notes.txt = %q, %v", data, err)
	}
	if code := run([]string{"mcp", "serve", "--agent", "other", "--root", root}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("bad agent code = %d", code)
	}
}

// certifyCodexGuarded keeps argument-focused tests independent of the gate
// while preserving its prior value.
func certifyCodexGuarded(t *testing.T) {
	t.Helper()
	previous := codexGuardedCertified
	codexGuardedCertified = true
	t.Cleanup(func() { codexGuardedCertified = previous })
}

func TestCodexGuardedCertificationBoundary(t *testing.T) {
	if !codexGuardedCertified {
		t.Fatal("Codex guarded must stay enabled after the 0.148.0 certification")
	}
	if slices.Contains(codexGuardedDisabledFeatures, "code_mode_host") {
		t.Fatal("code_mode_host must stay enabled for ACR MCP calls")
	}
	if !slices.Contains(codexGuardedDisabledFeatures, "code_mode") {
		t.Fatal("code_mode must stay disabled")
	}
}

// With the gate closed, a Codex guarded launch is refused before the binary
// runs, so a later uncertified build cannot be started by mistake.
func TestCodexGuardedIsRefusedWhenGateClosed(t *testing.T) {
	previous := codexGuardedCertified
	codexGuardedCertified = false
	t.Cleanup(func() { codexGuardedCertified = previous })
	marker := filepath.Join(t.TempDir(), "ran")
	fake := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ntouch "+marker+"\necho 'codex-cli 0.148.0'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"launch", "--root", t.TempDir(), "--mode", "guarded", "--binary", fake, "codex"}, strings.NewReader(""), &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "not available") {
		t.Fatalf("closed gate code = %d, stderr = %q", code, stderr.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("the Codex binary ran while the gate was closed")
	}
}
