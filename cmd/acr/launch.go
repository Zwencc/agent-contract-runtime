package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/agent-contract-runtime/acr/internal/directive"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/guarded"
	"github.com/agent-contract-runtime/acr/internal/project"
	"github.com/agent-contract-runtime/acr/internal/protocol"
)

// runLaunch starts an agent session whose process boundary ACR records.
// In native mode tool coverage depends on the agent's hooks; reconciling the
// workspace before and after the session reveals changes the hooks missed.
// In guarded mode the native workspace-changing tools are removed or confined
// to a read-only sandbox and the agent works through ACR's MCP tools, which ACR
// evaluates itself before running.
func runLaunch(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("launch", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	mode := flags.String("mode", "native", "launch mode: native or guarded")
	binary := flags.String("binary", "", "agent executable; defaults to claude or codex on PATH")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *mode != "native" && *mode != "guarded" {
		fmt.Fprintf(stderr, "launch mode %q is not available; use native or guarded\n", *mode)
		return 2
	}
	if flags.NArg() < 1 || (flags.Arg(0) != "claude" && flags.Arg(0) != "codex") {
		fmt.Fprintln(stderr, "launch requires an agent: claude or codex, optionally followed by -- and agent arguments")
		return 2
	}
	agent := flags.Arg(0)
	agentArgs := flags.Args()[1:]
	if len(agentArgs) > 0 && agentArgs[0] == "--" {
		agentArgs = agentArgs[1:]
	}
	if *mode == "guarded" && agent == "codex" && !codexGuardedCertified {
		fmt.Fprintln(stderr, codexGuardedUnavailable)
		return 2
	}
	if *mode == "guarded" && agent == "codex" {
		var err error
		agentArgs, err = validateCodexGuardedAgentArgs(agentArgs)
		if err != nil {
			fmt.Fprintf(stderr, "codex guarded arguments: %v\n", err)
			return 2
		}
	}
	kind := protocol.AgentCodex
	hookFile := filepath.Join(".codex", "hooks.json")
	if agent == "claude" {
		kind = protocol.AgentClaudeCode
		hookFile = filepath.Join(".claude", "settings.json")
	}

	config, paths, err := project.Load(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !hookFileContains(filepath.Join(paths.Root, hookFile), agent) {
		fmt.Fprintf(stderr, "%s hooks are not installed; run: acr install-hooks --agent %s --root %s --apply\n", agent, agent, paths.Root)
		return 1
	}
	executable := *binary
	if executable == "" {
		executable = agent
	}
	executable, err = exec.LookPath(executable)
	if err != nil {
		fmt.Fprintf(stderr, "find %s executable: %v\n", agent, err)
		return 1
	}
	if *mode == "guarded" && agent == "codex" {
		output, err := exec.Command(executable, "--version").Output()
		if err != nil {
			fmt.Fprintf(stderr, "check Codex guarded capability version: %v\n", err)
			return 1
		}
		if version := strings.TrimSpace(string(output)); version != "codex-cli 0.148.0" {
			fmt.Fprintf(stderr, "Codex guarded capability is certified only for codex-cli 0.148.0; found %q\n", version)
			return 1
		}
	}

	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer store.Close()
	ctx := context.Background()
	if os.Getenv("ACR_DAEMON") != "off" {
		// Hooks also work without it, only slower.
		if _, _, err := ensureDaemon(); err != nil {
			fmt.Fprintf(stderr, "ACR daemon unavailable; hooks will run directly: %v\n", err)
		}
	}
	if err := reconcileWorkspaceCoverage(ctx, store, paths.Root, config.ProjectID, config.WorkspaceID, kind); err != nil {
		fmt.Fprintf(stderr, "ACR coverage warning before launch: %v\n", err)
	}
	// Events after launch_started carry its ID, which is also the
	// ACR_LAUNCH_ID that the hooks and the guarded server record under.
	launchID := ""
	record := func(eventType string, payload map[string]any) (eventstore.Event, error) {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return eventstore.Event{}, err
		}
		return store.Append(ctx, eventstore.AppendRequest{
			ProjectID: config.ProjectID, WorkspaceID: config.WorkspaceID, OccurredAt: time.Now(),
			Type: eventType, Actor: eventstore.Actor{Kind: "runtime", AgentKind: kind, ACRSessionID: launchID}, Payload: encoded,
		})
	}
	started, err := record("runtime.launch_started", map[string]any{"agent": agent, "mode": *mode, "executable": executable, "argument_count": len(agentArgs)})
	if err != nil {
		fmt.Fprintf(stderr, "record launch: %v\n", err)
		return 1
	}
	launchID = started.EventID

	if *mode == "guarded" {
		self, err := os.Executable()
		if err != nil {
			fmt.Fprintf(stderr, "find the acr executable for guarded mode: %v\n", err)
			return 1
		}
		guardedArgs := []string{}
		if agent == "claude" {
			mcpConfig, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"acr": map[string]any{
				"type": "stdio", "command": self, "args": []string{"mcp", "serve", "--root", paths.Root, "--agent", agent},
				"env": map[string]string{"ACR_LAUNCH_ID": started.EventID},
			}}})
			// --tools allows only read and search built-ins, --strict-mcp-config
			// drops every MCP server but ACR's, and the denylist stays as a
			// second line. --disallowedTools and --mcp-config take several
			// values, so the single-valued --append-system-prompt comes last
			// and ends those lists before the agent's own arguments.
			guardedArgs = []string{"--tools", strings.Join(guardedClaudeTools, ","), "--strict-mcp-config", "--disallowedTools"}
			guardedArgs = append(guardedArgs, guardedDisallowedTools...)
			guardedArgs = append(guardedArgs, "--mcp-config", string(mcpConfig), "--append-system-prompt", guardedInstructions)
		} else {
			serverArgs, _ := json.Marshal([]string{"mcp", "serve", "--root", paths.Root, "--agent", agent})
			commandValue, _ := json.Marshal(self)
			launchIDValue, _ := json.Marshal(started.EventID)
			instructionsValue, _ := json.Marshal(codexGuardedInstructions)
			names := []string{}
			for _, tool := range (guarded.Toolsets{guarded.Executor{}, directive.Service{}}).Tools() {
				names = append(names, tool["name"].(string))
			}
			enabledTools, _ := json.Marshal(names)
			guardedArgs = []string{"--strict-config", "-c", "mcp_servers={}",
				"-c", "mcp_servers.acr.command=" + string(commandValue),
				"-c", "mcp_servers.acr.args=" + string(serverArgs),
				"-c", "mcp_servers.acr.env.ACR_LAUNCH_ID=" + string(launchIDValue),
				"-c", "mcp_servers.acr.enabled_tools="+string(enabledTools),
				"-c", "mcp_servers.acr.required=true",
				"-c", `mcp_servers.acr.default_tools_approval_mode="approve"`,
				"-c", "developer_instructions=" + string(instructionsValue)}
			for _, feature := range codexGuardedDisabledFeatures {
				guardedArgs = append(guardedArgs, "--disable", feature)
			}
			guardedArgs = append(guardedArgs, "--sandbox", "read-only", "--ask-for-approval", "never")
		}
		agentArgs = append(guardedArgs, agentArgs...)
	}
	command := exec.Command(executable, agentArgs...)
	command.Dir = paths.Root
	command.Stdin, command.Stdout, command.Stderr = stdin, stdout, stderr
	command.Env = append(os.Environ(), "ACR_LAUNCH_ID="+started.EventID, "ACR_PROJECT_ROOT="+paths.Root)
	if unattendedLaunch(agent, agentArgs) {
		// Nobody answers confirmation prompts, so the hooks deny instead.
		command.Env = append(command.Env, "ACR_UNATTENDED=1")
	}
	// The agent owns the terminal; ACR outlives an interrupt so the session
	// end is always recorded.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	begin := time.Now()
	runErr := command.Run()
	signal.Stop(interrupts)

	exitCode := 0
	var exitErr *exec.ExitError
	switch {
	case errors.As(runErr, &exitErr):
		exitCode = exitErr.ExitCode()
	case runErr != nil:
		fmt.Fprintf(stderr, "run %s: %v\n", agent, runErr)
		exitCode = 1
	}
	if err := reconcileWorkspaceCoverage(ctx, store, paths.Root, config.ProjectID, config.WorkspaceID, kind); err != nil {
		fmt.Fprintf(stderr, "ACR coverage warning after session: %v\n", err)
	}
	hookEvents, err := countHookEvents(ctx, store, config.ProjectID, started)
	if err != nil {
		fmt.Fprintf(stderr, "ACR could not count hook events: %v\n", err)
	} else if hookEvents == 0 {
		fmt.Fprintf(stderr, "ACR warning: no %s hook reported during this session; hooks may be disabled, untrusted or failing, so ACR did not see the agent's actions. Run acr doctor.\n", agent)
	}
	if _, err := record("runtime.launch_ended", map[string]any{"agent": agent, "launch_event_id": started.EventID, "exit_code": exitCode, "duration_ms": time.Since(begin).Milliseconds(), "error": errorString(runErr), "hook_events": hookEvents}); err != nil {
		fmt.Fprintf(stderr, "record launch end: %v\n", err)
	}
	return exitCode
}

// countHookEvents counts events the agent's hooks recorded for a launch. The
// guarded server records under the launch ID as its session, so its events
// are not hook reports.
func countHookEvents(ctx context.Context, store *eventstore.Store, projectID string, launch eventstore.Event) (int, error) {
	count := 0
	for after := launch.Sequence; ; {
		events, err := store.ListMatching(ctx, projectID, after, 500, eventstore.Filter{ACRSessionID: launch.EventID})
		if err != nil {
			return 0, err
		}
		if len(events) == 0 {
			return count, nil
		}
		for _, event := range events {
			after = event.Sequence
			if event.Actor.ACRSessionID == launch.EventID && event.Actor.NativeSessionID != "" && event.Actor.NativeSessionID != launch.EventID {
				count++
			}
		}
	}
}

// unattendedLaunch reports whether the agent runs without a person at the
// prompt: claude -p/--print or codex exec.
func unattendedLaunch(agent string, args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if agent == "claude" && (arg == "-p" || arg == "--print") || agent == "codex" && arg == "exec" {
			return true
		}
	}
	return false
}

func validateCodexGuardedAgentArgs(args []string) ([]string, error) {
	blockedOptions := map[string]bool{
		"-c": true, "--config": true, "--enable": true, "--disable": true,
		"-s": true, "--sandbox": true, "-a": true, "--ask-for-approval": true,
		"-p": true, "--profile": true, "-C": true, "--cd": true, "--add-dir": true,
		"--remote": true, "--remote-auth-token-env": true, "--approve-for-me": true,
		"--dangerously-bypass-approvals-and-sandbox": true, "--yolo": true,
		"-o": true, "--output-last-message": true,
	}
	blockedCommands := map[string]bool{
		"e": true, "review": true, "login": true, "logout": true, "mcp": true, "plugin": true,
		"mcp-server": true, "app-server": true, "remote-control": true, "completion": true, "update": true,
		"doctor": true, "sandbox": true, "debug": true, "apply": true, "a": true, "resume": true,
		"archive": true, "delete": true, "migrate-rollouts": true, "unarchive": true, "fork": true,
		"cloud": true, "exec-server": true, "features": true, "help": true,
	}
	for _, arg := range args {
		name := arg
		if index := strings.IndexByte(name, '='); index >= 0 {
			name = name[:index]
		}
		if blockedOptions[name] {
			return nil, fmt.Errorf("%q can override the guarded boundary", name)
		}
		if len(name) > 2 && name[0] == '-' && name[1] != '-' && strings.ContainsRune("csapCo", rune(name[1])) {
			return nil, fmt.Errorf("%q can override the guarded boundary", name[:2])
		}
		if name == "--full-auto" {
			return nil, fmt.Errorf("%q can override the guarded boundary", name)
		}
	}
	position := firstCodexPosition(args)
	if position >= 0 && blockedCommands[args[position]] {
		if position == len(args)-1 {
			return insertArgument(args, position, "--"), nil
		}
		return nil, fmt.Errorf("Codex subcommand %q is not a guarded agent session", args[position])
	}
	return args, nil
}

func firstCodexPosition(args []string) int {
	expectsValue := false
	for index, arg := range args {
		if expectsValue {
			expectsValue = false
			continue
		}
		if arg == "--" {
			return -1
		}
		if arg == "-m" || arg == "--model" || arg == "--local-provider" || arg == "--color" {
			expectsValue = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return index
	}
	return -1
}

func insertArgument(args []string, index int, value string) []string {
	result := make([]string, 0, len(args)+1)
	result = append(result, args[:index]...)
	result = append(result, value)
	return append(result, args[index:]...)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
