package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var baseTime = time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)

// testClock advances by step on every reading so updated_at is deterministic.
type testClock struct {
	mu      sync.Mutex
	current time.Time
	step    time.Duration
}

func (clock *testClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.current = clock.current.Add(clock.step)
	return clock.current
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	clock := &testClock{current: baseTime, step: time.Second}
	store.now = clock.Now
	t.Cleanup(func() { store.Close() })
	return store
}

func newTestStore(t *testing.T) *Store {
	return openTestStore(t, filepath.Join(t.TempDir(), "memory.db"))
}

func candidateRequest(projectID string, priority Priority, claim string) CreateRequest {
	return CreateRequest{
		ProjectID:    projectID,
		WorkspaceID:  "ws_1",
		Kind:         "decision",
		Priority:     priority,
		Claim:        claim,
		Scope:        json.RawMessage(`{"paths":["internal/**"],"modules":["core"]}`),
		EvidenceRefs: []string{"event:1"},
		Source:       "tool_evidence",
		Confidence:   0.8,
	}
}

func mustCreate(t *testing.T, store *Store, request CreateRequest) Memory {
	t.Helper()
	value, err := store.CreateCandidate(context.Background(), request)
	if err != nil {
		t.Fatalf("CreateCandidate() error = %v", err)
	}
	return value
}

func mustActive(t *testing.T, store *Store, request CreateRequest) Memory {
	t.Helper()
	value := mustCreate(t, store, request)
	active, err := store.Activate(context.Background(), value.ProjectID, value.MemoryID, "evidence reviewed")
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	return active
}

func retrieve(t *testing.T, store *Store, query Query) []Memory {
	t.Helper()
	if query.Limit == 0 {
		query.Limit = MaximumRetrieveLimit
	}
	values, err := store.Retrieve(context.Background(), query)
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	return values
}

func ids(values []Memory) []string {
	result := []string{}
	for _, value := range values {
		result = append(result, value.MemoryID)
	}
	return result
}

func assertIDs(t *testing.T, got []Memory, want ...Memory) {
	t.Helper()
	if fmt.Sprint(ids(got)) != fmt.Sprint(ids(want)) {
		t.Fatalf("retrieved %v, want %v", ids(got), ids(want))
	}
}

func assertHistory(t *testing.T, store *Store, value Memory, actions ...Action) []Revision {
	t.Helper()
	history, err := store.History(context.Background(), value.ProjectID, value.MemoryID)
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	if len(history) != len(actions) {
		t.Fatalf("history has %d revisions, want %d", len(history), len(actions))
	}
	for i, revision := range history {
		if revision.Memory.Revision != uint64(i+1) || revision.Action != actions[i] {
			t.Fatalf("revision %d = %d %s, want %d %s", i, revision.Memory.Revision, revision.Action, i+1, actions[i])
		}
	}
	return history
}

func TestCandidateNotRetrievable(t *testing.T) {
	store := newTestStore(t)
	candidate := mustCreate(t, store, candidateRequest("proj_a", P1, "use staging"))

	if candidate.Status != StatusCandidate || candidate.Revision != 1 || len(candidate.ContentHash) != 64 {
		t.Fatalf("CreateCandidate() = %+v", candidate)
	}
	if string(candidate.Scope) != `{"modules":["core"],"paths":["internal/**"]}` {
		t.Fatalf("scope = %s, want canonical JSON", candidate.Scope)
	}
	assertIDs(t, retrieve(t, store, Query{ProjectID: "proj_a"}))
	assertIDs(t, retrieve(t, store, Query{ProjectID: "proj_a", IncludeContested: true}))
}

func TestActivateMakesRetrievable(t *testing.T) {
	store := newTestStore(t)
	active := mustActive(t, store, candidateRequest("proj_a", P1, "use staging"))

	if active.Status != StatusActive || active.Revision != 2 {
		t.Fatalf("Activate() = %+v", active)
	}
	assertIDs(t, retrieve(t, store, Query{ProjectID: "proj_a"}), active)

	request := candidateRequest("proj_a", P1, "no evidence yet")
	request.EvidenceRefs = nil
	bare := mustCreate(t, store, request)
	if _, err := store.Activate(context.Background(), "proj_a", bare.MemoryID, "looks right"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Activate() without evidence error = %v, want ErrInvalidTransition", err)
	}
}

func TestConfirmedBeforeActive(t *testing.T) {
	store := newTestStore(t)
	confirmed := mustActive(t, store, candidateRequest("proj_a", P2, "first"))
	active := mustActive(t, store, candidateRequest("proj_a", P2, "second"))

	confirmed, err := store.Confirm(context.Background(), "proj_a", confirmed.MemoryID, []string{"event:9", "event:1"})
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if fmt.Sprint(confirmed.EvidenceRefs) != "[event:1 event:9]" {
		t.Fatalf("EvidenceRefs = %v, want merged and sorted", confirmed.EvidenceRefs)
	}
	// Make the active memory the most recently updated one.
	active, err = store.Contest(context.Background(), "proj_a", active.MemoryID, "conflict")
	if err != nil {
		t.Fatalf("Contest() error = %v", err)
	}
	active, err = store.Activate(context.Background(), "proj_a", active.MemoryID, "conflict resolved")
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if !active.UpdatedAt.After(confirmed.UpdatedAt) {
		t.Fatal("test setup: active memory must be newer")
	}
	assertIDs(t, retrieve(t, store, Query{ProjectID: "proj_a"}), confirmed, active)
}

func TestPriorityOrder(t *testing.T) {
	store := newTestStore(t)
	p2 := mustActive(t, store, candidateRequest("proj_a", P2, "decision"))
	p1 := mustActive(t, store, candidateRequest("proj_a", P1, "blocker"))
	p0 := mustActive(t, store, candidateRequest("proj_a", P0, "never touch prod"))
	p6 := mustActive(t, store, candidateRequest("proj_a", P6, "temporary note"))
	if _, err := store.Confirm(context.Background(), "proj_a", p2.MemoryID, []string{"event:2"}); err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}

	assertIDs(t, retrieve(t, store, Query{ProjectID: "proj_a"}), p0, p1, p2, p6)

	maximum := P1
	assertIDs(t, retrieve(t, store, Query{ProjectID: "proj_a", MaximumPriority: &maximum}), p0, p1)
}

func TestNonRetrievableStatesLeaveImmediately(t *testing.T) {
	cases := map[string]func(*Store, Memory) error{
		"contested": func(store *Store, value Memory) error {
			_, err := store.Contest(context.Background(), value.ProjectID, value.MemoryID, "new evidence conflicts")
			return err
		},
		"stale": func(store *Store, value Memory) error {
			_, err := store.MarkStale(context.Background(), value.ProjectID, value.MemoryID, "input digest changed")
			return err
		},
		"retracted": func(store *Store, value Memory) error {
			_, err := store.Retract(context.Background(), value.ProjectID, value.MemoryID, "user said it is wrong")
			return err
		},
		"archived": func(store *Store, value Memory) error {
			_, err := store.Archive(context.Background(), value.ProjectID, value.MemoryID, "task finished")
			return err
		},
		"superseded": func(store *Store, value Memory) error {
			_, err := store.Correct(context.Background(), value.ProjectID, value.MemoryID, CorrectRequest{
				Claim: "corrected", EvidenceRefs: []string{"event:7"}, Source: "user", Confidence: 1, Reason: "fix",
			})
			return err
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			store := newTestStore(t)
			value := mustActive(t, store, candidateRequest("proj_a", P1, "claim"))
			if err := change(store, value); err != nil {
				t.Fatalf("change error = %v", err)
			}
			for _, memory := range retrieve(t, store, Query{ProjectID: "proj_a"}) {
				if memory.MemoryID == value.MemoryID {
					t.Fatalf("%s memory is still retrieved", name)
				}
			}
		})
	}

	store := newTestStore(t)
	value := mustActive(t, store, candidateRequest("proj_a", P1, "claim"))
	if _, err := store.Contest(context.Background(), "proj_a", value.MemoryID, "conflict"); err != nil {
		t.Fatalf("Contest() error = %v", err)
	}
	audit := retrieve(t, store, Query{ProjectID: "proj_a", IncludeContested: true})
	if len(audit) != 1 || audit[0].Status != StatusContested {
		t.Fatalf("audit Retrieve() = %+v, want the contested memory", audit)
	}
}

func TestCorrectIsAtomic(t *testing.T) {
	store := newTestStore(t)
	old := mustActive(t, store, candidateRequest("proj_a", P0, "deploy to prod is allowed"))

	replacement, err := store.Correct(context.Background(), "proj_a", old.MemoryID, CorrectRequest{
		Claim:        "deploy to prod requires approval",
		EvidenceRefs: []string{"event:42"},
		Source:       "user_correction",
		Confidence:   1,
		Reason:       "user corrected the deployment rule",
	})
	if err != nil {
		t.Fatalf("Correct() error = %v", err)
	}
	if replacement.Status != StatusActive || replacement.SupersedesMemoryID != old.MemoryID ||
		replacement.Priority != P0 || replacement.Kind != old.Kind || string(replacement.Scope) != string(old.Scope) {
		t.Fatalf("replacement = %+v", replacement)
	}

	superseded, err := store.Get(context.Background(), "proj_a", old.MemoryID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if superseded.Status != StatusSuperseded || superseded.Claim != old.Claim {
		t.Fatalf("old memory = %+v", superseded)
	}
	assertIDs(t, retrieve(t, store, Query{ProjectID: "proj_a"}), replacement)

	oldHistory := assertHistory(t, store, old, ActionCreate, ActionActivate, ActionSupersede)
	newHistory := assertHistory(t, store, replacement, ActionCorrect)
	for _, revision := range []Revision{oldHistory[2], newHistory[0]} {
		if revision.Reason != "user corrected the deployment rule" || fmt.Sprint(revision.ChangeEvidenceRefs) != "[event:42]" {
			t.Fatalf("revision lost correction reason or evidence: %+v", revision)
		}
	}
	if oldHistory[2].RelatedMemoryID != replacement.MemoryID || newHistory[0].RelatedMemoryID != old.MemoryID {
		t.Fatal("correction revisions do not reference each other")
	}

	// A failing correction must leave nothing behind.
	other := mustActive(t, store, candidateRequest("proj_a", P2, "other"))
	_, err = store.Correct(context.Background(), "proj_a", other.MemoryID, CorrectRequest{
		Claim: "x", Scope: json.RawMessage(`[]`), EvidenceRefs: []string{"event:1"}, Source: "user", Confidence: 1, Reason: "fix",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Correct() with array scope error = %v, want ErrInvalidInput", err)
	}
	unchanged, err := store.Get(context.Background(), "proj_a", other.MemoryID)
	if err != nil || unchanged.Status != StatusActive || unchanged.Revision != other.Revision {
		t.Fatalf("failed correction changed old memory: %+v, %v", unchanged, err)
	}
	assertIDs(t, retrieve(t, store, Query{ProjectID: "proj_a"}), replacement, other)

	if _, err := store.Correct(context.Background(), "proj_a", old.MemoryID, CorrectRequest{
		Claim: "again", EvidenceRefs: []string{"event:1"}, Source: "user", Confidence: 1, Reason: "fix",
	}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Correct() of superseded memory error = %v, want ErrInvalidTransition", err)
	}
}

func TestRetractedCannotReactivate(t *testing.T) {
	store := newTestStore(t)
	value := mustActive(t, store, candidateRequest("proj_a", P1, "wrong"))
	if _, err := store.Retract(context.Background(), "proj_a", value.MemoryID, "confirmed wrong"); err != nil {
		t.Fatalf("Retract() error = %v", err)
	}

	if _, err := store.Activate(context.Background(), "proj_a", value.MemoryID, "try again"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Activate() of retracted error = %v, want ErrInvalidTransition", err)
	}
	if _, err := store.Correct(context.Background(), "proj_a", value.MemoryID, CorrectRequest{
		Claim: "right", EvidenceRefs: []string{"event:1"}, Source: "user", Confidence: 1, Reason: "fix",
	}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Correct() of retracted error = %v, want ErrInvalidTransition", err)
	}

	retracted, err := store.Get(context.Background(), "proj_a", value.MemoryID)
	if err != nil || retracted.Status != StatusRetracted || retracted.Claim != "wrong" {
		t.Fatalf("Get() after retract = %+v, %v; want retained retracted memory", retracted, err)
	}
	assertHistory(t, store, value, ActionCreate, ActionActivate, ActionRetract)
}

func TestExpiredNotRetrieved(t *testing.T) {
	store := newTestStore(t)
	expiring := candidateRequest("proj_a", P3, "tests pass on digest X")
	expiring.ValidUntil = baseTime.Add(time.Hour)
	expires := mustActive(t, store, expiring)
	future := candidateRequest("proj_a", P3, "applies tomorrow")
	future.ValidFrom = baseTime.Add(24 * time.Hour)
	later := mustActive(t, store, future)
	permanent := mustActive(t, store, candidateRequest("proj_a", P3, "permanent"))

	assertIDs(t, retrieve(t, store, Query{ProjectID: "proj_a", AsOf: baseTime.Add(time.Minute)}), permanent, expires)
	assertIDs(t, retrieve(t, store, Query{ProjectID: "proj_a", AsOf: baseTime.Add(time.Hour)}), permanent)
	assertIDs(t, retrieve(t, store, Query{ProjectID: "proj_a", AsOf: baseTime.Add(25 * time.Hour)}), permanent, later)
}

func TestArchiveExpiredSkipsP0P1(t *testing.T) {
	store := newTestStore(t)
	byPriority := map[Priority]Memory{}
	for _, priority := range []Priority{P0, P1, P2, P5} {
		request := candidateRequest("proj_a", priority, priority.String())
		request.ValidUntil = baseTime.Add(time.Hour)
		byPriority[priority] = mustActive(t, store, request)
	}
	notExpired := candidateRequest("proj_a", P5, "still valid")
	notExpired.ValidUntil = baseTime.Add(48 * time.Hour)
	valid := mustActive(t, store, notExpired)
	otherProject := candidateRequest("proj_b", P5, "other project")
	otherProject.ValidUntil = baseTime.Add(time.Hour)
	other := mustActive(t, store, otherProject)

	count, err := store.ArchiveExpired(context.Background(), "proj_a", baseTime.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("ArchiveExpired() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("ArchiveExpired() = %d, want 2", count)
	}

	want := map[string]Status{
		byPriority[P0].MemoryID: StatusActive,
		byPriority[P1].MemoryID: StatusActive,
		byPriority[P2].MemoryID: StatusArchived,
		byPriority[P5].MemoryID: StatusArchived,
		valid.MemoryID:          StatusActive,
	}
	for id, status := range want {
		value, err := store.Get(context.Background(), "proj_a", id)
		if err != nil || value.Status != status {
			t.Fatalf("memory %s = %s, %v; want %s", value.Claim, value.Status, err, status)
		}
	}
	if value, _ := store.Get(context.Background(), "proj_b", other.MemoryID); value.Status != StatusActive {
		t.Fatalf("other project memory = %s, want active", value.Status)
	}
	assertHistory(t, store, byPriority[P2], ActionCreate, ActionActivate, ActionExpire)

	if count, err := store.ArchiveExpired(context.Background(), "proj_a", baseTime.Add(2*time.Hour)); err != nil || count != 0 {
		t.Fatalf("second ArchiveExpired() = %d, %v; want 0", count, err)
	}
}

func TestProjectIsolation(t *testing.T) {
	store := newTestStore(t)
	a := mustActive(t, store, candidateRequest("proj_a", P1, "a"))
	b := mustActive(t, store, candidateRequest("proj_b", P1, "b"))

	assertIDs(t, retrieve(t, store, Query{ProjectID: "proj_a"}), a)
	assertIDs(t, retrieve(t, store, Query{ProjectID: "proj_b"}), b)

	ctx := context.Background()
	if _, err := store.Get(ctx, "proj_a", b.MemoryID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() across projects error = %v, want ErrNotFound", err)
	}
	if _, err := store.History(ctx, "proj_a", b.MemoryID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("History() across projects error = %v, want ErrNotFound", err)
	}
	if _, err := store.Retract(ctx, "proj_a", b.MemoryID, "cross-project"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Retract() across projects error = %v, want ErrNotFound", err)
	}
	if _, err := store.Correct(ctx, "proj_a", b.MemoryID, CorrectRequest{
		Claim: "x", EvidenceRefs: []string{"event:1"}, Source: "user", Confidence: 1, Reason: "fix",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Correct() across projects error = %v, want ErrNotFound", err)
	}
	if value, err := store.Get(ctx, "proj_b", b.MemoryID); err != nil || value.Revision != b.Revision {
		t.Fatalf("project b memory changed: %+v, %v", value, err)
	}
}

func TestWorkspaceAndKindFilters(t *testing.T) {
	store := newTestStore(t)
	own := mustActive(t, store, candidateRequest("proj_a", P2, "own workspace"))
	projectWideRequest := candidateRequest("proj_a", P2, "project wide")
	projectWideRequest.WorkspaceID = ""
	projectWideRequest.Kind = "project_fact"
	projectWide := mustActive(t, store, projectWideRequest)
	otherRequest := candidateRequest("proj_a", P2, "other workspace")
	otherRequest.WorkspaceID = "ws_2"
	mustActive(t, store, otherRequest)

	assertIDs(t, retrieve(t, store, Query{ProjectID: "proj_a", WorkspaceID: "ws_1"}), projectWide, own)
	assertIDs(t, retrieve(t, store, Query{ProjectID: "proj_a", WorkspaceID: "ws_1", Kinds: []string{"project_fact"}}), projectWide)
}

func TestRevisionHistoryContiguous(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	value := mustActive(t, store, candidateRequest("proj_a", P2, "claim"))
	steps := []func() (Memory, error){
		func() (Memory, error) { return store.Confirm(ctx, "proj_a", value.MemoryID, []string{"event:2"}) },
		func() (Memory, error) { return store.MarkStale(ctx, "proj_a", value.MemoryID, "inputs changed") },
		func() (Memory, error) { return store.Confirm(ctx, "proj_a", value.MemoryID, []string{"event:3"}) },
		func() (Memory, error) { return store.Contest(ctx, "proj_a", value.MemoryID, "conflict") },
		func() (Memory, error) { return store.Activate(ctx, "proj_a", value.MemoryID, "resolved") },
		func() (Memory, error) { return store.Archive(ctx, "proj_a", value.MemoryID, "done") },
	}
	for _, step := range steps {
		if _, err := step(); err != nil {
			t.Fatalf("step error = %v", err)
		}
	}
	history := assertHistory(t, store, value, ActionCreate, ActionActivate, ActionConfirm, ActionMarkStale,
		ActionConfirm, ActionContest, ActionActivate, ActionArchive)

	current, err := store.Get(ctx, "proj_a", value.MemoryID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	last := history[len(history)-1].Memory
	if current.Revision != last.Revision || current.Status != last.Status || current.ContentHash != last.ContentHash {
		t.Fatalf("current %+v does not match last revision %+v", current, last)
	}
	if history[1].Memory.ContentHash == history[2].Memory.ContentHash {
		t.Fatal("adding evidence did not change content_hash")
	}
	if history[0].Memory.ContentHash != history[1].Memory.ContentHash {
		t.Fatal("a status-only change altered content_hash")
	}

	// The database itself refuses gaps. The row extends the chain head
	// correctly, so only the contiguity trigger can reject it.
	_, err = store.db.Exec(`INSERT INTO memory_revisions
		SELECT project_id, memory_id, revision + 2, workspace_id, status, priority, kind, claim, scope,
			evidence_refs, source, confidence, valid_from, valid_until, created_at, updated_at,
			supersedes_memory_id, content_hash, action, reason, change_evidence_refs, related_memory_id, recorded_at,
			(SELECT head_sequence + 1 FROM memory_chain_heads WHERE project_id = 'proj_a'),
			(SELECT head_hash FROM memory_chain_heads WHERE project_id = 'proj_a'), 'forged'
		FROM memory_revisions WHERE memory_id = ? AND revision = ?`, value.MemoryID, last.Revision)
	if err == nil || !strings.Contains(err.Error(), "contiguous") {
		t.Fatalf("inserting a revision gap error = %v, want the contiguity trigger", err)
	}
}

func TestReopenKeepsState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	first := openTestStore(t, path)
	value := mustActive(t, first, candidateRequest("proj_a", P1, "persist me"))
	confirmed, err := first.Confirm(context.Background(), "proj_a", value.MemoryID, []string{"event:5"})
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened := openTestStore(t, path)
	got, err := reopened.Get(context.Background(), "proj_a", value.MemoryID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(confirmed)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("after reopen:\n got %s\nwant %s", gotJSON, wantJSON)
	}
	if _, err := reopened.MarkStale(context.Background(), "proj_a", value.MemoryID, "after reopen"); err != nil {
		t.Fatalf("MarkStale() after reopen error = %v", err)
	}
	assertHistory(t, reopened, value, ActionCreate, ActionActivate, ActionConfirm, ActionMarkStale)
}

func TestConcurrentUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	// Two Store instances on one file stand in for two processes.
	stores := []*Store{openTestStore(t, path), openTestStore(t, path)}
	value := mustActive(t, stores[0], candidateRequest("proj_a", P2, "shared"))

	const workers = 16
	var wait sync.WaitGroup
	errs := make(chan error, workers)
	for worker := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			store := stores[worker%len(stores)]
			if _, err := store.Confirm(context.Background(), "proj_a", value.MemoryID, []string{fmt.Sprintf("event:w%02d", worker)}); err != nil {
				errs <- err
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Confirm() error = %v", err)
	}

	history, err := stores[1].History(context.Background(), "proj_a", value.MemoryID)
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	if len(history) != workers+2 {
		t.Fatalf("history has %d revisions, want %d", len(history), workers+2)
	}
	for i, revision := range history {
		if revision.Memory.Revision != uint64(i+1) {
			t.Fatalf("revision %d has number %d", i, revision.Memory.Revision)
		}
	}
	current, err := stores[1].Get(context.Background(), "proj_a", value.MemoryID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if current.Revision != uint64(workers+2) || len(current.EvidenceRefs) != workers+1 {
		t.Fatalf("current revision %d evidence %d, want %d / %d", current.Revision, len(current.EvidenceRefs), workers+2, workers+1)
	}
}

func TestInvalidInputRejected(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	mutations := map[string]func(*CreateRequest){
		"empty claim":          func(r *CreateRequest) { r.Claim = "   " },
		"bad project_id":       func(r *CreateRequest) { r.ProjectID = "proj a" },
		"empty project_id":     func(r *CreateRequest) { r.ProjectID = "" },
		"bad workspace_id":     func(r *CreateRequest) { r.WorkspaceID = "ws/1" },
		"bad kind":             func(r *CreateRequest) { r.Kind = "Decision!" },
		"array scope":          func(r *CreateRequest) { r.Scope = json.RawMessage(`["a"]`) },
		"string scope":         func(r *CreateRequest) { r.Scope = json.RawMessage(`"a"`) },
		"malformed scope":      func(r *CreateRequest) { r.Scope = json.RawMessage(`{"a":`) },
		"missing scope":        func(r *CreateRequest) { r.Scope = nil },
		"trailing scope":       func(r *CreateRequest) { r.Scope = json.RawMessage(`{} {}`) },
		"negative confidence":  func(r *CreateRequest) { r.Confidence = -0.01 },
		"confidence above one": func(r *CreateRequest) { r.Confidence = 1.01 },
		"NaN confidence":       func(r *CreateRequest) { r.Confidence = math.NaN() },
		"priority below P0":    func(r *CreateRequest) { r.Priority = -1 },
		"priority above P6":    func(r *CreateRequest) { r.Priority = 7 },
		"empty source":         func(r *CreateRequest) { r.Source = "" },
		"empty evidence ref":   func(r *CreateRequest) { r.EvidenceRefs = []string{" "} },
		"valid_until early":    func(r *CreateRequest) { r.ValidFrom = baseTime; r.ValidUntil = baseTime },
	}
	for name, mutate := range mutations {
		request := candidateRequest("proj_a", P2, "claim")
		mutate(&request)
		if _, err := store.CreateCandidate(ctx, request); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s: CreateCandidate() error = %v, want ErrInvalidInput", name, err)
		}
	}
	assertIDs(t, retrieve(t, store, Query{ProjectID: "proj_a", IncludeContested: true}))

	candidate := mustCreate(t, store, candidateRequest("proj_a", P2, "candidate"))
	invalidTransitions := map[string]func() (Memory, error){
		"confirm candidate": func() (Memory, error) { return store.Confirm(ctx, "proj_a", candidate.MemoryID, []string{"event:1"}) },
		"stale candidate":   func() (Memory, error) { return store.MarkStale(ctx, "proj_a", candidate.MemoryID, "r") },
		"contest candidate": func() (Memory, error) { return store.Contest(ctx, "proj_a", candidate.MemoryID, "r") },
		"correct candidate": func() (Memory, error) {
			return store.Correct(ctx, "proj_a", candidate.MemoryID, CorrectRequest{
				Claim: "x", EvidenceRefs: []string{"event:1"}, Source: "user", Confidence: 1, Reason: "r",
			})
		},
	}
	for name, transition := range invalidTransitions {
		if _, err := transition(); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("%s error = %v, want ErrInvalidTransition", name, err)
		}
	}
	archived, err := store.Archive(ctx, "proj_a", candidate.MemoryID, "unused")
	if err != nil {
		t.Fatalf("Archive() error = %v", err)
	}
	if _, err := store.Activate(ctx, "proj_a", archived.MemoryID, "revive"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Activate() of archived error = %v, want ErrInvalidTransition", err)
	}

	active := mustActive(t, store, candidateRequest("proj_a", P2, "active"))
	invalidRequests := map[string]func() (Memory, error){
		"activate without reason": func() (Memory, error) { return store.Activate(ctx, "proj_a", active.MemoryID, " ") },
		"confirm without evidence": func() (Memory, error) { return store.Confirm(ctx, "proj_a", active.MemoryID, nil) },
		"bad memory_id":            func() (Memory, error) { return store.Retract(ctx, "proj_a", "mem 1", "r") },
		"correct without evidence": func() (Memory, error) {
			return store.Correct(ctx, "proj_a", active.MemoryID, CorrectRequest{Claim: "x", Source: "user", Confidence: 1, Reason: "r"})
		},
		"correct with bad confidence": func() (Memory, error) {
			return store.Correct(ctx, "proj_a", active.MemoryID, CorrectRequest{
				Claim: "x", EvidenceRefs: []string{"event:1"}, Source: "user", Confidence: 2, Reason: "r",
			})
		},
	}
	for name, request := range invalidRequests {
		if _, err := request(); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s error = %v, want ErrInvalidInput", name, err)
		}
	}

	bad := P6 + 1
	for name, query := range map[string]Query{
		"limit zero":       {ProjectID: "proj_a", Limit: 0},
		"limit too large":  {ProjectID: "proj_a", Limit: MaximumRetrieveLimit + 1},
		"missing project":  {Limit: 10},
		"bad priority":     {ProjectID: "proj_a", Limit: 10, MaximumPriority: &bad},
		"bad kind filter":  {ProjectID: "proj_a", Limit: 10, Kinds: []string{"x'); DROP TABLE memories; --"}},
		"bad workspace_id": {ProjectID: "proj_a", Limit: 10, WorkspaceID: "ws 1"},
	} {
		if _, err := store.Retrieve(ctx, query); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s: Retrieve() error = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestRetrieveStableOrder(t *testing.T) {
	store := newTestStore(t)
	// A frozen clock gives every memory the same updated_at, so memory_id
	// must break the tie.
	store.now = func() time.Time { return baseTime }

	var created []Memory
	for i := range 6 {
		created = append(created, mustActive(t, store, candidateRequest("proj_a", P3, fmt.Sprintf("claim %d", i))))
	}
	first := retrieve(t, store, Query{ProjectID: "proj_a"})
	for i := 1; i < len(first); i++ {
		if first[i-1].MemoryID >= first[i].MemoryID {
			t.Fatalf("equal-rank memories not ordered by memory_id: %v", ids(first))
		}
	}
	for range 5 {
		assertIDs(t, retrieve(t, store, Query{ProjectID: "proj_a"}), first...)
	}

	limited := retrieve(t, store, Query{ProjectID: "proj_a", Limit: 3})
	assertIDs(t, limited, first[:3]...)
	if len(created) != len(first) {
		t.Fatalf("retrieved %d of %d memories", len(first), len(created))
	}
}

func TestHistoryIsProtectedByTriggers(t *testing.T) {
	store := newTestStore(t)
	value := mustActive(t, store, candidateRequest("proj_a", P1, "protected"))

	statements := []string{
		`UPDATE memory_revisions SET claim = 'rewritten'`,
		`UPDATE memory_revisions SET status = 'active' WHERE revision = 1`,
		`DELETE FROM memory_revisions`,
		`DELETE FROM memories`,
		`UPDATE memories SET claim = 'rewritten'`,
		`UPDATE memories SET status = 'confirmed'`,
		`UPDATE memories SET revision = revision + 1, status = 'confirmed'`,
	}
	for _, statement := range statements {
		if _, err := store.db.Exec(statement); err == nil {
			t.Fatalf("%q succeeded", statement)
		}
	}
	got, err := store.Get(context.Background(), "proj_a", value.MemoryID)
	if err != nil || got.Claim != "protected" || got.Revision != value.Revision || got.Status != StatusActive {
		t.Fatalf("memory changed: %+v, %v", got, err)
	}
	assertHistory(t, store, value, ActionCreate, ActionActivate)
}

func TestOpenRejectsForeignDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE events (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create foreign table: %v", err)
	}
	db.Close()

	if _, err := Open(path); err == nil {
		t.Fatal("Open() of a foreign database error = nil")
	}
	for _, bad := range []string{"", "memory.db?mode=ro"} {
		if _, err := Open(bad); err == nil {
			t.Fatalf("Open(%q) error = nil", bad)
		}
	}
}
