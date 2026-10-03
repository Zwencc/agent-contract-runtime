// Package guarded executes agent tool calls on ACR's side of the boundary.
// Each call is evaluated against the active contracts inside ACR before
// anything runs, so a contract can never fail open because a host hook
// errored or timed out, and missing prerequisite evidence can be produced
// by ACR itself before the original call proceeds.
package guarded

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/coverage"
	"github.com/agent-contract-runtime/acr/internal/evaluator"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/objectstore"
	"github.com/agent-contract-runtime/acr/internal/protocol"
	"github.com/agent-contract-runtime/acr/internal/registry"
	"github.com/agent-contract-runtime/acr/internal/repair"
	acrruntime "github.com/agent-contract-runtime/acr/internal/runtime"
	"github.com/agent-contract-runtime/acr/internal/verification"
)

// Tool names and the native tools whose contracts they share.
const (
	ToolExec      = "exec"
	ToolWriteFile = "write_file"
	ToolEditFile  = "edit_file"

	defaultTimeoutSeconds = 120
	maximumFileBytes      = 8 << 20
)

var nativeNames = map[string]string{ToolExec: "Bash", ToolWriteFile: "Write", ToolEditFile: "Edit"}

type Executor struct {
	Root          string
	ProjectID     string
	WorkspaceID   string
	Agent         protocol.AgentKind
	SessionID     string
	ContractsPath string
	Events        *eventstore.Store
	Objects       *objectstore.Store
	LockDir       string
	Commands      repair.Executor
	// Authorizer, when set, lets the user lift contracts for one turn.
	Authorizer evaluator.Authorizer
}

type Result struct {
	Text    string
	IsError bool
}

type execInput struct {
	Command        string `json:"command"`
	CWD            string `json:"cwd,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type writeInput struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

type editInput struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// Call evaluates and, when allowed, executes one tool call. Any failure
// before execution leaves the workspace untouched.
func (executor Executor) Call(ctx context.Context, tool string, arguments json.RawMessage) Result {
	native, ok := nativeNames[tool]
	if !ok {
		return failure("unknown tool %q", tool)
	}
	input, resources, err := executor.normalize(tool, arguments)
	if err != nil {
		return failure("ACR rejected the %s arguments: %v", tool, err)
	}
	action := protocol.NewAction(
		protocol.AgentContext{Kind: executor.Agent, NativeSessionID: executor.SessionID, ACRSessionID: executor.SessionID},
		protocol.WorkspaceContext{CWD: executor.Root, Root: executor.Root, ProjectID: executor.ProjectID, WorkspaceID: executor.WorkspaceID},
		protocol.ToolCall{Name: native, Input: input},
	)
	action.OperationKind = map[string]string{ToolExec: "process.exec", ToolWriteFile: "filesystem.write", ToolEditFile: "filesystem.write"}[tool]
	action.Resources = resources

	decision, notes, err := executor.decide(ctx, action)
	if err != nil {
		return failure("ACR could not evaluate the call, so it did not run: %v", err)
	}
	switch decision.Kind {
	case protocol.DecisionPass:
	case protocol.DecisionRewrite:
		// A correction is held to the same argument and path rules as the
		// original call.
		corrected, resources, err := executor.normalize(tool, decision.EffectiveInput)
		if err != nil {
			return failure("ACR's correction of the call is not allowed, so it did not run: %v", err)
		}
		action.Tool.Input, action.Resources = corrected, resources
		notes = append(notes, "ACR corrected the call before running it: "+decision.Reason)
	case protocol.DecisionAsk:
		return failure("ACR needs human confirmation, which guarded mode cannot collect, so the call did not run: %s", decision.Reason)
	default:
		return failure("ACR denied the call: %s", strings.Join(append(notes, decision.Reason), "\n"))
	}

	// ACR runs commands itself, so it can see exactly which files a command
	// changed and record them as the explanation for those changes.
	var before *coverage.Snapshot
	if tool == ToolExec {
		if snapshot, err := coverage.Scan(executor.Root, 0, coverage.Options{}); err == nil {
			before = &snapshot
		} else {
			notes = append(notes, "warning: changed files could not be observed: "+err.Error())
		}
	}
	response, text, failed, err := executor.execute(ctx, tool, action.Tool.Input)
	if err != nil {
		return failure("ACR could not run the call: %v", err)
	}
	var observed []string
	if before != nil {
		if after, err := coverage.Scan(executor.Root, 0, coverage.Options{}); err == nil {
			for _, change := range coverage.Compare(*before, after) {
				observed = append(observed, change.Path)
				action.Resources = append(action.Resources, filepath.Join(executor.Root, filepath.FromSlash(change.Path)))
			}
		} else {
			notes = append(notes, "warning: changed files could not be observed: "+err.Error())
		}
	}
	if recordErr := executor.record(ctx, action, response, failed, observed); recordErr != nil {
		notes = append(notes, "warning: the outcome was not recorded: "+recordErr.Error())
	}
	if decision.AdditionalContext != "" {
		notes = append(notes, decision.AdditionalContext)
	}
	if len(notes) > 0 {
		text = strings.Join(notes, "\n") + "\n\n" + text
	}
	return Result{Text: text, IsError: failed}
}

func failure(format string, args ...any) Result {
	return Result{Text: fmt.Sprintf(format, args...), IsError: true}
}

// decide evaluates the action. When it is denied for missing evidence that
// a repeatable repair plan can produce, ACR runs those plans and evaluates
// once more; the original call is never retried more than that.
func (executor Executor) decide(ctx context.Context, action protocol.Action) (protocol.Decision, []string, error) {
	contracts, err := registry.Load(ctx, executor.Events, executor.ProjectID, executor.ContractsPath)
	if err != nil {
		return protocol.Decision{}, nil, err
	}
	evaluate := func() (protocol.Decision, error) {
		service, err := acrruntime.NewWithEventStore(contracts, executor.Events)
		if err != nil {
			return protocol.Decision{}, err
		}
		service.UseEvidence(&verification.Checker{Store: executor.Events, Root: executor.Root})
		if executor.Authorizer != nil {
			service.UseAuthorizer(executor.Authorizer)
		}
		return service.EvaluateContext(ctx, action)
	}
	decision, err := evaluate()
	if err != nil || decision.Kind != protocol.DecisionDeny {
		return decision, nil, err
	}
	notes, ran, err := executor.produceEvidence(ctx, contracts, action, decision)
	if err != nil {
		return protocol.Decision{}, notes, err
	}
	if !ran {
		return decision, notes, nil
	}
	decision, err = evaluate()
	return decision, notes, err
}

func (executor Executor) produceEvidence(ctx context.Context, contracts []contract.Contract, action protocol.Action, decision protocol.Decision) ([]string, bool, error) {
	matched := map[string]bool{}
	for _, match := range decision.Matches {
		matched[fmt.Sprintf("%s@%d", match.ContractID, match.ContractVersion)] = true
	}
	checker := &verification.Checker{Store: executor.Events, Root: executor.Root}
	notes := []string{}
	ran := false
	seen := map[string]bool{}
	for _, value := range contracts {
		if value.Verifiers == nil || !matched[fmt.Sprintf("%s@%d", value.Metadata.ID, value.Metadata.Version)] {
			continue
		}
		for _, requirement := range value.Verifiers.Evidence {
			if requirement.Plan == "" || seen[requirement.Plan] {
				continue
			}
			satisfied, _, err := checker.Satisfied(action, requirement.Subject)
			if err != nil {
				return notes, ran, err
			}
			if satisfied {
				continue
			}
			seen[requirement.Plan] = true
			plan, err := loadPlan(filepath.Join(executor.Root, filepath.FromSlash(requirement.Plan)))
			if err != nil {
				return notes, ran, err
			}
			if !plan.Repeatable {
				notes = append(notes, fmt.Sprintf("ACR did not run plan %s because it is not a repeatable verification plan.", requirement.Plan))
				continue
			}
			runner := repair.Runner{Store: executor.Events, ProjectID: executor.ProjectID, WorkspaceID: executor.WorkspaceID,
				Root: executor.Root, LockDir: executor.LockDir, Executor: executor.Commands}
			if _, err := runner.Run(ctx, plan); err != nil {
				notes = append(notes, fmt.Sprintf("ACR ran prerequisite plan %s and it failed: %v", requirement.Plan, err))
				continue
			}
			ran = true
			notes = append(notes, fmt.Sprintf("ACR ran prerequisite plan %s to produce %s.", requirement.Plan, requirement.Subject))
		}
	}
	return notes, ran, nil
}

func loadPlan(path string) (repair.Plan, error) {
	file, err := os.Open(path)
	if err != nil {
		return repair.Plan{}, fmt.Errorf("open prerequisite plan: %w", err)
	}
	defer file.Close()
	return repair.Decode(file)
}

// normalize validates arguments and returns the native-shaped tool input,
// with file paths made absolute inside the project root.
func (executor Executor) normalize(tool string, arguments json.RawMessage) (json.RawMessage, []string, error) {
	decode := func(target any) error {
		decoder := json.NewDecoder(strings.NewReader(string(arguments)))
		decoder.DisallowUnknownFields()
		return decoder.Decode(target)
	}
	switch tool {
	case ToolExec:
		var input execInput
		if err := decode(&input); err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(input.Command) == "" {
			return nil, nil, errors.New("command is required")
		}
		if input.TimeoutSeconds == 0 {
			input.TimeoutSeconds = defaultTimeoutSeconds
		}
		if input.TimeoutSeconds < 1 || input.TimeoutSeconds > 600 {
			return nil, nil, errors.New("timeout_seconds must be 1 through 600")
		}
		encoded, err := json.Marshal(input)
		return encoded, nil, err
	case ToolWriteFile:
		var input writeInput
		if err := decode(&input); err != nil {
			return nil, nil, err
		}
		if len(input.Content) > maximumFileBytes {
			return nil, nil, fmt.Errorf("content exceeds %d bytes", maximumFileBytes)
		}
		target, err := executor.insideRoot(input.FilePath)
		if err != nil {
			return nil, nil, err
		}
		input.FilePath = target
		encoded, err := json.Marshal(input)
		return encoded, []string{target}, err
	default:
		var input editInput
		if err := decode(&input); err != nil {
			return nil, nil, err
		}
		if input.OldString == "" || input.OldString == input.NewString {
			return nil, nil, errors.New("old_string must be non-empty and differ from new_string")
		}
		target, err := executor.insideRoot(input.FilePath)
		if err != nil {
			return nil, nil, err
		}
		input.FilePath = target
		encoded, err := json.Marshal(input)
		return encoded, []string{target}, err
	}
}

// insideRoot resolves a file path against the project root and refuses
// anything that leaves it, including through symlinked directories, a
// symlink at the target itself, or ACR's own state directory. Root must be
// symlink-resolved.
func (executor Executor) insideRoot(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("file_path is required")
	}
	target := value
	if !filepath.IsAbs(target) {
		target = filepath.Join(executor.Root, target)
	}
	target = filepath.Clean(target)
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is a symlink; guarded writes do not follow symlinks", value)
	}
	ancestor := filepath.Dir(target)
	for {
		if _, err := os.Lstat(ancestor); err == nil || filepath.Dir(ancestor) == ancestor {
			break
		}
		ancestor = filepath.Dir(ancestor)
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", value, err)
	}
	remainder, err := filepath.Rel(ancestor, target)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", value, err)
	}
	full := filepath.Join(resolved, remainder)
	relative, err := filepath.Rel(executor.Root, full)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the project root", value)
	}
	if first := strings.Split(filepath.ToSlash(relative), "/")[0]; first == ".acr" {
		return "", fmt.Errorf("%s is ACR state and cannot be written by agents", value)
	}
	return full, nil
}

func (executor Executor) execute(ctx context.Context, tool string, raw json.RawMessage) (json.RawMessage, string, bool, error) {
	switch tool {
	case ToolExec:
		var input execInput
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, "", false, err
		}
		outcome, err := executor.Commands.Execute(ctx, executor.Root, repair.Step{ID: "guarded-exec",
			Argv: []string{"/bin/sh", "-c", input.Command}, CWD: input.CWD, TimeoutSeconds: input.TimeoutSeconds, AllowedExitCodes: []int{0}})
		if err != nil {
			return nil, "", false, err
		}
		response, _ := json.Marshal(outcome)
		text := fmt.Sprintf("exit code %d", outcome.ExitCode)
		if outcome.TimedOut {
			text += " (timed out)"
		}
		text += "\nstdout:\n" + outcome.Stdout + "\nstderr:\n" + outcome.Stderr
		return response, text, outcome.ExitCode != 0 || outcome.TimedOut, nil
	case ToolWriteFile:
		var input writeInput
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, "", false, err
		}
		if err := writeFile(input.FilePath, []byte(input.Content)); err != nil {
			return nil, "", false, err
		}
		response, _ := json.Marshal(map[string]any{"file_path": input.FilePath, "bytes": len(input.Content)})
		return response, fmt.Sprintf("wrote %d bytes to %s", len(input.Content), input.FilePath), false, nil
	default:
		var input editInput
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, "", false, err
		}
		data, err := os.ReadFile(input.FilePath)
		if err != nil {
			return nil, "", false, err
		}
		count := strings.Count(string(data), input.OldString)
		if count == 0 || (count > 1 && !input.ReplaceAll) {
			response, _ := json.Marshal(map[string]any{"file_path": input.FilePath, "matches": count})
			return response, fmt.Sprintf("old_string occurs %d times in %s; nothing was changed (set replace_all to replace every occurrence)", count, input.FilePath), true, nil
		}
		updated := strings.Replace(string(data), input.OldString, input.NewString, map[bool]int{true: -1, false: 1}[input.ReplaceAll])
		if err := writeFile(input.FilePath, []byte(updated)); err != nil {
			return nil, "", false, err
		}
		response, _ := json.Marshal(map[string]any{"file_path": input.FilePath, "replacements": map[bool]int{true: count, false: 1}[input.ReplaceAll]})
		return response, fmt.Sprintf("edited %s", input.FilePath), false, nil
	}
}

// writeFile replaces path atomically, keeping an existing file's mode.
func writeFile(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".acr-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (executor Executor) record(ctx context.Context, action protocol.Action, response json.RawMessage, failed bool, observed []string) error {
	service, err := acrruntime.NewWithEventStore(nil, executor.Events)
	if err != nil {
		return err
	}
	if executor.Objects != nil {
		service.UseObjectStore(executor.Objects, 0)
	}
	outcome := acrruntime.ToolOutcome{Action: action, Response: response, Failed: failed, ObservedChanges: observed}
	if failed {
		outcome.Error = "guarded call reported failure"
	}
	return service.RecordToolOutcome(ctx, outcome)
}
