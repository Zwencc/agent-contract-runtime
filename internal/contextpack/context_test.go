package contextpack

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/memory"
)

func TestBuildIncludesActiveContractsAndRecentEvidenceWithinBudget(t *testing.T) {
	store, err := eventstore.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("eventstore.Open() error = %v", err)
	}
	defer store.Close()
	_, err = store.Append(context.Background(), eventstore.AppendRequest{
		ProjectID:  "project-1",
		OccurredAt: time.Now(),
		Type:       "action.failed",
		Actor:      eventstore.Actor{Kind: "agent"},
		Payload:    json.RawMessage(`{"error":"test failed"}`),
	})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	contracts := []contract.Contract{{
		Metadata: contract.Metadata{ID: "run-tests", Version: 1, Status: contract.StatusActive},
		Behavior: contract.Behavior{Instruction: "Run tests before deployment", Enforcement: contract.EnforcementHard},
	}}
	output, err := Build(context.Background(), store, "project-1", contracts, Options{MaximumCharacters: 2000})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !strings.Contains(output, "run-tests@1") || !strings.Contains(output, "action.failed") || !strings.Contains(output, "test failed") {
		t.Fatalf("context = %s", output)
	}
	if len(output) > 2000 {
		t.Fatalf("context length = %d", len(output))
	}
}

func TestBuildWithMemoryIncludesOnlyRetrievableMemoryInPriorityOrder(t *testing.T) {
	directory := t.TempDir()
	events, err := eventstore.Open(filepath.Join(directory, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	memories, err := memory.Open(filepath.Join(directory, "memories.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer memories.Close()
	low, err := memories.CreateCandidate(context.Background(), memory.CreateRequest{ProjectID: "project-1", Kind: "project_fact", Priority: memory.P4, Claim: "low priority", Scope: json.RawMessage(`{}`), EvidenceRefs: []string{"event:test-low"}, Source: "test", Confidence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memories.Activate(context.Background(), "project-1", low.MemoryID, "approved"); err != nil {
		t.Fatal(err)
	}
	high, err := memories.CreateCandidate(context.Background(), memory.CreateRequest{ProjectID: "project-1", Kind: "user_correction", Priority: memory.P0, Claim: "high priority", Scope: json.RawMessage(`{}`), EvidenceRefs: []string{"event:test-high"}, Source: "test", Confidence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memories.Activate(context.Background(), "project-1", high.MemoryID, "approved"); err != nil {
		t.Fatal(err)
	}
	if _, err := memories.CreateCandidate(context.Background(), memory.CreateRequest{ProjectID: "project-1", Kind: "work_state", Priority: memory.P1, Claim: "candidate hidden", Scope: json.RawMessage(`{}`), Source: "test", Confidence: 1}); err != nil {
		t.Fatal(err)
	}

	result, err := BuildWithMemory(context.Background(), events, memories, "project-1", "", nil, Options{MaximumCharacters: 6000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "high priority") || !strings.Contains(result, "low priority") || strings.Contains(result, "candidate hidden") {
		t.Fatalf("context = %s", result)
	}
	if strings.Index(result, "high priority") > strings.Index(result, "low priority") {
		t.Fatalf("memory priority order is wrong: %s", result)
	}
}

func TestBuildReportsOmissionsAndKeepsNewestEvidence(t *testing.T) {
	store, err := eventstore.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i := range 30 {
		if _, err := store.Append(context.Background(), eventstore.AppendRequest{
			ProjectID: "project-1", OccurredAt: time.Now(), Type: "action.completed", Actor: eventstore.Actor{Kind: "agent"},
			Payload: json.RawMessage(`{"note":"` + strings.Repeat("x", 80) + `","n":` + strconv.Itoa(i) + `}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	var contracts []contract.Contract
	for i := range 3 {
		contracts = append(contracts, contract.Contract{
			Metadata: contract.Metadata{ID: "rule-" + strconv.Itoa(i), Version: 1, Status: contract.StatusActive},
			Behavior: contract.Behavior{Instruction: "Hard rule " + strconv.Itoa(i), Enforcement: contract.EnforcementHard},
		})
	}

	output, err := Build(context.Background(), store, "project-1", contracts, Options{MaximumEvents: 30, MaximumCharacters: 1500})
	if err != nil {
		t.Fatal(err)
	}
	if len(output) > 1500 {
		t.Fatalf("context length = %d", len(output))
	}
	for i := range 3 {
		if !strings.Contains(output, "rule-"+strconv.Itoa(i)+"@1") {
			t.Fatalf("contract rule-%d missing from a tight budget:\n%s", i, output)
		}
	}
	if !strings.Contains(output, "E30 ") || strings.Contains(output, "E1 ") {
		t.Fatalf("budget should keep the newest evidence:\n%s", output)
	}
	if !strings.Contains(output, "## Omitted by context budget") || !strings.Contains(output, "older events") {
		t.Fatalf("omissions not reported:\n%s", output)
	}

	// With too little room even for contracts, their count is still reported.
	tiny, err := Build(context.Background(), store, "project-1", contracts, Options{MaximumEvents: 30, MaximumCharacters: 420})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tiny, "active contracts") || !strings.Contains(tiny, "Query ACR") {
		t.Fatalf("tiny context hides omitted contracts:\n%s", tiny)
	}
}
