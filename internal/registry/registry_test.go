package registry

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
)

func testContract(id string) contract.Contract {
	return contract.Contract{
		APIVersion:    contract.APIVersion,
		Kind:          "Contract",
		Metadata:      contract.Metadata{ID: id, Version: 1, Title: id, Status: contract.StatusActive, Severity: "high", Priority: 900},
		Scope:         contract.Scope{OperationKinds: []string{"process.exec"}},
		Applicability: contract.Applicability{All: []contract.Condition{{Field: "input.command", Operator: contract.OperatorContains, Value: json.RawMessage(`"` + id + `"`)}}},
		Behavior:      contract.Behavior{Instruction: "do not run " + id, Enforcement: contract.EnforcementHard},
		Correction:    contract.Correction{Mode: contract.CorrectionDeny},
		FailureMode:   contract.FailureClosed,
	}
}

func writeContract(t *testing.T, directory string, value contract.Contract) {
	t.Helper()
	encoded, _ := json.Marshal(value)
	if err := os.WriteFile(filepath.Join(directory, value.Metadata.ID+".json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

// load returns the enforced contracts other than the built-in ones.
func load(t *testing.T, store *eventstore.Store, directory string) []contract.Contract {
	t.Helper()
	values, err := Load(context.Background(), store, "p", directory)
	if err != nil {
		t.Fatal(err)
	}
	builtin := len(Builtin())
	if len(values) < builtin || values[builtin-1].Metadata.ID != BuiltinProtectState {
		t.Fatalf("built-in contracts missing: %v", ids(values))
	}
	return values[builtin:]
}

func ids(values []contract.Contract) []string {
	result := []string{}
	for _, value := range values {
		result = append(result, value.Metadata.ID)
	}
	return result
}

func TestRegistryDecidesWhatIsEnforcedOnceManaged(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store, err := eventstore.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	writeContract(t, directory, testContract("legacy"))
	draft := testContract("draft")
	draft.Metadata.Status = contract.StatusDraft
	writeContract(t, directory, draft)

	// Unmanaged: the directory is what is enforced.
	values := load(t, store, directory)
	if len(values) != 2 {
		t.Fatalf("unmanaged load = %v", ids(values))
	}

	// The first activation imports what was in force, so it keeps applying.
	entry, err := Activate(ctx, store, "p", "w", directory, testContract("added"), Source{Kind: SourceUserInstruction, QuoteEventID: "evt-1", Quote: "记住不要跑 added"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if entry.Contract.Metadata.Version != 1 || entry.Source.Kind != SourceUserInstruction {
		t.Fatalf("entry = %+v", entry)
	}
	values = load(t, store, directory)
	if len(values) != 2 || values[0].Metadata.ID != "added" || values[1].Metadata.ID != "legacy" {
		t.Fatalf("managed load = %v", ids(values))
	}

	// Files written behind ACR's back change nothing.
	writeContract(t, directory, testContract("sneaked"))
	weakened := testContract("legacy")
	weakened.Correction.Mode = contract.CorrectionNone
	weakened.Behavior.Enforcement = contract.EnforcementAdvisory
	writeContract(t, directory, weakened)
	os.Remove(filepath.Join(directory, "added.json"))
	values = load(t, store, directory)
	if len(values) != 2 || values[1].Correction.Mode != contract.CorrectionDeny {
		t.Fatalf("load after tampering = %v (%v)", ids(values), values)
	}

	// Activating the same ID again replaces it under the next version.
	again, err := Activate(ctx, store, "p", "w", directory, testContract("added"), Source{Kind: SourceCLI}, time.Now())
	if err != nil || again.Contract.Metadata.Version != 2 {
		t.Fatalf("reactivation = %+v, %v", again.Contract.Metadata, err)
	}
	if _, err := os.Stat(filepath.Join(directory, "added.json")); err != nil {
		t.Fatalf("mirror not rewritten: %v", err)
	}

	if err := Retire(ctx, store, "p", "w", directory, "legacy", "no longer needed", Source{Kind: SourceCLI}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "legacy.json")); !os.IsNotExist(err) {
		t.Fatalf("retired copy still present: %v", err)
	}
	values = load(t, store, directory)
	if len(values) != 1 || values[0].Metadata.ID != "added" {
		t.Fatalf("load after retire = %v", ids(values))
	}
	if err := Retire(ctx, store, "p", "w", directory, "legacy", "again", Source{Kind: SourceCLI}, time.Now()); err == nil {
		t.Fatal("retiring an inactive contract succeeded")
	}
	if err := store.Verify(ctx, "p"); err != nil {
		t.Fatal(err)
	}
}

func TestActivateRejectsInvalidContract(t *testing.T) {
	store, err := eventstore.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	directory := t.TempDir()
	writeContract(t, directory, testContract("legacy"))
	value := testContract("broken")
	value.Correction.Mode = "maybe"
	if _, err := Activate(context.Background(), store, "p", "w", directory, value, Source{Kind: SourceCLI}, time.Now()); err == nil {
		t.Fatal("invalid contract was activated")
	}
	if _, managed, err := Active(context.Background(), store, "p"); err != nil || managed {
		t.Fatalf("a failed activation changed the registry: managed = %v, %v", managed, err)
	}
}
