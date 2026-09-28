package repair

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/redaction"
	"github.com/agent-contract-runtime/acr/internal/verification"
)

type Executor interface {
	Execute(ctx context.Context, root string, step Step) (Outcome, error)
}

type Runner struct {
	Store       *eventstore.Store
	ProjectID   string
	WorkspaceID string
	Root        string
	LockDir     string
	Executor    Executor
	Now         func() time.Time

	// correlation ties every event of one run to its idempotency key, so the
	// whole transaction can be replayed with acr replay --correlation.
	correlation string
}

// prepare validates the plan and fills runner defaults for one operation.
func (runner Runner) prepare(plan Plan) (Runner, string, error) {
	if runner.Store == nil || runner.Executor == nil || strings.TrimSpace(runner.ProjectID) == "" {
		return runner, "", errors.New("repair runner requires event store, executor, and project id")
	}
	root, err := filepath.Abs(runner.Root)
	if err != nil {
		return runner, "", fmt.Errorf("resolve repair root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return runner, "", fmt.Errorf("resolve repair root symlinks: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return runner, "", err
	}
	if runner.Now == nil {
		runner.Now = time.Now
	}
	runner.correlation = plan.IdempotencyKey
	if runner.LockDir == "" {
		runner.LockDir = filepath.Join(root, ".acr", "state", "repair-locks")
	}
	return runner, root, nil
}

// Run executes the plan, or resumes an interrupted run of the same plan when
// every step it started has a recorded outcome. A step that started without
// an outcome is never guessed: it must first be settled with Resolve.
func (runner Runner) Run(ctx context.Context, plan Plan) (Result, error) {
	runner, root, err := runner.prepare(plan)
	if err != nil {
		return Result{}, err
	}
	release, err := acquireLocks(runner.LockDir, plan)
	if err != nil {
		return Result{}, err
	}
	defer release()
	state, err := runner.loadState(ctx, plan.IdempotencyKey)
	if err != nil {
		return Result{}, err
	}
	planHash, err := plan.Hash()
	if err != nil {
		return Result{}, err
	}
	resume := false
	switch state.run {
	case RunCompleted:
		if !plan.Repeatable {
			return Result{}, ErrAlreadyCompleted
		}
	case RunFailed:
		if !plan.Repeatable {
			return Result{}, ErrAlreadyFailed
		}
	case RunInProgress:
		if state.planHash != planHash {
			return Result{}, fmt.Errorf("repair %q is interrupted and the plan differs from the one that started: %w", plan.ID, ErrRecoveryRequired)
		}
		if step := state.unknownStep(); step != "" {
			return Result{}, fmt.Errorf("repair step %q started but has no recorded outcome; settle it with acr repair resolve: %w", step, ErrRecoveryRequired)
		}
		resume = true
	}

	completed := []string{}
	if resume {
		completed = append(completed, state.completed...)
		resumedPayload, _ := json.Marshal(map[string]any{"plan_id": plan.ID, "idempotency_key": plan.IdempotencyKey, "plan_hash": planHash, "completed_steps": completed})
		if _, err := runner.append(ctx, "repair.resumed", resumedPayload); err != nil {
			return Result{}, err
		}
	} else {
		startedPayload, _ := json.Marshal(map[string]any{"plan_id": plan.ID, "idempotency_key": plan.IdempotencyKey, "plan_hash": planHash, "resource_keys": plan.ResourceKeys, "step_count": len(plan.Steps)})
		if _, err := runner.append(ctx, "repair.started", startedPayload); err != nil {
			return Result{}, err
		}
	}
	for index, step := range plan.Steps {
		if resume && state.steps[step.ID] == StepCompleted {
			continue
		}
		stepPayload, _ := json.Marshal(map[string]any{"plan_id": plan.ID, "idempotency_key": plan.IdempotencyKey, "step_id": step.ID, "step_index": index, "argv": step.Argv, "cwd": step.CWD})
		stepPayload, err = redaction.JSON(stepPayload)
		if err != nil {
			return Result{}, err
		}
		if _, err := runner.append(ctx, "repair.step_started", stepPayload); err != nil {
			return Result{}, err
		}
		var inputsBefore verification.Fingerprint
		if step.Verification != nil {
			// A check whose inputs cannot be fingerprinted proves nothing, so
			// it is not run at all.
			if inputsBefore, err = fingerprint(root, step.Verification.Inputs); err != nil {
				failedPayload, _ := json.Marshal(map[string]any{"plan_id": plan.ID, "idempotency_key": plan.IdempotencyKey, "step_id": step.ID, "step_index": index, "argv": step.Argv, "cwd": step.CWD, "executor_error": "fingerprint verification inputs: " + err.Error()})
				if _, appendErr := runner.append(ctx, "repair.failed", failedPayload); appendErr != nil {
					return Result{}, appendErr
				}
				stepErr := fmt.Errorf("repair step %q: fingerprint verification inputs: %w", step.ID, err)
				return Result{}, errors.Join(stepErr, runner.compensate(ctx, root, plan, completed))
			}
		}
		outcome, executeErr := runner.Executor.Execute(ctx, root, step)
		outcomePayload, _ := json.Marshal(map[string]any{"plan_id": plan.ID, "idempotency_key": plan.IdempotencyKey, "step_id": step.ID, "step_index": index, "argv": step.Argv, "cwd": step.CWD, "outcome": outcome, "executor_error": errorText(executeErr)})
		outcomePayload, err = redaction.JSON(outcomePayload)
		if err != nil {
			return Result{}, err
		}
		succeeded := executeErr == nil && !outcome.TimedOut && allows(step.AllowedExitCodes, outcome.ExitCode)
		if !succeeded {
			failedEvent, appendErr := runner.append(ctx, "repair.failed", outcomePayload)
			if appendErr != nil {
				return Result{}, appendErr
			}
			if step.Verification != nil && executeErr == nil {
				if err := runner.recordVerification(ctx, root, plan, step, outcome, inputsBefore, false, failedEvent.EventID); err != nil {
					return Result{}, err
				}
			}
			stepErr := fmt.Errorf("repair step %q exited with code %d", step.ID, outcome.ExitCode)
			if executeErr != nil {
				stepErr = fmt.Errorf("repair step %q execution failed: %w", step.ID, executeErr)
			}
			return Result{}, errors.Join(stepErr, runner.compensate(ctx, root, plan, completed))
		}
		completedEvent, err := runner.append(ctx, "repair.step_completed", outcomePayload)
		if err != nil {
			return Result{}, err
		}
		completed = append(completed, step.ID)
		if step.Verification != nil {
			if err := runner.recordVerification(ctx, root, plan, step, outcome, inputsBefore, true, completedEvent.EventID); err != nil {
				return Result{}, err
			}
		}
	}
	completedPayload, _ := json.Marshal(map[string]any{"plan_id": plan.ID, "idempotency_key": plan.IdempotencyKey, "completed_steps": len(plan.Steps)})
	terminal, err := runner.append(ctx, "repair.completed", completedPayload)
	if err != nil {
		return Result{}, err
	}
	return Result{PlanID: plan.ID, IdempotencyKey: plan.IdempotencyKey, CompletedSteps: len(plan.Steps), TerminalEventID: terminal.EventID}, nil
}

// Resolve records a human judgement for a step that started without a
// recorded outcome. A step settled as failed fails the run and triggers the
// declared compensations of the steps that completed before it.
func (runner Runner) Resolve(ctx context.Context, plan Plan, stepID string, succeeded bool, reason string) error {
	runner, root, err := runner.prepare(plan)
	if err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		return errors.New("repair resolve requires a reason")
	}
	release, err := acquireLocks(runner.LockDir, plan)
	if err != nil {
		return err
	}
	defer release()
	state, err := runner.loadState(ctx, plan.IdempotencyKey)
	if err != nil {
		return err
	}
	if state.run != RunInProgress || state.steps[stepID] != StepUnknown {
		return fmt.Errorf("repair %q step %q has no unknown outcome to resolve", plan.ID, stepID)
	}
	outcome := "failed"
	if succeeded {
		outcome = "succeeded"
	}
	payload, _ := json.Marshal(map[string]any{"plan_id": plan.ID, "idempotency_key": plan.IdempotencyKey, "step_id": stepID, "outcome": outcome, "reason": reason})
	if payload, err = redaction.JSON(payload); err != nil {
		return err
	}
	if _, err := runner.appendAs(ctx, "user", "repair.step_resolved", payload); err != nil {
		return err
	}
	if succeeded {
		return nil
	}
	failedPayload, _ := json.Marshal(map[string]any{"plan_id": plan.ID, "idempotency_key": plan.IdempotencyKey, "step_id": stepID, "resolution": "manual"})
	if _, err := runner.append(ctx, "repair.failed", failedPayload); err != nil {
		return err
	}
	return runner.compensate(ctx, root, plan, state.completed)
}

// Inspect returns the recorded state of the plan's latest run without
// changing anything.
func (runner Runner) Inspect(ctx context.Context, plan Plan) (Status, error) {
	runner, _, err := runner.prepare(plan)
	if err != nil {
		return Status{}, err
	}
	state, err := runner.loadState(ctx, plan.IdempotencyKey)
	if err != nil {
		return Status{}, err
	}
	planHash, err := plan.Hash()
	if err != nil {
		return Status{}, err
	}
	status := Status{PlanID: plan.ID, IdempotencyKey: plan.IdempotencyKey, Run: state.run, Steps: []StepStatus{}}
	status.PlanChanged = state.run != RunNotStarted && state.planHash != planHash
	for _, step := range plan.Steps {
		value := state.steps[step.ID]
		if value == "" {
			value = StepPending
		}
		status.Steps = append(status.Steps, StepStatus{ID: step.ID, State: value})
	}
	return status, nil
}

// compensate runs, newest first, the declared compensation of every
// completed step. It stops at the first compensation that fails, because
// later ones may depend on it.
func (runner Runner) compensate(ctx context.Context, root string, plan Plan, completed []string) error {
	steps := map[string]Step{}
	for _, step := range plan.Steps {
		steps[step.ID] = step
	}
	for i := len(completed) - 1; i >= 0; i-- {
		step := steps[completed[i]]
		if step.Compensation == nil {
			continue
		}
		action := Step{ID: step.ID + ".compensation", Argv: step.Compensation.Argv, CWD: step.Compensation.CWD,
			TimeoutSeconds: step.Compensation.TimeoutSeconds, AllowedExitCodes: step.Compensation.AllowedExitCodes}
		startedPayload, _ := json.Marshal(map[string]any{"plan_id": plan.ID, "idempotency_key": plan.IdempotencyKey, "step_id": step.ID, "argv": action.Argv, "cwd": action.CWD})
		if startedPayload, err := redaction.JSON(startedPayload); err != nil {
			return err
		} else if _, err := runner.append(ctx, "repair.compensation_started", startedPayload); err != nil {
			return err
		}
		outcome, executeErr := runner.Executor.Execute(ctx, root, action)
		payload, _ := json.Marshal(map[string]any{"plan_id": plan.ID, "idempotency_key": plan.IdempotencyKey, "step_id": step.ID, "argv": action.Argv, "cwd": action.CWD, "outcome": outcome, "executor_error": errorText(executeErr)})
		payload, err := redaction.JSON(payload)
		if err != nil {
			return err
		}
		if executeErr != nil || outcome.TimedOut || !allows(action.AllowedExitCodes, outcome.ExitCode) {
			if _, err := runner.append(ctx, "repair.compensation_failed", payload); err != nil {
				return err
			}
			return fmt.Errorf("compensation of repair step %q failed (exit %d %s); remaining compensations were not run", step.ID, outcome.ExitCode, errorText(executeErr))
		}
		if _, err := runner.append(ctx, "repair.compensation_completed", payload); err != nil {
			return err
		}
	}
	return nil
}

func fingerprint(root string, patterns []string) (verification.Fingerprint, error) {
	return verification.FingerprintInputs(root, patterns)
}

// recordVerification binds a verification step's result to the inputs it saw
// before running; inputs that changed during the run make it unstable.
func (runner Runner) recordVerification(ctx context.Context, root string, plan Plan, step Step, outcome Outcome, before verification.Fingerprint, passed bool, sourceEventID string) error {
	after, err := fingerprint(root, step.Verification.Inputs)
	stable := err == nil && after.Digest == before.Digest
	_, err = verification.Record(ctx, runner.Store, runner.ProjectID, runner.WorkspaceID, passed, verification.Result{
		Subject:       "repair:" + plan.ID + "/" + step.ID,
		Argv:          step.Argv,
		CWD:           step.CWD,
		ExitCode:      outcome.ExitCode,
		SourceEventID: sourceEventID,
		Inputs:        before,
		Stable:        stable,
	}, runner.Now())
	return err
}

func (runner Runner) append(ctx context.Context, eventType string, payload json.RawMessage) (eventstore.Event, error) {
	return runner.appendAs(ctx, "runtime", eventType, payload)
}

func (runner Runner) appendAs(ctx context.Context, actor, eventType string, payload json.RawMessage) (eventstore.Event, error) {
	event, err := runner.Store.Append(ctx, eventstore.AppendRequest{ProjectID: runner.ProjectID, WorkspaceID: runner.WorkspaceID, OccurredAt: runner.Now(), Type: eventType, Actor: eventstore.Actor{Kind: actor}, Causality: eventstore.Causality{CorrelationID: runner.correlation}, Payload: payload})
	if err != nil {
		return eventstore.Event{}, fmt.Errorf("record %s: %w", eventType, err)
	}
	return event, nil
}

type RunState string

const (
	RunNotStarted RunState = "not_started"
	RunInProgress RunState = "in_progress"
	RunCompleted  RunState = "completed"
	RunFailed     RunState = "failed"
)

type StepState string

const (
	StepPending   StepState = "pending"
	StepUnknown   StepState = "unknown"
	StepCompleted StepState = "completed"
	StepFailed    StepState = "failed"
)

type StepStatus struct {
	ID    string    `json:"id"`
	State StepState `json:"state"`
}

type Status struct {
	PlanID         string       `json:"plan_id"`
	IdempotencyKey string       `json:"idempotency_key"`
	Run            RunState     `json:"run"`
	PlanChanged    bool         `json:"plan_changed"`
	Steps          []StepStatus `json:"steps"`
}

type runState struct {
	run       RunState
	planHash  string
	steps     map[string]StepState
	completed []string
}

func (state runState) unknownStep() string {
	for id, value := range state.steps {
		if value == StepUnknown {
			return id
		}
	}
	return ""
}

// loadState folds the repair events of an idempotency key into the state of
// its latest run.
func (runner Runner) loadState(ctx context.Context, idempotencyKey string) (runState, error) {
	state := runState{run: RunNotStarted, steps: map[string]StepState{}}
	for after := uint64(0); ; {
		events, err := runner.Store.List(ctx, runner.ProjectID, after, 500)
		if err != nil {
			return runState{}, err
		}
		if len(events) == 0 {
			return state, nil
		}
		for _, event := range events {
			after = event.Sequence
			if !strings.HasPrefix(event.Type, "repair.") {
				continue
			}
			var payload struct {
				IdempotencyKey string `json:"idempotency_key"`
				PlanHash       string `json:"plan_hash"`
				StepID         string `json:"step_id"`
				Outcome        any    `json:"outcome"`
			}
			if json.Unmarshal(event.Payload, &payload) != nil || payload.IdempotencyKey != idempotencyKey {
				continue
			}
			switch event.Type {
			case "repair.started":
				state = runState{run: RunInProgress, planHash: payload.PlanHash, steps: map[string]StepState{}}
			case "repair.resumed":
				state.run = RunInProgress
			case "repair.step_started":
				state.run = RunInProgress
				state.steps[payload.StepID] = StepUnknown
			case "repair.step_completed":
				state.steps[payload.StepID] = StepCompleted
				state.completed = append(state.completed, payload.StepID)
			case "repair.step_resolved":
				if payload.Outcome == "succeeded" {
					state.steps[payload.StepID] = StepCompleted
					state.completed = append(state.completed, payload.StepID)
				} else {
					state.steps[payload.StepID] = StepFailed
				}
			case "repair.failed":
				state.run = RunFailed
				if payload.StepID != "" {
					state.steps[payload.StepID] = StepFailed
				}
			case "repair.completed":
				state.run = RunCompleted
			}
		}
	}
}

func acquireLocks(directory string, plan Plan) (func(), error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create repair lock directory: %w", err)
	}
	keys := append([]string{"idempotency:" + plan.IdempotencyKey}, plan.ResourceKeys...)
	sort.Strings(keys)
	paths := []string{}
	for _, key := range keys {
		digest := fmt.Sprintf("%x", sha256Bytes(key))
		path := filepath.Join(directory, digest+".lock")
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) && removeStaleLock(path) {
			file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		}
		if err != nil {
			for _, acquired := range paths {
				os.Remove(acquired)
			}
			if errors.Is(err, os.ErrExist) {
				return nil, fmt.Errorf("resource %q: %w", key, ErrResourceLocked)
			}
			return nil, fmt.Errorf("lock repair resource %q: %w", key, err)
		}
		fmt.Fprintf(file, "%s\n%d\n", plan.ID, os.Getpid())
		file.Close()
		paths = append(paths, path)
	}
	return func() {
		for _, path := range paths {
			os.Remove(path)
		}
	}, nil
}

// removeStaleLock deletes a lock whose recorded holder process no longer
// exists. Locks without a readable holder are treated as held.
func removeStaleLock(path string) bool {
	holder := func() (string, bool) {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", false
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) != 2 {
			return "", false
		}
		pid, err := strconv.Atoi(lines[1])
		if err != nil || pid <= 0 || processAlive(pid) {
			return "", false
		}
		return string(data), true
	}
	content, stale := holder()
	if !stale {
		return false
	}
	// Re-read right before removing so a lock just re-taken by a live
	// process is left alone.
	if again, err := os.ReadFile(path); err != nil || string(again) != content {
		return false
	}
	return os.Remove(path) == nil
}

func processAlive(pid int) bool {
	if goruntime.GOOS == "windows" {
		return true
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM)
}

func sha256Bytes(value string) [32]byte {
	return sha256.Sum256([]byte(value))
}

type CommandExecutor struct {
	MaximumOutputBytes int
}

func (executor CommandExecutor) Execute(ctx context.Context, root string, step Step) (Outcome, error) {
	maximum := executor.MaximumOutputBytes
	if maximum <= 0 {
		maximum = 64 << 10
	}
	stepContext, cancel := context.WithTimeout(ctx, time.Duration(step.TimeoutSeconds)*time.Second)
	defer cancel()
	cwd, err := filepath.EvalSymlinks(filepath.Join(root, step.CWD))
	if err != nil {
		return Outcome{}, fmt.Errorf("resolve repair step cwd: %w", err)
	}
	relative, err := filepath.Rel(root, cwd)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return Outcome{}, fmt.Errorf("repair step cwd resolves outside the project root")
	}
	command := exec.CommandContext(stepContext, step.Argv[0], step.Argv[1:]...)
	command.Dir = cwd
	stdout := &limitedBuffer{maximum: maximum}
	stderr := &limitedBuffer{maximum: maximum}
	command.Stdout = stdout
	command.Stderr = stderr
	err = command.Run()
	outcome := Outcome{ExitCode: 0, Stdout: stdout.String(), Stderr: stderr.String(), TimedOut: errors.Is(stepContext.Err(), context.DeadlineExceeded)}
	if exitErr := new(exec.ExitError); errors.As(err, &exitErr) {
		outcome.ExitCode = exitErr.ExitCode()
		return outcome, nil
	}
	return outcome, err
}

type limitedBuffer struct {
	buffer  bytes.Buffer
	maximum int
	truncated bool
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := buffer.maximum - buffer.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
			buffer.truncated = true
		}
		buffer.buffer.Write(data)
	} else {
		buffer.truncated = true
	}
	return original, nil
}

func (buffer *limitedBuffer) String() string {
	if buffer.truncated {
		return buffer.buffer.String() + "\n[output truncated]"
	}
	return buffer.buffer.String()
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
