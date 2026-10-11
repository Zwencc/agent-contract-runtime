package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/agent-contract-runtime/acr/internal/directive"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/guarded"
	"github.com/agent-contract-runtime/acr/internal/memory"
	"github.com/agent-contract-runtime/acr/internal/objectstore"
	"github.com/agent-contract-runtime/acr/internal/project"
	"github.com/agent-contract-runtime/acr/internal/protocol"
	"github.com/agent-contract-runtime/acr/internal/repair"
)

// guardedDisallowedTools are the Claude Code tools that change the
// workspace; guarded sessions must use ACR's MCP tools instead.
var guardedDisallowedTools = []string{"Bash", "Edit", "Write", "NotebookEdit"}

// guardedClaudeTools is the allowlist of built-in Claude Code tools in a
// guarded session: read and search only. An allowlist keeps tools added by
// later Claude Code versions out by default; the 2.1.303 run showed
// Monitor, Task, SendMessage, CronCreate and others survive a denylist.
// ToolSearch must stay, because the agent loads ACR's deferred MCP tools
// through it.
var guardedClaudeTools = []string{"Read", "Glob", "Grep", "ToolSearch", "WebFetch", "WebSearch"}

// codexGuardedDisabledFeatures removes every Codex 0.148.0 host capability
// found during capability certification that can execute commands, mutate the
// workspace indirectly, or delegate to another tool-bearing runtime. The
// read-only sandbox remains the enforcement backstop for native tools that
// cannot be removed independently, including apply_patch.
var codexGuardedDisabledFeatures = []string{
	"shell_tool", "unified_exec", "code_mode", "multi_agent",
	"hooks", "plugins", "remote_plugin", "apps", "skill_mcp_dependency_install", "workspace_dependencies",
	"shell_snapshot", "request_permissions_tool", "guardian_approval", "image_generation",
	"browser_use", "browser_use_external", "browser_use_full_cdp_access", "computer_use", "in_app_browser",
}

// codexGuardedCertified gates Codex guarded launches. Codex 0.148.0 passed
// source review, bypass probes, and a positive exec run with only ACR's MCP
// server approved while the global native-tool approval policy stayed never.
var codexGuardedCertified = true

const codexGuardedUnavailable = "guarded mode is not available for this Codex build; use acr launch --mode native codex and see docs/capability-codex-guarded.md"

const guardedInstructions = "ACR guarded mode: the native Bash, Edit, Write and NotebookEdit tools are disabled. " +
	"Use mcp__acr__exec, mcp__acr__write_file and mcp__acr__edit_file instead. ACR checks every call against the project contracts before it runs; " +
	"a refused call did not run, and its message says why and what to do."

const codexGuardedInstructions = "ACR guarded mode: native command execution, patching, code mode, subagents, hooks, plugins, apps, browser and computer-use paths are disabled, and the native sandbox is read-only. " +
	"Use mcp__acr__exec, mcp__acr__write_file and mcp__acr__edit_file for all commands and workspace changes. ACR checks every call against the project contracts before it runs; " +
	"a refused call did not run, and its message says why and what to do."

// runMCP serves the guarded tools over the MCP stdio transport.
func runMCP(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "serve" {
		fmt.Fprintln(stderr, "mcp requires serve")
		return 2
	}
	flags := flag.NewFlagSet("mcp serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	agent := flags.String("agent", "claude", "agent using the tools: claude or codex")
	toolset := flags.String("toolset", "guarded", "guarded (execution plus memory and rule tools) or directives (memory and rule tools only)")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 || (*agent != "claude" && *agent != "codex") || (*toolset != "guarded" && *toolset != "directives") {
		fmt.Fprintln(stderr, "mcp serve accepts --root, --agent claude|codex and --toolset guarded|directives")
		return 2
	}
	config, paths, err := project.Load(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	resolvedRoot, err := filepath.EvalSymlinks(paths.Root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer store.Close()
	objects, err := objectstore.Open(paths.Objects)
	if err != nil {
		fmt.Fprintln(stderr, "ACR object store unavailable; recording tool output inline: "+err.Error())
		objects = nil
	}
	session := os.Getenv("ACR_LAUNCH_ID")
	if session == "" {
		session = "guarded-" + rand.Text()
	}
	kind := protocol.AgentCodex
	if *agent == "claude" {
		kind = protocol.AgentClaudeCode
	}
	memories, err := memory.Open(paths.MemoryDB)
	if err != nil {
		fmt.Fprintln(stderr, "ACR memory store unavailable; memory tools will fail: "+err.Error())
		memories = nil
	} else {
		defer memories.Close()
	}
	directives := directive.Service{Events: store, Memories: memories, ProjectID: config.ProjectID, WorkspaceID: config.WorkspaceID,
		ContractsDir: paths.Contracts, Agent: kind, LaunchID: os.Getenv("ACR_LAUNCH_ID")}
	var tools guarded.Toolset = directives
	if *toolset == "guarded" {
		executor := guarded.Executor{
			Root: resolvedRoot, ProjectID: config.ProjectID, WorkspaceID: config.WorkspaceID, Agent: kind, SessionID: session,
			ContractsPath: paths.Contracts, Events: store, Objects: objects,
			LockDir: filepath.Join(paths.State, "repair-locks"), Commands: repair.CommandExecutor{},
			Authorizer: directive.Grants{Events: store, ProjectID: config.ProjectID},
		}
		tools = guarded.Toolsets{executor, directives}
	}
	if err := guarded.Serve(context.Background(), stdin, stdout, tools, version); err != nil {
		fmt.Fprintf(stderr, "mcp serve: %v\n", err)
		return 1
	}
	return 0
}
