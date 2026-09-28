package repair

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
)

func threeStepPlan() Plan {
	return Plan{
		SchemaVersion:  SchemaVersion,
		ID:             "release",
		IdempotencyKey: "release-v1",
		ResourceKeys:   []string{"deployment:staging"},
		Steps: []Step{
			{ID: "migrate", Argv: []string{"migrate", "up"}, TimeoutSeconds: 10, Compensation: &Compensation{Argv: []string{"migrate", "down"}, TimeoutSeconds: 10}},
			{ID: "deploy", Argv: []string{"deploy"}, TimeoutSeconds: 10, Compensation: &Compensation{Argv: []string{"rollback"}, TimeoutSeconds: 10}},
			{ID: "smoke", Argv: []string{"smoke-test"}, TimeoutSeconds: 10},
		},
	}
}

// simulateCrash writes the events a runner leaves behind when its process
// dies: the given steps completed, and the step after them started.
func simulateCrash(t *testing.T, store *eventstore.Store, plan Plan, completed int, startedNext bool) {
	t.Helper()
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	hash, err := plan.Hash()
	if err != nil {
		t.Fatal(err)
	}
	add := func(eventType string, payload map[string]any) {
		payload["plan_id"] = plan.ID
		payload["idempotency_key"] = plan.IdempotencyKey
		encoded, _ := json.Marshal(payload)
		if _, err := store.Append(context.Background(), eventstore.AppendRequest{ProjectID: "project-1", OccurredAt: time.Now(),
			Type: eventType, Actor: eventstore.Actor{Kind: "runtime"}, Payload: encoded}); err != nil {
			t.Fatal(err)
		}
	}
	add("repair.started", map[string]any{"plan_hash": hash})
	for index := range completed {
		add("repair.step_started", map[string]any{"step_id": plan.Steps[index].ID})
		add("repair.step_completed", map[string]any{"step_id": plan.Steps[index].ID, "outcome": map[string]any{"exit_code": 0}})
	}
	if startedNext {
		add("repair.step_started", map[string]any{"step_id": plan.Steps[completed].ID})
	}
}

func eventTypes(t *testing.T, store *eventstore.Store) []string {
	t.Helper()
	events, err := store.List(context.Background(), "project-1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	types := []string{}
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func stepStates(t *testing.T, runner Runner, plan Plan) string {
	t.Helper()
	status, err := runner.Inspect(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	states := []string{string(status.Run)}
	for _, step := range status.Steps {
		states = append(states, step.ID+"="+string(step.State))
	}
	return strings.Join(states, " ")
}

func TestUnknownStepIsNeverGuessed(t *testing.T) {
	runner, store, executor := newTestRunner(t)
	defer store.Close()
	plan := threeStepPlan()
	simulateCrash(t, store, plan, 1, true)

	if got := stepStates(t, runner, plan); got != "in_progress migrate=completed deploy=unknown smoke=pending" {
		t.Fatalf("status = %s", got)
	}
	_, err := runner.Run(context.Background(), plan)
	if !errors.Is(err, ErrRecoveryRequired) || !strings.Contains(err.Error(), `"deploy"`) {
		t.Fatalf("Run() error = %v", err)
	}
	if len(executor.steps) != 0 {
		t.Fatalf("interrupted run executed %v", executor.steps)
	}
	if err := runner.Resolve(context.Background(), plan, "smoke", true, "not started"); err == nil {
		t.Fatal("Resolve() accepted a step without an unknown outcome")
	}
	if err := runner.Resolve(context.Background(), plan, "deploy", true, ""); err == nil {
		t.Fatal("Resolve() accepted an empty reason")
	}

	// A human confirms the deploy finished; the run resumes after it.
	if err := runner.Resolve(context.Background(), plan, "deploy", true, "deployment dashboard shows the new build"); err != nil {
		t.Fatal(err)
	}
	executor.outcomes = []Outcome{{ExitCode: 0}}
	result, err := runner.Run(context.Background(), plan)
	if err != nil || result.CompletedSteps != 3 {
		t.Fatalf("resumed Run() = %+v, %v", result, err)
	}
	if strings.Join(executor.steps, ",") != "smoke" {
		t.Fatalf("resumed run executed %v, want only smoke", executor.steps)
	}
	types := eventTypes(t, store)
	if types[len(types)-1] != "repair.completed" || !contains(types, "repair.step_resolved") || !contains(types, "repair.resumed") {
		t.Fatalf("events = %v", types)
	}
	if _, err := runner.Run(context.Background(), plan); !errors.Is(err, ErrAlreadyCompleted) {
		t.Fatalf("Run() after completion error = %v", err)
	}
}

func TestInterruptedRunResumesAfterLastCompletedStep(t *testing.T) {
	runner, store, executor := newTestRunner(t)
	defer store.Close()
	plan := threeStepPlan()
	simulateCrash(t, store, plan, 1, false)
	executor.outcomes = []Outcome{{ExitCode: 0}, {ExitCode: 0}}
	if _, err := runner.Run(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if strings.Join(executor.steps, ",") != "deploy,smoke" {
		t.Fatalf("resumed run executed %v", executor.steps)
	}
}

func TestInterruptedRunRefusesChangedPlan(t *testing.T) {
	runner, store, executor := newTestRunner(t)
	defer store.Close()
	plan := threeStepPlan()
	simulateCrash(t, store, plan, 1, false)
	plan.Steps[2].Argv = []string{"other-smoke-test"}
	if _, err := runner.Run(context.Background(), plan); !errors.Is(err, ErrRecoveryRequired) || !strings.Contains(err.Error(), "plan differs") {
		t.Fatalf("Run() error = %v", err)
	}
	if len(executor.steps) != 0 {
		t.Fatalf("changed plan executed %v", executor.steps)
	}
}

func TestResolvedFailureCompensatesCompletedSteps(t *testing.T) {
	runner, store, executor := newTestRunner(t)
	defer store.Close()
	plan := threeStepPlan()
	simulateCrash(t, store, plan, 1, true)
	if err := runner.Resolve(context.Background(), plan, "deploy", false, "deployment never reached the cluster"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(executor.steps, ",") != "migrate.compensation" {
		t.Fatalf("compensations run = %v", executor.steps)
	}
	if got := stepStates(t, runner, plan); got != "failed migrate=completed deploy=failed smoke=pending" {
		t.Fatalf("status = %s", got)
	}
	if _, err := runner.Run(context.Background(), plan); !errors.Is(err, ErrAlreadyFailed) {
		t.Fatalf("Run() after failure error = %v", err)
	}
}

func TestFailedStepCompensatesInReverseOrder(t *testing.T) {
	runner, store, executor := newTestRunner(t)
	defer store.Close()
	executor.outcomes = []Outcome{{ExitCode: 0}, {ExitCode: 0}, {ExitCode: 9}, {ExitCode: 0}, {ExitCode: 0}}
	_, err := runner.Run(context.Background(), threeStepPlan())
	if err == nil || !strings.Contains(err.Error(), `"smoke" exited with code 9`) {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Join(executor.steps, ",") != "migrate,deploy,smoke,deploy.compensation,migrate.compensation" {
		t.Fatalf("executed %v", executor.steps)
	}
	types := eventTypes(t, store)
	if strings.Count(strings.Join(types, ","), "repair.compensation_completed") != 2 {
		t.Fatalf("events = %v", types)
	}
}

func TestFailedCompensationStopsTheChain(t *testing.T) {
	runner, store, executor := newTestRunner(t)
	defer store.Close()
	executor.outcomes = []Outcome{{ExitCode: 0}, {ExitCode: 0}, {ExitCode: 9}, {ExitCode: 1}}
	_, err := runner.Run(context.Background(), threeStepPlan())
	if err == nil || !strings.Contains(err.Error(), `compensation of repair step "deploy" failed`) {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Join(executor.steps, ",") != "migrate,deploy,smoke,deploy.compensation" {
		t.Fatalf("executed %v; migrate must not be compensated after rollback failed", executor.steps)
	}
	if types := eventTypes(t, store); types[len(types)-1] != "repair.compensation_failed" {
		t.Fatalf("events = %v", types)
	}
}

func TestStaleLockOfDeadProcessIsRecovered(t *testing.T) {
	runner, store, _ := newTestRunner(t)
	defer store.Close()
	plan := testPlan()
	if err := os.MkdirAll(runner.LockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runner.LockDir, formatDigest(sha256Bytes("idempotency:"+plan.IdempotencyKey))+".lock")

	if err := os.WriteFile(path, []byte(fmt.Sprintf("%s\n%d\n", plan.ID, os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), plan); !errors.Is(err, ErrResourceLocked) {
		t.Fatalf("Run() with a live holder error = %v", err)
	}

	finished := exec.Command("true")
	if err := finished.Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%s\n%d\n", plan.ID, finished.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), plan); err != nil {
		t.Fatalf("Run() with a dead holder error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("lock left behind after run: %v", err)
	}
}

func TestCompensationIsValidated(t *testing.T) {
	plan := threeStepPlan()
	plan.Steps[0].Compensation.CWD = "../outside"
	if err := plan.Validate(); err == nil {
		t.Fatal("Validate() accepted an escaping compensation cwd")
	}
	plan = threeStepPlan()
	plan.Steps[1].Compensation.TimeoutSeconds = 0
	if err := plan.Validate(); err == nil {
		t.Fatal("Validate() accepted a compensation without timeout")
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
