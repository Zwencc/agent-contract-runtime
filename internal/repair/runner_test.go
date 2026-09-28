package repair

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
)

type fakeExecutor struct {
	outcomes []Outcome
	errors   []error
	steps    []string
}

func (executor *fakeExecutor) Execute(_ context.Context, _ string, step Step) (Outcome, error) {
	executor.steps = append(executor.steps, step.ID)
	index := len(executor.steps) - 1
	var outcome Outcome
	var err error
	if index < len(executor.outcomes) {
		outcome = executor.outcomes[index]
	}
	if index < len(executor.errors) {
		err = executor.errors[index]
	}
	return outcome, err
}

func TestRunnerCompletesStepsAndDoesNotReplayIdempotencyKey(t *testing.T) {
	runner, store, executor := newTestRunner(t)
	defer store.Close()
	plan := testPlan()
	result, err := runner.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.CompletedSteps != 2 || len(executor.steps) != 2 {
		t.Fatalf("result = %#v, steps = %#v", result, executor.steps)
	}
	events, err := store.List(context.Background(), "project-1", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 6 || events[0].Type != "repair.started" || events[5].Type != "repair.completed" {
		t.Fatalf("events = %#v", events)
	}
	if _, err := runner.Run(context.Background(), plan); !errors.Is(err, ErrAlreadyCompleted) {
		t.Fatalf("second Run() error = %v", err)
	}
	if len(executor.steps) != 2 {
		t.Fatalf("completed repair replayed steps: %#v", executor.steps)
	}
}

func TestRunnerStopsAfterFailedStep(t *testing.T) {
	runner, store, executor := newTestRunner(t)
	defer store.Close()
	executor.outcomes = []Outcome{{ExitCode: 7}}
	_, err := runner.Run(context.Background(), testPlan())
	if err == nil {
		t.Fatal("Run() accepted failed step")
	}
	if len(executor.steps) != 1 {
		t.Fatalf("steps = %#v", executor.steps)
	}
	events, err := store.List(context.Background(), "project-1", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-1].Type != "repair.failed" {
		t.Fatalf("terminal event = %#v", events[len(events)-1])
	}
}

func TestRunnerRequiresRecoveryForInterruptedPlan(t *testing.T) {
	runner, store, executor := newTestRunner(t)
	defer store.Close()
	payload, _ := json.Marshal(map[string]string{"idempotency_key": "deploy-after-test"})
	if _, err := store.Append(context.Background(), eventstore.AppendRequest{ProjectID: "project-1", OccurredAt: time.Now(), Type: "repair.step_started", Actor: eventstore.Actor{Kind: "runtime"}, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), testPlan()); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("Run() error = %v", err)
	}
	if len(executor.steps) != 0 {
		t.Fatalf("interrupted repair replayed steps: %#v", executor.steps)
	}
}

func TestRunnerHonorsResourceLock(t *testing.T) {
	runner, store, _ := newTestRunner(t)
	defer store.Close()
	plan := testPlan()
	if err := os.MkdirAll(runner.LockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256Bytes("idempotency:" + plan.IdempotencyKey)
	path := filepath.Join(runner.LockDir, formatDigest(digest)+".lock")
	if err := os.WriteFile(path, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), plan); !errors.Is(err, ErrResourceLocked) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestPlanRejectsEscapingCWDAndShellIsNotImplicit(t *testing.T) {
	plan := testPlan()
	plan.Steps[0].CWD = "../outside"
	if err := plan.Validate(); err == nil {
		t.Fatal("Validate() accepted escaping cwd")
	}
	plan = testPlan()
	if len(plan.Steps[0].Argv) != 2 || plan.Steps[0].Argv[0] != "test-command" {
		t.Fatalf("argv was not preserved: %#v", plan.Steps[0].Argv)
	}
}

func TestCommandExecutorRejectsSymlinkCWDOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	_, err := (CommandExecutor{}).Execute(context.Background(), root, Step{ID: "test", Argv: []string{"/bin/true"}, CWD: "outside", TimeoutSeconds: 10, AllowedExitCodes: []int{0}})
	if err == nil {
		t.Fatal("CommandExecutor.Execute() accepted cwd symlink outside root")
	}
}

func newTestRunner(t *testing.T) (Runner, *eventstore.Store, *fakeExecutor) {
	t.Helper()
	directory := t.TempDir()
	store, err := eventstore.Open(filepath.Join(directory, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutor{outcomes: []Outcome{{ExitCode: 0}, {ExitCode: 0}}}
	runner := Runner{Store: store, ProjectID: "project-1", WorkspaceID: "workspace-1", Root: directory, LockDir: filepath.Join(directory, "locks"), Executor: executor, Now: time.Now}
	return runner, store, executor
}

func testPlan() Plan {
	return Plan{
		SchemaVersion: SchemaVersion,
		ID: "deploy-plan",
		IdempotencyKey: "deploy-after-test",
		ResourceKeys: []string{"deployment:staging"},
		Steps: []Step{
			{ID: "test", Argv: []string{"test-command", "./..."}, TimeoutSeconds: 30},
			{ID: "deploy", Argv: []string{"deploy-command"}, TimeoutSeconds: 30},
		},
	}
}

func formatDigest(value [32]byte) string {
	const digits = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for index, current := range value {
		encoded[index*2] = digits[current>>4]
		encoded[index*2+1] = digits[current&15]
	}
	return string(encoded)
}
