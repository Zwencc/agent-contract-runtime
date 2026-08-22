package eventstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/agent-contract-runtime/acr/internal/protocol"
)

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func testRequest(projectID string, payload string) AppendRequest {
	return AppendRequest{
		ProjectID:   projectID,
		WorkspaceID: "ws-1",
		OccurredAt:  time.Date(2026, 10, 19, 8, 30, 0, 123456789, time.FixedZone("CST", 8*3600)),
		Type:        "action.proposed",
		Actor: Actor{
			Kind:            "agent",
			AgentKind:       protocol.AgentClaudeCode,
			NativeSessionID: "native-1",
		},
		Causality:    Causality{CorrelationID: "corr-1"},
		Payload:      json.RawMessage(payload),
		EvidenceRefs: []string{"sha256:abc"},
	}
}

func mustAppend(t *testing.T, store *Store, request AppendRequest) Event {
	t.Helper()
	event, err := store.Append(context.Background(), request)
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	return event
}

func mustVerify(t *testing.T, store *Store, projectID string) {
	t.Helper()
	if err := store.Verify(context.Background(), projectID); err != nil {
		t.Fatalf("Verify(%q) error = %v", projectID, err)
	}
}

func TestAppendFirstEvent(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "events.db"))
	request := testRequest("proj-a", `{"tool":"write_file"}`)

	event := mustAppend(t, store, request)

	if event.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion = %q", event.SchemaVersion)
	}
	if event.EventID == "" {
		t.Fatal("EventID was not generated")
	}
	if event.Sequence != 1 {
		t.Fatalf("Sequence = %d, want 1", event.Sequence)
	}
	if event.PreviousHash != "" {
		t.Fatalf("PreviousHash = %q, want empty", event.PreviousHash)
	}
	if len(event.PayloadHash) != 64 || len(event.EventHash) != 64 {
		t.Fatalf("hashes = %q / %q", event.PayloadHash, event.EventHash)
	}
	if !event.OccurredAt.Equal(request.OccurredAt) || event.RecordedAt.IsZero() {
		t.Fatalf("times = %v / %v", event.OccurredAt, event.RecordedAt)
	}
	if event.Actor != request.Actor || event.Causality != request.Causality {
		t.Fatalf("actor/causality = %+v / %+v", event.Actor, event.Causality)
	}

	head, err := store.Head(context.Background(), "proj-a")
	if err != nil {
		t.Fatalf("Head() error = %v", err)
	}
	if head.Sequence != 1 || head.EventHash != event.EventHash {
		t.Fatalf("Head() = %+v", head)
	}

	listed, err := store.List(context.Background(), "proj-a", 0, 10)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 1 || listed[0].EventHash != event.EventHash || string(listed[0].Payload) != `{"tool":"write_file"}` {
		t.Fatalf("List() = %+v", listed)
	}
	if len(listed[0].EvidenceRefs) != 1 || listed[0].EvidenceRefs[0] != "sha256:abc" {
		t.Fatalf("EvidenceRefs = %v", listed[0].EvidenceRefs)
	}
	mustVerify(t, store, "proj-a")
}

func TestAppendBuildsChain(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "events.db"))

	var events []Event
	for i := range 5 {
		events = append(events, mustAppend(t, store, testRequest("proj-a", fmt.Sprintf(`{"n":%d}`, i))))
	}
	for i, event := range events {
		if event.Sequence != uint64(i+1) {
			t.Fatalf("event %d sequence = %d", i, event.Sequence)
		}
		if i > 0 && event.PreviousHash != events[i-1].EventHash {
			t.Fatalf("event %d previous_hash does not link to event %d", i, i-1)
		}
	}

	page, err := store.List(context.Background(), "proj-a", 2, 2)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page) != 2 || page[0].Sequence != 3 || page[1].Sequence != 4 {
		t.Fatalf("List(after=2, limit=2) = %+v", page)
	}
	if _, err := store.List(context.Background(), "proj-a", 0, 0); err == nil {
		t.Fatal("List(limit=0) error = nil")
	}
	mustVerify(t, store, "proj-a")
}

func TestProjectsHaveIndependentChains(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "events.db"))

	var a, b []Event
	for i := range 3 {
		a = append(a, mustAppend(t, store, testRequest("proj-a", fmt.Sprintf(`{"n":%d}`, i))))
		b = append(b, mustAppend(t, store, testRequest("proj-b", fmt.Sprintf(`{"n":%d}`, i))))
	}
	for i := range 3 {
		if a[i].Sequence != uint64(i+1) || b[i].Sequence != uint64(i+1) {
			t.Fatalf("sequences = %d / %d, want %d", a[i].Sequence, b[i].Sequence, i+1)
		}
	}
	if a[0].PreviousHash != "" || b[0].PreviousHash != "" {
		t.Fatal("first event of each project must have empty previous_hash")
	}
	if b[1].PreviousHash != b[0].EventHash || a[1].PreviousHash != a[0].EventHash {
		t.Fatal("chains are interleaved across projects")
	}
	mustVerify(t, store, "proj-a")
	mustVerify(t, store, "proj-b")

	tamper(t, store, `UPDATE events SET payload = '{"n":99}' WHERE project_id = 'proj-a' AND sequence = 2`)
	if err := store.Verify(context.Background(), "proj-a"); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Verify(proj-a) error = %v, want ErrIntegrity", err)
	}
	mustVerify(t, store, "proj-b")
}

func TestReopenContinuesChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	mustAppend(t, first, testRequest("proj-a", `{"n":1}`))
	second := mustAppend(t, first, testRequest("proj-a", `{"n":2}`))
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened := openTestStore(t, path)
	third := mustAppend(t, reopened, testRequest("proj-a", `{"n":3}`))
	if third.Sequence != 3 || third.PreviousHash != second.EventHash {
		t.Fatalf("after reopen: sequence %d previous_hash linked = %v", third.Sequence, third.PreviousHash == second.EventHash)
	}
	mustVerify(t, reopened, "proj-a")
}

func TestPayloadUsesStableJSON(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "events.db"))

	one := mustAppend(t, store, testRequest("proj-a", `{"b":{"y":1,"x":[3,2]},"a":"<&>","n":12345678901234567890}`))
	two := mustAppend(t, store, testRequest("proj-b", " {\n \"n\": 12345678901234567890, \"a\": \"<&>\", \"b\": {\"x\": [3, 2], \"y\": 1}\n}"))

	want := `{"a":"<&>","b":{"x":[3,2],"y":1},"n":12345678901234567890}`
	if string(one.Payload) != want || string(two.Payload) != want {
		t.Fatalf("payloads = %s / %s, want %s", one.Payload, two.Payload, want)
	}
	if one.PayloadHash != two.PayloadHash {
		t.Fatal("equal JSON values produced different payload hashes")
	}
}

func TestInvalidJSONRejected(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "events.db"))

	for _, payload := range []string{``, `{"a":`, `{"a":1} {"b":2}`, `{a:1}`} {
		if _, err := store.Append(context.Background(), testRequest("proj-a", payload)); err == nil {
			t.Fatalf("Append(payload %q) error = nil", payload)
		}
	}
	if _, err := store.Head(context.Background(), "proj-a"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("Head() after rejected appends error = %v, want ErrProjectNotFound", err)
	}
}

func TestRequiredFieldsRejected(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "events.db"))

	missingProject := testRequest("", `{}`)
	missingType := testRequest("proj-a", `{}`)
	missingType.Type = ""
	missingTime := testRequest("proj-a", `{}`)
	missingTime.OccurredAt = time.Time{}

	for name, request := range map[string]AppendRequest{
		"project_id":  missingProject,
		"type":        missingType,
		"occurred_at": missingTime,
	} {
		if _, err := store.Append(context.Background(), request); err == nil {
			t.Fatalf("Append() without %s error = nil", name)
		}
	}
}

func TestDuplicateEventIDRejected(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "events.db"))

	request := testRequest("proj-a", `{}`)
	request.EventID = "evt-1"
	mustAppend(t, store, request)
	if _, err := store.Append(context.Background(), request); err == nil {
		t.Fatal("Append() with duplicate event_id error = nil")
	}

	head, err := store.Head(context.Background(), "proj-a")
	if err != nil || head.Sequence != 1 {
		t.Fatalf("Head() = %+v, %v; want sequence 1", head, err)
	}
	mustVerify(t, store, "proj-a")
}

func TestUnknownProject(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "events.db"))
	mustAppend(t, store, testRequest("proj-a", `{}`))

	if _, err := store.Head(context.Background(), "missing"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("Head() error = %v, want ErrProjectNotFound", err)
	}
	events, err := store.List(context.Background(), "missing", 0, 10)
	if err != nil || events == nil || len(events) != 0 {
		t.Fatalf("List() = %v, %v; want empty slice", events, err)
	}
	if err := store.Verify(context.Background(), "missing"); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("Verify() error = %v, want ErrProjectNotFound", err)
	}
}

func TestEventsAreAppendOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	store := openTestStore(t, path)
	mustAppend(t, store, testRequest("proj-a", `{}`))

	db := store.db
	if _, err := db.Exec(`UPDATE events SET payload = '{"x":1}'`); err == nil {
		t.Fatal("UPDATE events error = nil")
	}
	if _, err := db.Exec(`DELETE FROM events`); err == nil {
		t.Fatal("DELETE FROM events error = nil")
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	cases := map[string]string{
		"payload": `UPDATE events SET payload = '{"n":99}' WHERE sequence = 2`,
		"payload and payload_hash": `UPDATE events SET payload = '{"n":99}',
			payload_hash = '` + hashHex([]byte(`{"n":99}`)) + `' WHERE sequence = 2`,
		"event_hash":    `UPDATE events SET event_hash = '` + hashHex([]byte("forged")) + `' WHERE sequence = 2`,
		"previous_hash": `UPDATE events SET previous_hash = '' WHERE sequence = 2`,
		"type":          `UPDATE events SET type = 'action.passed' WHERE sequence = 1`,
		"actor":         `UPDATE events SET actor = '{"kind":"user"}' WHERE sequence = 3`,
		"head_hash":     `UPDATE projects SET head_hash = '` + hashHex([]byte("forged")) + `'`,
		"last event":    `DELETE FROM events WHERE sequence = 3`,
		"middle event":  `DELETE FROM events WHERE sequence = 2`,
	}
	for name, statement := range cases {
		t.Run(name, func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "events.db"))
			for i := range 3 {
				mustAppend(t, store, testRequest("proj-a", fmt.Sprintf(`{"n":%d}`, i)))
			}
			mustVerify(t, store, "proj-a")

			tamper(t, store, statement)
			if err := store.Verify(context.Background(), "proj-a"); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("Verify() error = %v, want ErrIntegrity", err)
			}
		})
	}
}

func TestConcurrentAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	// Two Store instances on one file stand in for two processes.
	stores := []*Store{openTestStore(t, path), openTestStore(t, path)}

	const workers = 8
	const perWorker = 25
	var wait sync.WaitGroup
	errs := make(chan error, workers*perWorker)
	for worker := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			store := stores[worker%len(stores)]
			for i := range perWorker {
				projectID := "proj-a"
				if i%5 == 0 {
					projectID = "proj-b"
				}
				request := testRequest(projectID, fmt.Sprintf(`{"worker":%d,"i":%d}`, worker, i))
				if _, err := store.Append(context.Background(), request); err != nil {
					errs <- err
				}
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Append() error = %v", err)
	}

	wantCounts := map[string]int{"proj-a": workers * perWorker * 4 / 5, "proj-b": workers * perWorker / 5}
	for projectID, want := range wantCounts {
		events, err := stores[0].List(context.Background(), projectID, 0, 1000)
		if err != nil {
			t.Fatalf("List(%q) error = %v", projectID, err)
		}
		if len(events) != want {
			t.Fatalf("List(%q) returned %d events, want %d", projectID, len(events), want)
		}
		seen := map[uint64]bool{}
		for i, event := range events {
			if seen[event.Sequence] || event.Sequence != uint64(i+1) {
				t.Fatalf("%s: duplicate or missing sequence at %d: %d", projectID, i, event.Sequence)
			}
			seen[event.Sequence] = true
		}
		mustVerify(t, stores[1], projectID)
	}
}

func TestOpenRejectsBadPath(t *testing.T) {
	for _, path := range []string{"", "  ", "events.db?mode=ro"} {
		if _, err := Open(path); err == nil {
			t.Fatalf("Open(%q) error = nil", path)
		}
	}
}

// tamper bypasses the append-only triggers the way a local user editing the
// SQLite file could.
func tamper(t *testing.T, store *Store, statement string) {
	t.Helper()
	db := store.db
	for _, drop := range []string{`DROP TRIGGER events_no_update`, `DROP TRIGGER events_no_delete`} {
		if _, err := db.Exec(drop); err != nil {
			t.Fatalf("%s: %v", drop, err)
		}
	}
	if _, err := db.Exec(statement); err != nil {
		t.Fatalf("tamper %q: %v", statement, err)
	}
}
