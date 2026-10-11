package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agent-contract-runtime/acr/internal/adapter"
	"github.com/agent-contract-runtime/acr/internal/adapter/claude"
	"github.com/agent-contract-runtime/acr/internal/adapter/codex"
	"github.com/agent-contract-runtime/acr/internal/bootstrap"
	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/contextpack"
	"github.com/agent-contract-runtime/acr/internal/coverage"
	"github.com/agent-contract-runtime/acr/internal/directive"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/hookconfig"
	"github.com/agent-contract-runtime/acr/internal/memory"
	"github.com/agent-contract-runtime/acr/internal/objectstore"
	"github.com/agent-contract-runtime/acr/internal/project"
	"github.com/agent-contract-runtime/acr/internal/protocol"
	"github.com/agent-contract-runtime/acr/internal/registry"
	acrruntime "github.com/agent-contract-runtime/acr/internal/runtime"
	"github.com/agent-contract-runtime/acr/internal/verification"
)

const version = "0.1.0-dev"

func main() {
	leaveStoresOpen = true
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Hooks run in the daemon when one is running, and here otherwise; both
	// run the same code, so enforcement never depends on the daemon.
	if len(args) >= 1 && args[0] == "hook" && os.Getenv("ACR_DAEMON") != "off" {
		data, err := io.ReadAll(io.LimitReader(stdin, maximumDaemonInput+1))
		if err == nil && len(data) <= maximumDaemonInput {
			if code, ok := forwardHook(args, data, stdout, stderr); ok {
				return code
			}
		}
		stdin = io.MultiReader(bytes.NewReader(data), stdin)
	}
	return runCommand(args, stdin, stdout, stderr)
}

func runCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) >= 1 && args[0] == "daemon" {
		return runDaemon(args[1:], stdout, stderr)
	}
	if len(args) >= 1 && args[0] == "ui" {
		return runUI(args[1:], stdout, stderr)
	}
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintf(stdout, "acr %s\n", version)
		return 0
	}
	if len(args) >= 1 && args[0] == "init" {
		return runInit(args[1:], stdout, stderr)
	}
	if len(args) >= 1 && args[0] == "doctor" {
		return runDoctor(args[1:], stdout, stderr)
	}
	if len(args) >= 1 && args[0] == "install-hooks" {
		return runInstallHooks(args[1:], stdout, stderr)
	}
	if len(args) >= 1 && args[0] == "evaluate" {
		return runEvaluate(args[1:], stdin, stdout, stderr)
	}
	if len(args) >= 2 && args[0] == "contract" && args[1] == "simulate" {
		return runContractSimulate(args[2:], stdout, stderr)
	}
	if len(args) >= 1 && args[0] == "candidate" {
		return runCandidate(args[1:], stdout, stderr)
	}
	if len(args) >= 2 && args[0] == "contract" && (args[1] == "activate" || args[1] == "retire" || args[1] == "list") {
		return runContractRegistry(args[1], args[2:], stdout, stderr)
	}
	if len(args) >= 2 && args[0] == "contract" && args[1] == "validate" {
		return runContractValidate(args[2:], stdout, stderr)
	}
	if len(args) >= 3 && args[0] == "hook" && args[2] == "pre-tool-use" {
		return runPreToolUseHook(args[1], args[3:], stdin, stdout, stderr)
	}
	if len(args) >= 3 && args[0] == "hook" && args[2] == "user-prompt-submit" {
		return runUserPromptSubmitHook(args[1], args[3:], stdin, stdout, stderr)
	}
	if len(args) >= 3 && args[0] == "hook" && args[2] == "lifecycle" {
		return runLifecycleHook(args[1], args[3:], stdin, stdout, stderr)
	}
	if len(args) >= 3 && args[0] == "hook" && args[2] == "session-start" {
		return runSessionStartHook(args[1], args[3:], stdin, stdout, stderr)
	}
	if len(args) >= 3 && args[0] == "hook" && (args[2] == "post-tool-use" || args[2] == "post-tool-use-failure") {
		return runPostToolUseHook(args[1], args[2], args[3:], stdin, stdout, stderr)
	}
	if len(args) >= 2 && args[0] == "event" && args[1] == "list" {
		return runEventList(args[2:], stdout, stderr)
	}
	if len(args) >= 2 && args[0] == "integrity" && args[1] == "verify" {
		return runIntegrityVerify(args[2:], stdout, stderr)
	}
	if len(args) >= 2 && args[0] == "bootstrap" && args[1] == "sources" {
		return runBootstrapSources(args[2:], stdout, stderr)
	}
	if len(args) >= 1 && args[0] == "memory" {
		return runMemory(args[1:], stdout, stderr)
	}
	if len(args) >= 1 && args[0] == "reconcile" {
		return runReconcile(args[1:], stdout, stderr)
	}
	if len(args) >= 1 && args[0] == "repair" {
		return runRepair(args[1:], stdout, stderr)
	}
	if len(args) >= 1 && args[0] == "mcp" {
		return runMCP(args[1:], stdin, stdout, stderr)
	}
	if len(args) >= 1 && args[0] == "launch" {
		return runLaunch(args[1:], stdin, stdout, stderr)
	}
	if len(args) >= 1 && args[0] == "replay" {
		return runReplay(args[1:], stdout, stderr)
	}
	if len(args) >= 2 && args[0] == "verification" && args[1] == "list" {
		return runVerificationList(args[2:], stdout, stderr)
	}

	writeUsage(stderr)
	return 2
}

func runBootstrapSources(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("bootstrap sources", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	maximumFileBytes := flags.Int64("max-file-bytes", 0, "maximum bytes per source")
	maximumTotalBytes := flags.Int64("max-total-bytes", 0, "maximum indexed bytes")
	maximumSources := flags.Int("max-sources", 0, "maximum discovered sources")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "bootstrap sources accepts no positional arguments")
		return 2
	}
	report, err := bootstrap.Discover(*root, bootstrap.Options{
		MaximumFileBytes:  *maximumFileBytes,
		MaximumTotalBytes: *maximumTotalBytes,
		MaximumSources:    *maximumSources,
	})
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintf(stderr, "encode bootstrap source report: %v\n", err)
		return 1
	}
	return 0
}

// directiveGuide tells every session how to carry out what the user asks it
// to remember or enforce, so the change reaches the other sessions too.
const directiveGuide = "# ACR memory and rules\n\n" +
	"When the user asks you to remember something, correct or forget a memory, or add or drop a rule for this project, use the ACR tools " +
	"(mcp__acr__remember, correct_memory, forget_memory, add_rule, retire_rule) and pass the user's exact words as quote. " +
	"Do not write .acr/ yourself. Things you decide to remember on your own stay candidates until the user agrees."

func runSessionStartHook(agentName string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if agentName != "codex" && agentName != "claude" {
		fmt.Fprintf(stderr, "unsupported hook agent %q; want codex or claude\n", agentName)
		return 2
	}
	flags := flag.NewFlagSet("hook "+agentName+" session-start", flag.ContinueOnError)
	flags.SetOutput(stderr)
	contractsPath := flags.String("contracts", "", "contract JSON file or directory")
	eventDB := flags.String("event-db", "", "SQLite event database path")
	memoryDB := flags.String("memory-db", "", "SQLite memory database path")
	projectID := flags.String("project-id", "", "ACR project id")
	workspaceID := flags.String("workspace-id", "", "ACR workspace id")
	maximumCharacters := flags.Int("max-chars", 6000, "maximum context characters")
	root := flags.String("root", "", "project root for bootstrap source refresh")
	projectionDB := flags.String("projection-db", "", "projection database; defaults to projections.db next to --event-db")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *contractsPath == "" || *eventDB == "" || *projectID == "" {
		fmt.Fprintln(stderr, "session-start requires --contracts, --event-db, and --project-id")
		return 2
	}
	raw, err := readHookInput(stdin)
	if err != nil {
		return writePostHookWarning("ACR could not read SessionStart input: "+err.Error(), stdout, stderr)
	}
	input, err := adapter.DecodeSessionStart(raw)
	if err != nil {
		return writePostHookWarning("ACR rejected SessionStart input: "+err.Error(), stdout, stderr)
	}
	store, release, err := openEvents(*eventDB)
	if err != nil {
		return writePostHookWarning("ACR could not open event store: "+err.Error(), stdout, stderr)
	}
	defer release()
	contracts, err := registry.Load(context.Background(), store, *projectID, *contractsPath)
	if err != nil {
		return writePostHookWarning("ACR could not load contracts: "+err.Error(), stdout, stderr)
	}
	kind := protocol.AgentCodex
	if agentName == "claude" {
		kind = protocol.AgentClaudeCode
	}
	bootstrapWarning := ""
	if *root != "" {
		if err := refreshBootstrapSources(context.Background(), store, *root, *projectID, *workspaceID, kind); err != nil {
			bootstrapWarning = "ACR bootstrap refresh warning: " + err.Error()
			fmt.Fprintln(stderr, bootstrapWarning)
		}
		if err := reconcileWorkspaceCoverage(context.Background(), store, *root, *projectID, *workspaceID, kind); err != nil {
			if bootstrapWarning != "" {
				bootstrapWarning += "; "
			}
			bootstrapWarning += "ACR coverage warning: " + err.Error()
			fmt.Fprintln(stderr, bootstrapWarning)
		} else if err := reconcileVerifications(context.Background(), store, *root, *projectID, *workspaceID); err != nil {
			if bootstrapWarning != "" {
				bootstrapWarning += "; "
			}
			bootstrapWarning += "ACR verification warning: " + err.Error()
			fmt.Fprintln(stderr, bootstrapWarning)
		}
	}
	var memoryStore *memory.Store
	if *memoryDB != "" {
		var releaseMemories func()
		memoryStore, releaseMemories, err = openMemories(*memoryDB)
		if err != nil {
			if bootstrapWarning != "" {
				bootstrapWarning += "; "
			}
			bootstrapWarning += "ACR memory warning: " + err.Error()
			fmt.Fprintln(stderr, bootstrapWarning)
		} else {
			defer releaseMemories()
		}
	}
	projectionRoot := *root
	if projectionRoot == "" {
		projectionRoot = input.CWD
	}
	if *projectionDB == "" {
		*projectionDB = filepath.Join(filepath.Dir(*eventDB), "projections.db")
	}
	projections, releaseProjections, err := openProjections(*projectionDB)
	if err != nil {
		if bootstrapWarning != "" {
			bootstrapWarning += "; "
		}
		bootstrapWarning += "ACR project state warning: " + err.Error()
		fmt.Fprintln(stderr, bootstrapWarning)
		projections = nil
	} else {
		defer releaseProjections()
	}
	// The guide comes first and the project context gets the rest.
	budget := max(*maximumCharacters-len(directiveGuide)-2, 0)
	contextText, err := contextpack.BuildWithMemory(context.Background(), store, memoryStore, *projectID, *workspaceID, contracts, contextpack.Options{MaximumCharacters: budget, Projections: projections, Root: projectionRoot})
	if err != nil {
		return writePostHookWarning("ACR could not build project context: "+err.Error(), stdout, stderr)
	}
	if bootstrapWarning != "" {
		contextText = bootstrapWarning + "\n\n" + contextText
	}
	contextText = limitUTF8(directiveGuide+"\n\n"+contextText, *maximumCharacters)
	sessionPayload, _ := json.Marshal(map[string]string{"source": input.Source, "cwd": input.CWD})
	_, appendErr := store.Append(context.Background(), eventstore.AppendRequest{
		ProjectID:   *projectID,
		WorkspaceID: *workspaceID,
		OccurredAt:  time.Now(),
		Type:        "session.started",
		Actor: eventstore.Actor{
			Kind:            "agent",
			AgentKind:       kind,
			NativeSessionID: input.SessionID,
			ACRSessionID:    os.Getenv("ACR_LAUNCH_ID"),
		},
		Payload: sessionPayload,
	})
	if appendErr != nil {
		return writePostHookWarning("ACR could not record session start: "+appendErr.Error(), stdout, stderr)
	}
	encoded, err := adapter.EncodeSessionContext(contextText)
	if err != nil {
		return writePostHookWarning("ACR could not encode session context: "+err.Error(), stdout, stderr)
	}
	fmt.Fprintln(stdout, string(encoded))
	return 0
}

func reconcileWorkspaceCoverage(ctx context.Context, store *eventstore.Store, root, projectID, workspaceID string, kind protocol.AgentKind) error {
	paths, err := project.ResolvePaths(root)
	if err != nil {
		return err
	}
	head, err := store.Head(ctx, projectID)
	if errors.Is(err, eventstore.ErrProjectNotFound) {
		head.Sequence = 0
	} else if err != nil {
		return fmt.Errorf("read event head for workspace coverage: %w", err)
	}
	current, err := coverage.Scan(paths.Root, head.Sequence, coverage.Options{})
	if err != nil {
		return err
	}
	previous, err := coverage.Read(paths.WorkspaceSnapshot)
	if errors.Is(err, os.ErrNotExist) {
		return coverage.Write(paths.WorkspaceSnapshot, current)
	} else if err != nil {
		return err
	}
	if previous.Root != current.Root {
		return fmt.Errorf("workspace snapshot root %q does not match current root %q", previous.Root, current.Root)
	}
	changes := coverage.Compare(previous, current)
	if len(changes) == 0 {
		return coverage.Write(paths.WorkspaceSnapshot, current)
	}
	unexplained, outcomeEventIDs, err := unexplainedChangesSince(ctx, store, projectID, current.Root, previous.EventSequence, head.Sequence, changes)
	if err != nil {
		return err
	}
	if len(unexplained) > 0 {
		const maximumChanges = 200
		observed := unexplained
		omitted := 0
		if len(observed) > maximumChanges {
			omitted = len(observed) - maximumChanges
			observed = observed[:maximumChanges]
		}
		confidence := "confirmed"
		reason := "workspace changed without any recorded tool outcome"
		if len(outcomeEventIDs) > 0 {
			confidence = "suspected"
			reason = "workspace changes were not attributable to recorded tool inputs"
		}
		payload, err := json.Marshal(struct {
			KnownFromSequence uint64            `json:"known_from_sequence"`
			KnownUntilSequence uint64           `json:"known_until_sequence"`
			Reason              string            `json:"reason"`
			Confidence          string            `json:"confidence"`
			ObservedChanges     []coverage.Change `json:"observed_changes"`
			OmittedChanges      int               `json:"omitted_changes,omitempty"`
			SkippedFiles        int               `json:"skipped_files,omitempty"`
			OutcomeEventIDs     []string          `json:"outcome_event_ids,omitempty"`
		}{
			KnownFromSequence: previous.EventSequence,
			KnownUntilSequence: head.Sequence,
			Reason:              reason,
			Confidence:          confidence,
			ObservedChanges:     observed,
			OmittedChanges:      omitted,
			SkippedFiles:        current.SkippedFiles,
			OutcomeEventIDs:     outcomeEventIDs,
		})
		if err != nil {
			return fmt.Errorf("encode coverage gap: %w", err)
		}
		event, err := store.Append(ctx, eventstore.AppendRequest{
			ProjectID:   projectID,
			WorkspaceID: workspaceID,
			OccurredAt:  time.Now(),
			Type:        "coverage.gap_detected",
			Actor: eventstore.Actor{Kind: "runtime", AgentKind: kind},
			Payload: payload,
		})
		if err != nil {
			return fmt.Errorf("record workspace coverage gap: %w", err)
		}
		current.EventSequence = event.Sequence
	}
	return coverage.Write(paths.WorkspaceSnapshot, current)
}

// reconcileVerifications invalidates verifications whose inputs differ from
// the workspace snapshot that coverage reconciliation has just written.
func reconcileVerifications(ctx context.Context, store *eventstore.Store, root, projectID, workspaceID string) error {
	paths, err := project.ResolvePaths(root)
	if err != nil {
		return err
	}
	snapshot, err := coverage.Read(paths.WorkspaceSnapshot)
	if err != nil {
		return err
	}
	_, err = verification.Reconcile(ctx, store, projectID, workspaceID, snapshot, time.Now())
	return err
}

// unexplainedChangesSince returns the changes no recorded outcome accounts
// for, and the ids of all outcome events in the range. root is the
// symlink-resolved workspace root the change paths are relative to.
func unexplainedChangesSince(ctx context.Context, store *eventstore.Store, projectID, root string, after, through uint64, changes []coverage.Change) ([]coverage.Change, []string, error) {
	matched := make(map[string]bool, len(changes))
	outcomeEventIDs := []string{}
	for after < through {
		events, err := store.List(ctx, projectID, after, 500)
		if err != nil {
			return nil, nil, fmt.Errorf("read events for workspace coverage: %w", err)
		}
		if len(events) == 0 {
			break
		}
		for _, event := range events {
			if event.Sequence > through {
				after = through
				break
			}
			if outcomeEventTypes[event.Type] {
				outcomeEventIDs = append(outcomeEventIDs, event.EventID)
				explainChanges(event, root, changes, matched)
			}
			after = event.Sequence
		}
	}
	unexplained := make([]coverage.Change, 0, len(changes))
	for _, change := range changes {
		if !matched[change.Path] {
			unexplained = append(unexplained, change)
		}
	}
	return unexplained, outcomeEventIDs, nil
}

// outcomeEventTypes are the events showing that tool calls were recorded.
var outcomeEventTypes = map[string]bool{
	"action.completed": true, "action.failed": true,
	"repair.step_completed": true, "repair.failed": true,
	"repair.compensation_completed": true, "repair.compensation_failed": true,
	"config.installed": true,
}

// writeTools are the host tools whose recorded resources are the files they
// changed. Other tools, including shell commands and reads, explain nothing
// by naming a path.
var writeTools = map[string]bool{"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true, "apply_patch": true}

// explainChanges marks the changes an outcome event accounts for: the exact
// resources of a write tool, the files ACR observed changing while it ran a
// call itself, or the paths named in a repair step's argv. Command output
// is never evidence.
func explainChanges(event eventstore.Event, root string, changes []coverage.Change, matched map[string]bool) {
	var payload struct {
		ToolName        string   `json:"tool_name"`
		Resources       []string `json:"resources"`
		ObservedChanges []string `json:"observed_changes"`
		Argv            []string `json:"argv"`
	}
	if json.Unmarshal(event.Payload, &payload) != nil {
		return
	}
	explained := map[string]bool{}
	for _, value := range payload.ObservedChanges {
		explained[value] = true
	}
	if writeTools[payload.ToolName] {
		for _, resource := range payload.Resources {
			if relative, ok := projectRelative(root, resource); ok {
				explained[relative] = true
			}
		}
	}
	for _, change := range changes {
		if explained[change.Path] {
			matched[change.Path] = true
			continue
		}
		for _, argument := range payload.Argv {
			if strings.Contains(argument, change.Path) {
				matched[change.Path] = true
			}
		}
	}
}

// projectRelative maps a recorded path onto the slash-separated path
// relative to root, which coverage changes use.
func projectRelative(root, value string) (string, bool) {
	candidate := filepath.Clean(value)
	if filepath.IsAbs(candidate) {
		relative, err := filepath.Rel(root, candidate)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			resolved, resolveErr := filepath.EvalSymlinks(filepath.Dir(candidate))
			if resolveErr != nil {
				return "", false
			}
			if relative, err = filepath.Rel(root, filepath.Join(resolved, filepath.Base(candidate))); err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return "", false
			}
		}
		candidate = relative
	}
	candidate = filepath.ToSlash(candidate)
	if candidate == "." || candidate == ".." || strings.HasPrefix(candidate, "../") {
		return "", false
	}
	return candidate, true
}

func limitUTF8(value string, maximum int) string {
	if maximum <= 0 || len(value) <= maximum {
		return value
	}
	boundary := 0
	for index := range value {
		if index > maximum {
			break
		}
		boundary = index
	}
	return value[:boundary]
}

func refreshBootstrapSources(ctx context.Context, store *eventstore.Store, root, projectID, workspaceID string, kind protocol.AgentKind) error {
	paths, err := project.ResolvePaths(root)
	if err != nil {
		return err
	}
	snapshotPath := filepath.Join(paths.State, "bootstrap-sources.json")
	current, err := bootstrap.Discover(paths.Root, bootstrap.Options{})
	if err != nil {
		return err
	}
	previous, err := bootstrap.ReadSnapshot(snapshotPath)
	if errors.Is(err, os.ErrNotExist) {
		previous = bootstrap.Report{Root: current.Root, Sources: []bootstrap.Source{}}
	} else if err != nil {
		return err
	}
	diff := bootstrap.Compare(previous, current)
	if diff.Empty() {
		return nil
	}
	payload, err := json.Marshal(diff)
	if err != nil {
		return fmt.Errorf("encode bootstrap source diff: %w", err)
	}
	if _, err := store.Append(ctx, eventstore.AppendRequest{
		ProjectID:   projectID,
		WorkspaceID: workspaceID,
		OccurredAt:  time.Now(),
		Type:        "source.snapshot_changed",
		Actor: eventstore.Actor{
			Kind:      "agent_session",
			AgentKind: kind,
		},
		Payload: payload,
	}); err != nil {
		return fmt.Errorf("record bootstrap source diff: %w", err)
	}
	if err := bootstrap.WriteSnapshot(snapshotPath, current); err != nil {
		return err
	}
	return nil
}

func runInstallHooks(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("install-hooks", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	agentName := flags.String("agent", "all", "codex, claude, or all")
	binaryPath := flags.String("binary", "", "absolute acr binary path")
	dryRun := flags.Bool("dry-run", false, "preview merged configuration")
	apply := flags.Bool("apply", false, "write merged configuration after creating backups")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || (*agentName != "all" && *agentName != "codex" && *agentName != "claude") {
		fmt.Fprintln(stderr, "install-hooks requires --agent codex|claude|all and accepts no positional arguments")
		return 2
	}
	if *dryRun && *apply {
		fmt.Fprintln(stderr, "install-hooks accepts either --dry-run or --apply, not both")
		return 2
	}
	preview := !*apply
	config, paths, err := project.Load(*root)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	if *binaryPath == "" {
		*binaryPath, err = os.Executable()
		if err != nil {
			fmt.Fprintf(stderr, "resolve acr executable: %v\n", err)
			return 1
		}
	}
	*binaryPath, err = filepath.Abs(*binaryPath)
	if err != nil {
		fmt.Fprintf(stderr, "resolve acr binary path: %v\n", err)
		return 1
	}

	agents := []string{*agentName}
	if *agentName == "all" {
		agents = []string{"codex", "claude"}
	}
	type hookUpdate struct {
		agent  string
		target string
		data   []byte
		unchanged bool
	}
	updates := make([]hookUpdate, 0, len(agents))
	for _, agent := range agents {
		target := filepath.Join(paths.Root, "."+agent, "hooks.json")
		if agent == "claude" {
			target = filepath.Join(paths.Root, ".claude", "settings.json")
		}
		existing, readErr := os.ReadFile(target)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			fmt.Fprintf(stderr, "read existing hook config %q: %v\n", target, readErr)
			return 1
		}
		common := []string{
			*binaryPath,
			"hook", agent,
		}
		identity := []string{
			"--project-id", config.ProjectID,
			"--workspace-id", config.WorkspaceID,
			"--event-db", paths.EventDB,
		}
		sessionStart := append(append([]string{}, common...), "session-start", "--contracts", paths.Contracts, "--root", paths.Root, "--memory-db", paths.MemoryDB)
		sessionStart = append(sessionStart, identity...)
		commands := hookconfig.Commands{
			SessionStart: shellJoin(sessionStart),
			UserPromptSubmit: shellJoin(append(append(append([]string{}, common...), "user-prompt-submit", "--root", paths.Root), identity...)),
			PreToolUse: shellJoin(append(append(append([]string{}, common...), "pre-tool-use", "--contracts", paths.Contracts, "--root", paths.Root), identity...)),
			PostToolUse: shellJoin(append(append(append([]string{}, common...), "post-tool-use"), identity...)),
		}
		if agent == "claude" {
			commands.PostToolUseFailure = shellJoin(append(append(append([]string{}, common...), "post-tool-use-failure"), identity...))
		}
		commands.Lifecycle = shellJoin(append(append(append([]string{}, common...), "lifecycle", "--root", paths.Root), identity...))
		merged, err := hookconfig.Merge(existing, agent, commands)
		if err != nil {
			fmt.Fprintf(stderr, "build %s hook config: %v\n", agent, err)
			return 1
		}
		updates = append(updates, hookUpdate{agent: agent, target: target, data: merged, unchanged: string(existing) == string(merged)})

		// The memory and rule tools, so the agent can carry out what the
		// user asks it to remember or enforce.
		mcpArgs := []string{"mcp", "serve", "--root", paths.Root, "--agent", agent, "--toolset", "directives"}
		mcpTarget := filepath.Join(paths.Root, ".codex", "config.toml")
		if agent == "claude" {
			mcpTarget = filepath.Join(paths.Root, ".mcp.json")
		}
		existing, readErr = os.ReadFile(mcpTarget)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			fmt.Fprintf(stderr, "read existing MCP config %q: %v\n", mcpTarget, readErr)
			return 1
		}
		var mcpData []byte
		changed := true
		if agent == "claude" {
			mcpData, err = hookconfig.MergeClaudeMCP(existing, *binaryPath, mcpArgs)
			changed = string(existing) != string(mcpData)
		} else {
			mcpData, changed, err = hookconfig.MergeCodexMCP(existing, *binaryPath, mcpArgs)
		}
		if err != nil {
			fmt.Fprintf(stderr, "build %s MCP config: %v\n", agent, err)
			return 1
		}
		updates = append(updates, hookUpdate{agent: agent, target: mcpTarget, data: mcpData, unchanged: !changed})
	}
	written := []string{}
	defer func() {
		if len(written) == 0 {
			return
		}
		// The files ACR wrote are accounted for, so the next session start
		// does not report them as unexplained changes.
		store, err := eventstore.Open(paths.EventDB)
		if err != nil {
			fmt.Fprintf(stderr, "ACR could not record the configuration change: %v\n", err)
			return
		}
		defer store.Close()
		payload, _ := json.Marshal(map[string]any{"command": "install-hooks", "observed_changes": written})
		if _, err := store.Append(context.Background(), eventstore.AppendRequest{ProjectID: config.ProjectID, WorkspaceID: config.WorkspaceID, OccurredAt: time.Now(),
			Type: "config.installed", Actor: eventstore.Actor{Kind: "human"}, Payload: payload}); err != nil {
			fmt.Fprintf(stderr, "ACR could not record the configuration change: %v\n", err)
		}
	}()
	for _, update := range updates {
		if update.unchanged {
			fmt.Fprintf(stdout, "%s hooks already installed: %s\n", update.agent, update.target)
			continue
		}
		if preview {
			fmt.Fprintf(stdout, "would write %s\n%s", update.target, update.data)
			continue
		}
		backup, err := hookconfig.WriteAtomic(update.target, update.data)
		if err != nil {
			fmt.Fprintf(stderr, "install %s hook config: %v\n", update.agent, err)
			return 1
		}
		for _, file := range []string{update.target, backup} {
			if relative, err := filepath.Rel(paths.Root, file); file != "" && err == nil {
				written = append(written, filepath.ToSlash(relative))
			}
		}
		label := "hooks"
		if base := filepath.Base(update.target); base == ".mcp.json" || base == "config.toml" {
			label = "memory and rule tools"
		}
		fmt.Fprintf(stdout, "installed %s %s: %s\n", update.agent, label, update.target)
		if backup != "" {
			fmt.Fprintf(stdout, "backup: %s\n", backup)
		}
		if filepath.Base(update.target) == ".mcp.json" {
			fmt.Fprintln(stdout, "Claude Code asks once to approve the project MCP server acr; approve it so the agent can record what you ask it to remember")
		}
	}
	return 0
}

func shellJoin(arguments []string) string {
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		quoted[index] = "'" + strings.ReplaceAll(argument, "'", `'"'"'`) + "'"
	}
	return strings.Join(quoted, " ")
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "doctor accepts no positional arguments")
		return 2
	}
	config, paths, err := project.Load(*root)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR project: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "OK project: %s (%s)\n", config.Name, config.ProjectID)

	entries, err := os.ReadDir(paths.Contracts)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR contracts: %v\n", err)
		return 1
	}
	contractCount := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			contractCount++
		}
	}
	if contractCount == 0 {
		fmt.Fprintln(stdout, "WARN contracts: no .json contracts; runtime enforcement is not ready")
	} else {
		values, err := contract.LoadPath(paths.Contracts)
		if err != nil {
			fmt.Fprintf(stderr, "ERROR contracts: %v\n", err)
			return 1
		}
		if _, err := acrruntime.New(values); err != nil {
			fmt.Fprintf(stderr, "ERROR contracts: %v\n", err)
			return 1
		}
		active := 0
		for _, value := range values {
			if value.Metadata.Status == contract.StatusActive {
				active++
			}
		}
		fmt.Fprintf(stdout, "OK contracts: %d valid, %d active\n", len(values), active)
	}

	var launch eventstore.Event
	var hookTimes map[protocol.AgentKind]time.Time
	var launchErr error
	if _, err := os.Stat(paths.EventDB); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stdout, "WARN events: database does not exist yet")
	} else if err != nil {
		fmt.Fprintf(stderr, "ERROR events: %v\n", err)
		return 1
	} else {
		store, err := eventstore.Open(paths.EventDB)
		if err != nil {
			fmt.Fprintf(stderr, "ERROR events: %v\n", err)
			return 1
		}
		verifyErr := store.Verify(context.Background(), config.ProjectID)
		gaps, gapErr := unresolvedCoverageGaps(context.Background(), store, config.ProjectID)
		launch, hookTimes, launchErr = hookHistory(context.Background(), store, config.ProjectID)
		registered, managed, registryErr := registry.Active(context.Background(), store, config.ProjectID)
		store.Close()
		if errors.Is(verifyErr, eventstore.ErrProjectNotFound) {
			fmt.Fprintln(stdout, "OK events: database initialized, no events yet")
		} else if verifyErr != nil {
			fmt.Fprintf(stderr, "ERROR events: %v\n", verifyErr)
			return 1
		} else {
			fmt.Fprintln(stdout, "OK events: hash chain verified")
		}
		if gapErr != nil {
			fmt.Fprintf(stderr, "ERROR coverage gaps: %v\n", gapErr)
			return 1
		}
		if len(gaps) > 0 {
			fmt.Fprintf(stdout, "WARN coverage gaps: %d unresolved; run acr reconcile status\n", len(gaps))
		} else {
			fmt.Fprintln(stdout, "OK coverage gaps: none unresolved")
		}
		if launchErr != nil {
			fmt.Fprintf(stderr, "ERROR launches: %v\n", launchErr)
			return 1
		}
		switch {
		case registryErr != nil:
			fmt.Fprintf(stderr, "ERROR contract registry: %v\n", registryErr)
			return 1
		case !managed:
			fmt.Fprintln(stdout, "WARN contract registry: contracts are read from the directory, so anything that writes it changes the rules; activate them with acr contract activate")
		default:
			fmt.Fprintf(stdout, "OK contract registry: %d contracts activated through ACR\n", len(registered))
			for _, name := range unmanagedContractFiles(paths.Contracts, registered) {
				fmt.Fprintf(stdout, "WARN contract registry: %s is not enforced; it differs from the active contracts\n", name)
			}
		}
		var ended struct {
			Agent      string `json:"agent"`
			HookEvents *int   `json:"hook_events"`
		}
		if launch.EventID != "" && json.Unmarshal(launch.Payload, &ended) == nil && ended.HookEvents != nil {
			when := launch.OccurredAt.UTC().Format(time.RFC3339)
			if *ended.HookEvents == 0 {
				fmt.Fprintf(stdout, "WARN hooks: the last acr launch of %s (%s) recorded no hook events; check that its hooks are installed, trusted and enabled\n", ended.Agent, when)
			} else {
				fmt.Fprintf(stdout, "OK hooks: the last acr launch of %s (%s) recorded %d hook events\n", ended.Agent, when, *ended.HookEvents)
			}
		}
	}
	if _, err := os.Stat(paths.MemoryDB); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stdout, "WARN memories: database does not exist yet")
	} else if err != nil {
		fmt.Fprintf(stderr, "ERROR memories: %v\n", err)
		return 1
	} else {
		store, err := memory.Open(paths.MemoryDB)
		if err != nil {
			fmt.Fprintf(stderr, "ERROR memories: %v\n", err)
			return 1
		}
		verifyErr := store.Verify(context.Background(), config.ProjectID)
		store.Close()
		if verifyErr != nil {
			fmt.Fprintf(stderr, "ERROR memories: %v\n", verifyErr)
			return 1
		}
		fmt.Fprintln(stdout, "OK memories: revision integrity verified")
	}
	if _, err := os.Stat(paths.Objects); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stdout, "OK objects: none stored yet")
	} else if err != nil {
		fmt.Fprintf(stderr, "ERROR objects: %v\n", err)
		return 1
	} else {
		objects, err := objectstore.Open(paths.Objects)
		if err != nil {
			fmt.Fprintf(stderr, "ERROR objects: %v\n", err)
			return 1
		}
		count, err := objects.Verify()
		if err != nil {
			fmt.Fprintf(stderr, "ERROR objects: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "OK objects: %d verified\n", count)
	}
	if snapshot, err := coverage.Read(paths.WorkspaceSnapshot); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stdout, "WARN coverage: workspace baseline does not exist")
	} else if err != nil {
		fmt.Fprintf(stderr, "ERROR coverage: %v\n", err)
		return 1
	} else {
		current, scanErr := coverage.Scan(paths.Root, snapshot.EventSequence, coverage.Options{})
		if scanErr != nil {
			fmt.Fprintf(stderr, "ERROR coverage: %v\n", scanErr)
			return 1
		}
		if snapshot.Root != current.Root {
			fmt.Fprintf(stderr, "ERROR coverage: workspace snapshot root %q does not match current root %q\n", snapshot.Root, current.Root)
			return 1
		}
		changes := coverage.Compare(snapshot, current)
		if len(changes) > 0 {
			fmt.Fprintf(stdout, "WARN coverage: %d workspace changes await SessionStart reconciliation\n", len(changes))
		} else if snapshot.SkippedFiles > 0 {
			fmt.Fprintf(stdout, "WARN coverage: baseline current, %d files outside scan limits\n", snapshot.SkippedFiles)
		} else {
			fmt.Fprintf(stdout, "OK coverage: workspace baseline current at event sequence %d\n", snapshot.EventSequence)
		}
	}

	if os.Getenv("ACR_DAEMON") == "off" {
		fmt.Fprintln(stdout, "OK daemon: disabled by ACR_DAEMON=off; hooks run directly")
	} else if response, err := daemonCall(daemonRequest{Command: "ping"}, time.Second); err != nil {
		fmt.Fprintln(stdout, "WARN daemon: not running; hooks run directly with the same enforcement, only slower (acr daemon start)")
	} else if response.Build != buildID() {
		fmt.Fprintf(stdout, "WARN daemon: pid %d runs an older acr build and exits on the next hook\n", response.PID)
	} else {
		fmt.Fprintf(stdout, "OK daemon: running (pid %d, %d hooks served)\n", response.PID, response.Served)
	}

	codexConfigured := hookFileContains(filepath.Join(paths.Root, ".codex", "hooks.json"), "codex")
	claudeConfigured := hookFileContains(filepath.Join(paths.Root, ".claude", "settings.json"), "claude")
	if codexConfigured && !hookTimes[protocol.AgentCodex].IsZero() {
		fmt.Fprintf(stdout, "OK codex hook: configured; last recorded an event at %s\n", hookTimes[protocol.AgentCodex].UTC().Format(time.RFC3339))
	} else if codexConfigured {
		fmt.Fprintln(stdout, "WARN codex hook: configured, but trust and live execution are not verified")
	} else {
		fmt.Fprintln(stdout, "WARN codex hook: not found")
	}
	if claudeConfigured && !hookTimes[protocol.AgentClaudeCode].IsZero() {
		fmt.Fprintf(stdout, "OK claude hook: configured; last recorded an event at %s\n", hookTimes[protocol.AgentClaudeCode].UTC().Format(time.RFC3339))
	} else if claudeConfigured {
		fmt.Fprintln(stdout, "WARN claude hook: configured, but live execution is not verified")
	} else {
		fmt.Fprintln(stdout, "WARN claude hook: not found")
	}
	if codexConfigured && claudeConfigured && contractCount > 0 {
		fmt.Fprintln(stdout, "guarantee: L1 candidate; run live probes before relying on enforcement")
	} else {
		fmt.Fprintln(stdout, "guarantee: L0 context/audit only")
	}
	return 0
}

func hookFileContains(path, agent string) bool {
	data, err := os.ReadFile(path)
	if err != nil || !json.Valid(data) {
		return false
	}
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return false
	}
	return containsHookCommand(root, agent)
}

func containsHookCommand(value any, agent string) bool {
	switch typed := value.(type) {
	case map[string]any:
		if command, ok := typed["command"].(string); ok {
			quoted := "'hook' '" + agent + "' 'pre-tool-use'"
			plain := "hook " + agent + " pre-tool-use"
			if strings.Contains(command, quoted) || strings.Contains(command, plain) {
				return true
			}
		}
		for _, child := range typed {
			if containsHookCommand(child, agent) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsHookCommand(child, agent) {
				return true
			}
		}
	}
	return false
}

func runPostToolUseHook(agentName, eventName string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var kind protocol.AgentKind
	switch agentName {
	case "codex":
		kind = protocol.AgentCodex
	case "claude":
		kind = protocol.AgentClaudeCode
	default:
		fmt.Fprintf(stderr, "unsupported hook agent %q; want codex or claude\n", agentName)
		return 2
	}
	if agentName == "codex" && eventName == "post-tool-use-failure" {
		fmt.Fprintln(stderr, "Codex does not expose a separate PostToolUseFailure event")
		return 2
	}

	flags := flag.NewFlagSet("hook "+agentName+" "+eventName, flag.ContinueOnError)
	flags.SetOutput(stderr)
	eventDB := flags.String("event-db", "", "SQLite event database path")
	projectID := flags.String("project-id", "", "ACR project id")
	workspaceID := flags.String("workspace-id", "", "ACR workspace id")
	objectDir := flags.String("object-dir", "", "object store directory for large outputs; defaults to objects next to --event-db")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *eventDB == "" || *projectID == "" {
		fmt.Fprintln(stderr, "post-tool hook requires --event-db and --project-id")
		return 2
	}
	raw, err := readHookInput(stdin)
	if err != nil {
		return writePostHookWarning("ACR could not read post-tool input: "+err.Error(), stdout, stderr)
	}
	input, err := adapter.DecodePostToolUse(raw)
	if err != nil {
		return writePostHookWarning("ACR rejected post-tool input: "+err.Error(), stdout, stderr)
	}
	expectedNativeEvent := "PostToolUse"
	if eventName == "post-tool-use-failure" {
		expectedNativeEvent = "PostToolUseFailure"
	}
	if input.HookEventName != expectedNativeEvent {
		return writePostHookWarning("ACR hook command and native event do not match", stdout, stderr)
	}
	store, release, err := openEvents(*eventDB)
	if err != nil {
		return writePostHookWarning("ACR could not open event store: "+err.Error(), stdout, stderr)
	}
	defer release()
	service, err := acrruntime.NewWithEventStore(nil, store)
	if err != nil {
		return writePostHookWarning("ACR could not initialize outcome recorder: "+err.Error(), stdout, stderr)
	}
	if *objectDir == "" {
		*objectDir = filepath.Join(filepath.Dir(*eventDB), "objects")
	}
	if objects, err := objectstore.Open(*objectDir); err != nil {
		fmt.Fprintln(stderr, "ACR object store unavailable; recording tool output inline: "+err.Error())
	} else {
		service.UseObjectStore(objects, 0)
	}
	action := adapter.NormalizePostToolUse(kind, input, adapter.ProjectContext{ProjectID: *projectID, WorkspaceID: *workspaceID})
	action.Agent.ACRSessionID = os.Getenv("ACR_LAUNCH_ID")
	if err := service.RecordToolOutcome(context.Background(), acrruntime.ToolOutcome{
		Action:      action,
		Response:    input.ToolResponse,
		Failed:      input.HookEventName == "PostToolUseFailure",
		Error:       input.Error,
		IsInterrupt: input.IsInterrupt,
		DurationMS:  input.DurationMS,
	}); err != nil {
		return writePostHookWarning("ACR could not record tool outcome: "+err.Error(), stdout, stderr)
	}
	fmt.Fprintln(stdout, "{}")
	return 0
}

func writePostHookWarning(reason string, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, reason)
	encoded, err := json.Marshal(map[string]string{"systemMessage": reason})
	if err != nil {
		fmt.Fprintf(stderr, "encode post-tool warning: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, string(encoded))
	return 0
}

// hookHistory returns the most recent runtime.launch_ended event, or an empty
// event when there is none, and when each agent's hooks last recorded an
// event. Guarded server events, recorded under the launch ID, are not hooks.
func hookHistory(ctx context.Context, store *eventstore.Store, projectID string) (eventstore.Event, map[protocol.AgentKind]time.Time, error) {
	var last eventstore.Event
	hooks := map[protocol.AgentKind]time.Time{}
	for after := uint64(0); ; {
		events, err := store.List(ctx, projectID, after, 500)
		if err != nil || len(events) == 0 {
			return last, hooks, err
		}
		for _, event := range events {
			after = event.Sequence
			if event.Type == "runtime.launch_ended" {
				last = event
			}
			if event.Actor.NativeSessionID != "" && event.Actor.NativeSessionID != event.Actor.ACRSessionID && event.Actor.AgentKind != "" {
				hooks[event.Actor.AgentKind] = event.OccurredAt
			}
		}
	}
}

func runInit(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	name := flags.String("name", "", "project name")
	dryRun := flags.Bool("dry-run", false, "preview without writing files")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "init accepts no positional arguments")
		return 2
	}
	config, err := project.NewConfig(*root, *name)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	paths, err := project.ResolvePaths(*root)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	if *dryRun {
		report, err := bootstrap.Discover(paths.Root, bootstrap.Options{})
		if err != nil {
			fmt.Fprintf(stderr, "discover bootstrap sources: %v\n", err)
			return 1
		}
		encoded, err := project.Encode(config)
		if err != nil {
			fmt.Fprintf(stderr, "encode project preview: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "would create %s\n%s", paths.Config, encoded)
		fmt.Fprintf(stdout, "would create directory %s\n", paths.Contracts)
		fmt.Fprintf(stdout, "would create directory %s\n", paths.State)
		fmt.Fprintf(stdout, "would index %d bootstrap sources (%d skipped, %d invalid)\n", report.Indexed, report.Skipped, report.Invalid)
		return 0
	}
	report, err := bootstrap.Discover(paths.Root, bootstrap.Options{})
	if err != nil {
		fmt.Fprintf(stderr, "discover bootstrap sources: %v\n", err)
		return 1
	}
	paths, err = project.Initialize(*root, config)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	snapshotPath := filepath.Join(paths.State, "bootstrap-sources.json")
	if err := bootstrap.WriteSnapshot(snapshotPath, report); err != nil {
		fmt.Fprintf(stderr, "project initialized but bootstrap snapshot failed: %v\n", err)
		return 1
	}
	workspaceSnapshot, err := coverage.Scan(paths.Root, 0, coverage.Options{})
	if err != nil {
		fmt.Fprintf(stderr, "project initialized but workspace baseline failed: %v\n", err)
		return 1
	}
	if err := coverage.Write(paths.WorkspaceSnapshot, workspaceSnapshot); err != nil {
		fmt.Fprintf(stderr, "project initialized but workspace baseline failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "initialized ACR project %s\n", config.ProjectID)
	fmt.Fprintf(stdout, "config: %s\n", paths.Config)
	fmt.Fprintf(stdout, "contracts: %s\n", paths.Contracts)
	fmt.Fprintf(stdout, "event database: %s\n", paths.EventDB)
	fmt.Fprintf(stdout, "memory database: %s\n", paths.MemoryDB)
	fmt.Fprintf(stdout, "bootstrap: %d indexed, %d skipped, %d invalid (%s)\n", report.Indexed, report.Skipped, report.Invalid, snapshotPath)
	return 0
}

func runEventList(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("event list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String("db", "", "SQLite event database path")
	projectID := flags.String("project-id", "", "ACR project id")
	after := flags.Uint64("after", 0, "return events after this sequence")
	limit := flags.Int("limit", 100, "maximum events to return")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *databasePath == "" || *projectID == "" {
		fmt.Fprintln(stderr, "event list requires --db and --project-id")
		return 2
	}
	store, err := eventstore.Open(*databasePath)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	defer store.Close()
	events, err := store.List(context.Background(), *projectID, *after, *limit)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(events); err != nil {
		fmt.Fprintf(stderr, "encode events: %v\n", err)
		return 1
	}
	return 0
}

func runIntegrityVerify(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("integrity verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String("db", "", "SQLite event database path")
	projectID := flags.String("project-id", "", "ACR project id")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *databasePath == "" || *projectID == "" {
		fmt.Fprintln(stderr, "integrity verify requires --db and --project-id")
		return 2
	}
	store, err := eventstore.Open(*databasePath)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	defer store.Close()
	if err := store.Verify(context.Background(), *projectID); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "valid: project %s event chain\n", *projectID)
	return 0
}

func runPreToolUseHook(agentName string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var kind protocol.AgentKind
	switch agentName {
	case "codex":
		kind = protocol.AgentCodex
	case "claude":
		kind = protocol.AgentClaudeCode
	default:
		fmt.Fprintf(stderr, "unsupported hook agent %q; want codex or claude\n", agentName)
		return 2
	}

	flags := flag.NewFlagSet("hook "+agentName+" pre-tool-use", flag.ContinueOnError)
	flags.SetOutput(stderr)
	contractsPath := flags.String("contracts", "", "contract JSON file or directory")
	projectID := flags.String("project-id", "", "ACR project id")
	workspaceID := flags.String("workspace-id", "", "ACR workspace id")
	eventDB := flags.String("event-db", "", "SQLite event database path")
	root := flags.String("root", "", "project root for evidence checks; defaults to the nearest .acr project above cwd")
	memoryDB := flags.String("memory-db", "", "memory database for correction notices; defaults to memories.db next to --event-db")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *contractsPath == "" {
		fmt.Fprintln(stderr, "pre-tool-use requires --contracts and accepts no positional arguments")
		return 2
	}
	if *eventDB != "" && *projectID == "" {
		fmt.Fprintln(stderr, "pre-tool-use requires --project-id when --event-db is set")
		return 2
	}

	var service *acrruntime.Service
	var store *eventstore.Store
	var err error
	if *eventDB == "" {
		service, err = acrruntime.NewFromPath(*contractsPath)
	} else {
		var release func()
		store, release, err = openEvents(*eventDB)
		if err == nil {
			defer release()
			var contracts []contract.Contract
			if contracts, err = registry.Load(context.Background(), store, *projectID, *contractsPath); err == nil {
				service, err = acrruntime.NewWithEventStore(contracts, store)
			}
		}
	}
	if err != nil {
		return writeHookFailure(kind, "ACR could not load contracts: "+err.Error(), stdout, stderr)
	}
	raw, err := readHookInput(stdin)
	if err != nil {
		return writeHookFailure(kind, "ACR could not read hook input: "+err.Error(), stdout, stderr)
	}
	input, err := adapter.DecodePreToolUse(raw)
	if err != nil {
		return writeHookFailure(kind, "ACR rejected hook input: "+err.Error(), stdout, stderr)
	}
	action := adapter.NormalizePreToolUse(kind, input, adapter.ProjectContext{
		ProjectID:   *projectID,
		WorkspaceID: *workspaceID,
	})
	action.Agent.ACRSessionID = os.Getenv("ACR_LAUNCH_ID")
	if os.Getenv("ACR_UNATTENDED") == "1" {
		if action.Attributes == nil {
			action.Attributes = map[string]string{}
		}
		action.Attributes["unattended"] = "true"
	}
	if *root == "" {
		*root = projectRootFrom(input.CWD)
	}
	if absolute, err := filepath.Abs(*root); err == nil {
		*root = absolute
	}
	action.Workspace.Root = *root
	if store != nil {
		service.UseEvidence(&verification.Checker{Store: store, Root: *root})
		service.UseAuthorizer(directive.Grants{Events: store, ProjectID: *projectID})
	}
	decision, err := service.Evaluate(action)
	if err != nil {
		return writeHookFailure(kind, "ACR evaluation failed: "+err.Error(), stdout, stderr)
	}
	// Memory corrections ride along with calls that proceed unchanged; other
	// decisions keep them for a later call so none is lost.
	var corrections contextpack.Corrections
	if store != nil && decision.Kind == protocol.DecisionPass {
		corrections, err = sessionCorrections(store, *eventDB, *memoryDB, *projectID, input.SessionID)
		if err != nil {
			fmt.Fprintln(stderr, "ACR memory correction warning: "+err.Error())
		}
		if corrections.Text != "" {
			decision.AdditionalContext = strings.TrimSpace(decision.AdditionalContext + "\n\n" + corrections.Text)
		}
	}
	encoded, err := encodeHookDecision(kind, decision)
	if err != nil {
		return writeHookFailure(kind, "ACR could not encode its decision: "+err.Error(), stdout, stderr)
	}
	if _, err := fmt.Fprintln(stdout, string(encoded)); err != nil {
		fmt.Fprintf(stderr, "write hook output: %v\n", err)
		return 1
	}
	if err := contextpack.RecordDelivery(context.Background(), store, *projectID, *workspaceID, kind, input.SessionID, "PreToolUse", corrections); err != nil {
		fmt.Fprintln(stderr, err)
	}
	return 0
}

// projectRootFrom returns the nearest directory at or above cwd that holds an
// ACR project, or cwd itself.
func projectRootFrom(cwd string) string {
	for directory := filepath.Clean(cwd); ; {
		if _, err := os.Stat(filepath.Join(directory, ".acr", "project.json")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return cwd
		}
		directory = parent
	}
}

func readHookInput(reader io.Reader) ([]byte, error) {
	const maximum = 4 << 20
	limited := io.LimitReader(reader, maximum+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(data) > maximum {
		return nil, fmt.Errorf("input exceeds %d bytes", maximum)
	}
	return data, nil
}

func encodeHookDecision(kind protocol.AgentKind, decision protocol.Decision) ([]byte, error) {
	if kind == protocol.AgentCodex {
		return codex.EncodePreToolUseDecision(decision)
	}
	return claude.EncodePreToolUseDecision(decision)
}

func writeHookFailure(kind protocol.AgentKind, reason string, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, reason)
	decision := protocol.NewDecision(protocol.DecisionDeny, reason)
	encoded, err := encodeHookDecision(kind, decision)
	if err != nil {
		fmt.Fprintf(stderr, "encode fail-closed hook output: %v\n", err)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, string(encoded)); err != nil {
		fmt.Fprintf(stderr, "write fail-closed hook output: %v\n", err)
		return 1
	}
	return 0
}

func runEvaluate(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("evaluate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	contractsPath := flags.String("contracts", "", "contract JSON file or directory")
	actionPath := flags.String("action", "-", "action JSON file, or - for stdin")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *contractsPath == "" {
		fmt.Fprintln(stderr, "evaluate requires --contracts and accepts no positional arguments")
		return 2
	}

	service, err := acrruntime.NewFromPath(*contractsPath)
	if err != nil {
		fmt.Fprintf(stderr, "load runtime: %v\n", err)
		return 1
	}

	reader := stdin
	var file *os.File
	if *actionPath != "-" {
		file, err = os.Open(*actionPath)
		if err != nil {
			fmt.Fprintf(stderr, "open action %q: %v\n", *actionPath, err)
			return 1
		}
		defer file.Close()
		reader = file
	}
	action, err := acrruntime.DecodeAction(reader)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	decision, err := service.Evaluate(action)
	if err != nil {
		fmt.Fprintf(stderr, "evaluate action: %v\n", err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(decision); err != nil {
		fmt.Fprintf(stderr, "encode decision: %v\n", err)
		return 1
	}
	return 0
}

func runContractValidate(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "contract validate requires exactly one file or directory")
		return 2
	}
	values, err := contract.LoadPath(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	if len(values) == 0 {
		fmt.Fprintln(stderr, "contract validation requires at least one .json contract")
		return 1
	}
	if _, err := acrruntime.New(values); err != nil {
		fmt.Fprintf(stderr, "validate contract set: %v\n", err)
		return 1
	}
	label := "contracts"
	if len(values) == 1 {
		label = "contract"
	}
	fmt.Fprintf(stdout, "valid: %d %s\n", len(values), label)
	return 0
}

func writeUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage:")
	fmt.Fprintln(writer, "  acr version")
	fmt.Fprintln(writer, "  acr init [--root <path>] [--name <name>] [--dry-run]")
	fmt.Fprintln(writer, "  acr doctor [--root <path>]")
	fmt.Fprintln(writer, "  acr ui [--root <path>] [--listen 127.0.0.1:7480] [--no-open]")
	fmt.Fprintln(writer, "  acr install-hooks --agent <codex|claude|all> [--root <path>] [--binary <path>] [--dry-run|--apply]")
	fmt.Fprintln(writer, "  acr contract validate <file-or-directory>")
	fmt.Fprintln(writer, "  acr contract activate <file> [--root <path>] [--apply]")
	fmt.Fprintln(writer, "  acr contract retire <id> --reason <text> [--root <path>] [--apply]")
	fmt.Fprintln(writer, "  acr contract list [--root <path>]")
	fmt.Fprintln(writer, "  acr contract simulate <file> --cases <file>")
	fmt.Fprintln(writer, "  acr candidate propose [--root <path>] [--min-occurrences <n>]")
	fmt.Fprintln(writer, "  acr candidate list [--root <path>] [--all]")
	fmt.Fprintln(writer, "  acr candidate reject <id> --reason <text> [--root <path>]")
	fmt.Fprintln(writer, "  acr candidate accept <id> [--root <path>] [--apply]")
	fmt.Fprintln(writer, "  acr evaluate --contracts <file-or-directory> [--action <file|->]")
	fmt.Fprintln(writer, "  acr hook <codex|claude> pre-tool-use --contracts <path> [--project-id <id>] [--workspace-id <id>] [--event-db <path>] [--root <path>]")
	fmt.Fprintln(writer, "  acr hook <codex|claude> session-start --contracts <path> --event-db <path> --project-id <id> [--workspace-id <id>] [--root <path>] [--memory-db <path>] [--projection-db <path>]")
	fmt.Fprintln(writer, "  acr hook <codex|claude> user-prompt-submit --event-db <path> --project-id <id> [--workspace-id <id>] [--root <path>] [--projection-db <path>]")
	fmt.Fprintln(writer, "  acr hook <codex|claude> post-tool-use --event-db <path> --project-id <id> [--workspace-id <id>] [--object-dir <path>]")
	fmt.Fprintln(writer, "  acr hook claude post-tool-use-failure --event-db <path> --project-id <id> [--workspace-id <id>] [--object-dir <path>]")
	fmt.Fprintln(writer, "  acr event list --db <path> --project-id <id> [--after <n>] [--limit <n>]")
	fmt.Fprintln(writer, "  acr integrity verify --db <path> --project-id <id>")
	fmt.Fprintln(writer, "  acr bootstrap sources [--root <path>] [--max-file-bytes <n>] [--max-total-bytes <n>] [--max-sources <n>]")
	fmt.Fprintln(writer, "  acr memory <add|list|show|history|activate|confirm|stale|contest|contest-pair|correct|retract|archive> ...")
	fmt.Fprintln(writer, "  acr reconcile status [--root <path>]")
	fmt.Fprintln(writer, "  acr reconcile accept --event-id <id> --resolution <kind> --reason <text> [--root <path>]")
	fmt.Fprintln(writer, "  acr repair run --plan <file> [--root <path>] [--apply]")
	fmt.Fprintln(writer, "  acr repair status --plan <file> [--root <path>]")
	fmt.Fprintln(writer, "  acr repair resolve --plan <file> --step <id> --outcome succeeded|failed --reason <text> [--root <path>] [--apply]")
	fmt.Fprintln(writer, "  acr verification list [--root <path>]")
	fmt.Fprintln(writer, "  acr launch [--root <path>] [--mode native|guarded] [--binary <path>] <claude|codex> [-- <agent args>]")
	fmt.Fprintln(writer, "  acr mcp serve [--root <path>] [--agent claude|codex]")
	fmt.Fprintln(writer, "  acr replay (--session <id>|--correlation <id>|--tool-use <id>|--all) [--root <path>] [--limit <n>] [--with-objects] [--format text|json]")
}
