package distill

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/projector"
	"github.com/agent-contract-runtime/acr/internal/protocol"
	acrruntime "github.com/agent-contract-runtime/acr/internal/runtime"
)

const projectID = "project-1"

type fixture struct {
	root        string
	store       *eventstore.Store
	projections *projector.Store
	service     *acrruntime.Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	directory := t.TempDir()
	store, err := eventstore.Open(filepath.Join(directory, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	projections, err := projector.Open(filepath.Join(directory, "projections.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { projections.Close() })
	service, err := acrruntime.NewWithEventStore(nil, store)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{root: directory, store: store, projections: projections, service: service}
}

func (f *fixture) fail(t *testing.T, tool string, input map[string]string, message string) string {
	t.Helper()
	raw, _ := json.Marshal(input)
	action := protocol.NewAction(protocol.AgentContext{Kind: protocol.AgentClaudeCode, NativeSessionID: "s1"},
		protocol.WorkspaceContext{CWD: f.root, ProjectID: projectID}, protocol.ToolCall{Name: tool, Input: raw})
	if value := input["file_path"]; value != "" {
		action.Resources = []string{value}
	}
	if err := f.service.RecordToolOutcome(context.Background(), acrruntime.ToolOutcome{Action: action, Failed: true, Error: message}); err != nil {
		t.Fatal(err)
	}
	head, err := f.store.Head(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	events, _ := f.store.List(context.Background(), projectID, head.Sequence-1, 1)
	return events[0].EventID
}

func (f *fixture) propose(t *testing.T) []Candidate {
	t.Helper()
	candidates, err := Propose(context.Background(), f.store, f.projections, projectID, f.root, DefaultMinimumOccurrences, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return candidates
}

func byTool(candidates []Candidate) map[string]Candidate {
	result := map[string]Candidate{}
	for _, candidate := range candidates {
		result[candidate.Contract.Scope.ToolNames[0]] = candidate
	}
	return result
}

func TestProposeFromRepeatedFailuresOnly(t *testing.T) {
	f := newFixture(t)
	login := filepath.Join(f.root, "internal/auth/login.go")
	editIDs := []string{
		f.fail(t, "Edit", map[string]string{"file_path": login}, "login_test.go:12: expected 200, got 500"),
		f.fail(t, "Edit", map[string]string{"file_path": login}, "login_test.go:40: expected 200, got 501"),
	}
	f.fail(t, "Edit", map[string]string{"file_path": filepath.Join(f.root, "other.go")}, "syntax error")
	f.fail(t, "Bash", map[string]string{"command": "make deploy"}, "permission denied")
	f.fail(t, "Bash", map[string]string{"command": "make deploy-all"}, "permission denied")

	candidates := byTool(f.propose(t))
	if len(candidates) != 2 {
		t.Fatalf("proposed %d candidates, want Edit and Bash", len(candidates))
	}
	edit := candidates["Edit"]
	sort.Strings(editIDs)
	if strings.Join(edit.SourceEventIDs, ",") != strings.Join(editIDs, ",") || edit.Occurrences != 2 {
		t.Fatalf("edit candidate evidence = %v (%d)", edit.SourceEventIDs, edit.Occurrences)
	}
	for _, candidate := range candidates {
		value := candidate.Contract
		if value.Metadata.Status != contract.StatusDraft || value.Behavior.Enforcement != contract.EnforcementAdvisory {
			t.Fatalf("candidate %s is %s/%s; candidates must be advisory drafts", candidate.CandidateID, value.Metadata.Status, value.Behavior.Enforcement)
		}
		for _, id := range candidate.SourceEventIDs {
			if !strings.Contains(value.Behavior.Instruction, id) {
				t.Fatalf("instruction does not cite source event %s", id)
			}
		}
		if len(candidate.Assumptions) == 0 || len(candidate.FalsePositiveRisks) == 0 {
			t.Fatalf("candidate %s hides its assumptions or risks", candidate.CandidateID)
		}
		results, err := Simulate(value, candidate.Cases)
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range results {
			if !result.Passed {
				t.Fatalf("candidate %s fails case %+v", candidate.CandidateID, result)
			}
		}
	}
	if len(edit.Cases) != 3 || !strings.Contains(string(edit.Contract.Applicability.All[1].Value), "internal/auth/login.go") {
		t.Fatalf("edit candidate is not narrowed to the failing path: %+v", edit.Contract.Applicability)
	}
	bash := candidates["Bash"]
	if len(bash.Ambiguities) == 0 || !strings.Contains(bash.Ambiguities[0], "matches every Bash call") || !strings.Contains(strings.Join(bash.FalsePositiveRisks, " "), "High") {
		t.Fatalf("broad Bash candidate does not flag its risk: %+v", bash)
	}

	if again := f.propose(t); len(again) != 0 {
		t.Fatalf("same evidence proposed again: %d", len(again))
	}
}

func TestRejectedCandidateReturnsOnlyWithNewEvidence(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.root, "a.go")
	f.fail(t, "Write", map[string]string{"file_path": path}, "disk quota exceeded")
	f.fail(t, "Write", map[string]string{"file_path": path}, "disk quota exceeded")
	first := f.propose(t)[0]

	if _, err := Decide(context.Background(), f.store, projectID, first.CandidateID, false, "", time.Now()); err == nil {
		t.Fatal("rejection without a reason was accepted")
	}
	if _, err := Decide(context.Background(), f.store, projectID, first.CandidateID, false, "quota is a CI-only issue", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := Decide(context.Background(), f.store, projectID, first.CandidateID, true, "", time.Now()); err == nil {
		t.Fatal("a rejected candidate was accepted")
	}
	if again := f.propose(t); len(again) != 0 {
		t.Fatal("rejected candidate was proposed again on the same evidence")
	}

	f.fail(t, "Write", map[string]string{"file_path": path}, "disk quota exceeded")
	renewed := f.propose(t)
	if len(renewed) != 1 || renewed[0].CandidateID == first.CandidateID || renewed[0].Occurrences != 3 {
		t.Fatalf("new evidence did not produce a new candidate: %+v", renewed)
	}

	if _, err := Decide(context.Background(), f.store, projectID, renewed[0].CandidateID, true, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	f.fail(t, "Write", map[string]string{"file_path": path}, "disk quota exceeded")
	if after := f.propose(t); len(after) != 0 {
		t.Fatal("an accepted pattern was proposed again")
	}

	records, err := List(context.Background(), f.store, projectID)
	if err != nil || len(records) != 2 {
		t.Fatalf("List() = %d records, %v", len(records), err)
	}
	if _, err := Decide(context.Background(), f.store, projectID, "cand_missing", true, "", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Decide() of unknown candidate error = %v", err)
	}
}

func TestRepairFailuresAreNotContractCandidates(t *testing.T) {
	f := newFixture(t)
	for range 3 {
		payload, _ := json.Marshal(map[string]any{"plan_id": "deploy", "idempotency_key": "deploy-v1", "step_id": "push", "outcome": map[string]any{"exit_code": 1, "stderr": "denied"}})
		if _, err := f.store.Append(context.Background(), eventstore.AppendRequest{ProjectID: projectID, OccurredAt: time.Now(), Type: "repair.failed", Actor: eventstore.Actor{Kind: "runtime"}, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	if candidates := f.propose(t); len(candidates) != 0 {
		t.Fatalf("repair failures produced candidates: %+v", candidates)
	}
	if _, err := Propose(context.Background(), f.store, f.projections, projectID, f.root, 1, time.Now()); err == nil {
		t.Fatal("Propose() accepted a single occurrence threshold")
	}
}
