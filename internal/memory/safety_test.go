package memory

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func correctRequest(claim string) CorrectRequest {
	return CorrectRequest{
		Claim:        claim,
		EvidenceRefs: []string{"event:fix"},
		Source:       "user_correction",
		Confidence:   1,
		Reason:       "user corrected it",
	}
}

func mustGet(t *testing.T, store *Store, projectID, memoryID string) Memory {
	t.Helper()
	value, err := store.Get(context.Background(), projectID, memoryID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	return value
}

func assertUnchanged(t *testing.T, store *Store, want Memory) {
	t.Helper()
	got := mustGet(t, store, want.ProjectID, want.MemoryID)
	if !sameJSON(got, want) {
		t.Fatalf("memory %s changed:\n got %+v\nwant %+v", want.MemoryID, got, want)
	}
	history, err := store.History(context.Background(), want.ProjectID, want.MemoryID)
	if err != nil || uint64(len(history)) != want.Revision {
		t.Fatalf("history has %d revisions (%v), want %d", len(history), err, want.Revision)
	}
}

func mustVerify(t *testing.T, store *Store, projectID string) {
	t.Helper()
	if err := store.Verify(context.Background(), projectID); err != nil {
		t.Fatalf("Verify(%q) error = %v", projectID, err)
	}
}

// race runs both functions at the same moment and returns their errors.
func race(first, second func() error) (error, error) {
	start := make(chan struct{})
	var wait sync.WaitGroup
	var firstErr, secondErr error
	wait.Add(2)
	go func() { defer wait.Done(); <-start; firstErr = first() }()
	go func() { defer wait.Done(); <-start; secondErr = second() }()
	close(start)
	wait.Wait()
	return firstErr, secondErr
}

func exactlyOneWins(t *testing.T, first, second error) {
	t.Helper()
	switch {
	case first == nil && errors.Is(second, ErrRevisionConflict):
	case second == nil && errors.Is(first, ErrRevisionConflict):
	default:
		t.Fatalf("want exactly one success and one ErrRevisionConflict, got %v / %v", first, second)
	}
}

func TestAtRevisionConcurrentStoresExactlyOneWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	stores := []*Store{openTestStore(t, path), openTestStore(t, path)}
	ctx := context.Background()
	value := mustActive(t, stores[0], candidateRequest("proj_a", P2, "shared"))

	for round := range 20 {
		expected := mustGet(t, stores[0], "proj_a", value.MemoryID).Revision
		first, second := race(
			func() error {
				_, err := stores[0].ConfirmAtRevision(ctx, "proj_a", value.MemoryID, expected, []string{fmt.Sprintf("event:a%d", round)})
				return err
			},
			func() error {
				_, err := stores[1].ConfirmAtRevision(ctx, "proj_a", value.MemoryID, expected, []string{fmt.Sprintf("event:b%d", round)})
				return err
			},
		)
		exactlyOneWins(t, first, second)
		if got := mustGet(t, stores[1], "proj_a", value.MemoryID).Revision; got != expected+1 {
			t.Fatalf("round %d: revision %d, want %d", round, got, expected+1)
		}
	}

	// Different actions on the same expected revision also serialize.
	expected := mustGet(t, stores[0], "proj_a", value.MemoryID).Revision
	first, second := race(
		func() error {
			_, err := stores[0].ContestAtRevision(ctx, "proj_a", value.MemoryID, expected, "conflict")
			return err
		},
		func() error {
			_, err := stores[1].RetractAtRevision(ctx, "proj_a", value.MemoryID, expected, "wrong")
			return err
		},
	)
	exactlyOneWins(t, first, second)
	mustVerify(t, stores[0], "proj_a")
}

func TestAtRevisionRejectsStaleExpectation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	value := mustActive(t, store, candidateRequest("proj_a", P2, "claim"))

	calls := map[string]func(uint64) (Memory, error){
		"activate": func(revision uint64) (Memory, error) {
			return store.ActivateAtRevision(ctx, "proj_a", value.MemoryID, revision, "r")
		},
		"confirm": func(revision uint64) (Memory, error) {
			return store.ConfirmAtRevision(ctx, "proj_a", value.MemoryID, revision, []string{"event:2"})
		},
		"mark stale": func(revision uint64) (Memory, error) {
			return store.MarkStaleAtRevision(ctx, "proj_a", value.MemoryID, revision, "r")
		},
		"contest": func(revision uint64) (Memory, error) {
			return store.ContestAtRevision(ctx, "proj_a", value.MemoryID, revision, "r")
		},
		"retract": func(revision uint64) (Memory, error) {
			return store.RetractAtRevision(ctx, "proj_a", value.MemoryID, revision, "r")
		},
		"archive": func(revision uint64) (Memory, error) {
			return store.ArchiveAtRevision(ctx, "proj_a", value.MemoryID, revision, "r")
		},
		"correct": func(revision uint64) (Memory, error) {
			return store.CorrectAtRevision(ctx, "proj_a", value.MemoryID, revision, correctRequest("fixed"))
		},
	}
	for name, call := range calls {
		_, err := call(1)
		if !errors.Is(err, ErrRevisionConflict) {
			t.Fatalf("%s at stale revision error = %v, want ErrRevisionConflict", name, err)
		}
		if !strings.Contains(err.Error(), "expected revision 1, actual 2") {
			t.Fatalf("%s conflict error %q lacks expected and actual revisions", name, err)
		}
		if _, err := call(0); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s at revision 0 error = %v, want ErrInvalidInput", name, err)
		}
	}
	assertUnchanged(t, store, value)

	confirmed, err := store.ConfirmAtRevision(ctx, "proj_a", value.MemoryID, value.Revision, []string{"event:2"})
	if err != nil || confirmed.Revision != value.Revision+1 {
		t.Fatalf("ConfirmAtRevision() at current revision = %+v, %v", confirmed, err)
	}
}

func TestAtRevisionWrongProjectIsNotFound(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	value := mustActive(t, store, candidateRequest("proj_a", P2, "claim"))

	if _, err := store.MarkStaleAtRevision(ctx, "proj_b", value.MemoryID, value.Revision, "r"); !errors.Is(err, ErrNotFound) || errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("wrong project error = %v, want only ErrNotFound", err)
	}
	if _, err := store.CorrectAtRevision(ctx, "proj_b", value.MemoryID, 99, correctRequest("x")); !errors.Is(err, ErrNotFound) || errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("wrong project correct error = %v, want only ErrNotFound", err)
	}
	if _, err := store.ActivateAtRevision(ctx, "proj_a", "mem_missing", 99, "r"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing memory error = %v, want ErrNotFound", err)
	}
	assertUnchanged(t, store, value)
}

func TestContestPair(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	left := mustActive(t, store, candidateRequest("proj_a", P1, "use OIDC"))
	right := mustActive(t, store, candidateRequest("proj_a", P1, "use passwords"))
	right, err := store.Confirm(ctx, "proj_a", right.MemoryID, []string{"event:3"})
	if err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}

	gotLeft, gotRight, err := store.ContestPair(ctx, "proj_a", left.MemoryID, left.Revision, right.MemoryID, right.Revision,
		"claims contradict", []string{"event:conflict"})
	if err != nil {
		t.Fatalf("ContestPair() error = %v", err)
	}
	if gotLeft.Status != StatusContested || gotRight.Status != StatusContested ||
		gotLeft.Revision != left.Revision+1 || gotRight.Revision != right.Revision+1 {
		t.Fatalf("ContestPair() = %+v / %+v", gotLeft, gotRight)
	}
	if fmt.Sprint(gotRight.EvidenceRefs) != fmt.Sprint(right.EvidenceRefs) {
		t.Fatal("conflict evidence must not become supporting evidence")
	}

	for _, pair := range [][2]Memory{{left, right}, {right, left}} {
		history, err := store.History(ctx, "proj_a", pair[0].MemoryID)
		if err != nil {
			t.Fatalf("History() error = %v", err)
		}
		last := history[len(history)-1]
		if last.Action != ActionContest || last.RelatedMemoryID != pair[1].MemoryID ||
			last.Reason != "claims contradict" || fmt.Sprint(last.ChangeEvidenceRefs) != "[event:conflict]" {
			t.Fatalf("contest revision = %+v", last)
		}
	}
	assertIDs(t, retrieve(t, store, Query{ProjectID: "proj_a"}))
	mustVerify(t, store, "proj_a")
}

func TestContestPairRollsBackOnRevisionConflict(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	left := mustActive(t, store, candidateRequest("proj_a", P1, "left"))
	right := mustActive(t, store, candidateRequest("proj_a", P1, "right"))

	_, _, err := store.ContestPair(ctx, "proj_a", left.MemoryID, left.Revision, right.MemoryID, right.Revision-1,
		"conflict", []string{"event:1"})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("ContestPair() error = %v, want ErrRevisionConflict", err)
	}
	assertUnchanged(t, store, left)
	assertUnchanged(t, store, right)
}

func TestContestPairRollsBackOnInvalidState(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	left := mustActive(t, store, candidateRequest("proj_a", P1, "left"))
	candidate := mustCreate(t, store, candidateRequest("proj_a", P1, "candidate"))
	retracted := mustActive(t, store, candidateRequest("proj_a", P1, "retracted"))
	retracted, err := store.Retract(ctx, "proj_a", retracted.MemoryID, "wrong")
	if err != nil {
		t.Fatalf("Retract() error = %v", err)
	}

	// left is applied first, so these prove the whole transaction rolls back.
	for _, right := range []Memory{candidate, retracted} {
		_, _, err := store.ContestPair(ctx, "proj_a", left.MemoryID, left.Revision, right.MemoryID, right.Revision,
			"conflict", []string{"event:1"})
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("ContestPair() with %s error = %v, want ErrInvalidTransition", right.Status, err)
		}
		assertUnchanged(t, store, left)
		assertUnchanged(t, store, right)
	}
	mustVerify(t, store, "proj_a")
}

func TestContestPairRejectsBadInput(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	left := mustActive(t, store, candidateRequest("proj_a", P1, "left"))
	right := mustActive(t, store, candidateRequest("proj_a", P1, "right"))
	foreign := mustActive(t, store, candidateRequest("proj_b", P1, "foreign"))

	cases := map[string]struct {
		rightID  string
		reason   string
		evidence []string
		want     error
	}{
		"same memory":      {left.MemoryID, "r", []string{"event:1"}, ErrInvalidInput},
		"missing reason":   {right.MemoryID, " ", []string{"event:1"}, ErrInvalidInput},
		"missing evidence": {right.MemoryID, "r", nil, ErrInvalidInput},
		"other project":    {foreign.MemoryID, "r", []string{"event:1"}, ErrNotFound},
	}
	for name, test := range cases {
		_, _, err := store.ContestPair(ctx, "proj_a", left.MemoryID, left.Revision, test.rightID, 2, test.reason, test.evidence)
		if !errors.Is(err, test.want) {
			t.Fatalf("%s: ContestPair() error = %v, want %v", name, err, test.want)
		}
	}
	assertUnchanged(t, store, left)
	assertUnchanged(t, store, right)
	assertUnchanged(t, store, foreign)
}

func TestCorrectAtRevisionConcurrentConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	stores := []*Store{openTestStore(t, path), openTestStore(t, path)}
	// Like two real processes, both stores must read one clock; otherwise
	// the winner's valid_from can lie after the other store's as_of.
	stores[1].now = stores[0].now
	ctx := context.Background()
	old := mustActive(t, stores[0], candidateRequest("proj_a", P0, "wrong rule"))

	var winners []Memory
	var mu sync.Mutex
	correct := func(store *Store, claim string) func() error {
		return func() error {
			value, err := store.CorrectAtRevision(ctx, "proj_a", old.MemoryID, old.Revision, correctRequest(claim))
			if err == nil {
				mu.Lock()
				winners = append(winners, value)
				mu.Unlock()
			}
			return err
		}
	}
	first, second := race(correct(stores[0], "fix one"), correct(stores[1], "fix two"))
	exactlyOneWins(t, first, second)

	active := retrieve(t, stores[1], Query{ProjectID: "proj_a"})
	if len(active) != 1 || active[0].MemoryID != winners[0].MemoryID || active[0].SupersedesMemoryID != old.MemoryID {
		t.Fatalf("after concurrent corrections retrieved %+v, want only the winner", active)
	}
	if got := mustGet(t, stores[1], "proj_a", old.MemoryID); got.Status != StatusSuperseded || got.Revision != old.Revision+1 {
		t.Fatalf("old memory = %+v", got)
	}
	mustVerify(t, stores[1], "proj_a")
}

// buildVerifiedProject exercises every lifecycle path so Verify sees create,
// correct, supersede, single and pairwise contest, confirm, expire and
// retract revisions.
func buildVerifiedProject(t *testing.T, store *Store) map[string]Memory {
	t.Helper()
	ctx := context.Background()
	must := func(value Memory, err error) Memory {
		t.Helper()
		if err != nil {
			t.Fatalf("setup error = %v", err)
		}
		return value
	}

	a := mustActive(t, store, candidateRequest("proj_a", P2, "a"))
	a = must(store.Confirm(ctx, "proj_a", a.MemoryID, []string{"event:a2"}))
	a = must(store.MarkStale(ctx, "proj_a", a.MemoryID, "inputs changed"))
	a = must(store.Contest(ctx, "proj_a", a.MemoryID, "single contest"))
	a = must(store.Activate(ctx, "proj_a", a.MemoryID, "resolved"))

	old := mustActive(t, store, candidateRequest("proj_a", P0, "old"))
	replacement := must(store.Correct(ctx, "proj_a", old.MemoryID, correctRequest("new")))

	x := mustActive(t, store, candidateRequest("proj_a", P1, "x"))
	y := mustActive(t, store, candidateRequest("proj_a", P1, "y"))
	if _, _, err := store.ContestPair(ctx, "proj_a", x.MemoryID, x.Revision, y.MemoryID, y.Revision, "conflict", []string{"event:c"}); err != nil {
		t.Fatalf("ContestPair() error = %v", err)
	}

	expiring := candidateRequest("proj_a", P5, "expiring")
	expiring.ValidUntil = baseTime.Add(time.Hour)
	mustActive(t, store, expiring)
	if _, err := store.ArchiveExpired(ctx, "proj_a", baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("ArchiveExpired() error = %v", err)
	}
	retracted := mustActive(t, store, candidateRequest("proj_a", P3, "retracted"))
	must(store.Retract(ctx, "proj_a", retracted.MemoryID, "wrong"))

	foreign := mustActive(t, store, candidateRequest("proj_b", P1, "foreign"))
	return map[string]Memory{"a": a, "old": old, "new": replacement, "x": x, "y": y, "foreign": foreign}
}

func TestVerifyPasses(t *testing.T) {
	store := newTestStore(t)
	buildVerifiedProject(t, store)
	mustVerify(t, store, "proj_a")
	mustVerify(t, store, "proj_b")
	mustVerify(t, store, "proj_empty")
	if err := store.Verify(context.Background(), "bad project"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Verify() with bad project_id error = %v, want ErrInvalidInput", err)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	cases := map[string]func(fixture map[string]Memory) (string, []any){
		"revision content_hash": func(f map[string]Memory) (string, []any) {
			return `UPDATE memory_revisions SET content_hash = 'forged' WHERE memory_id = ? AND revision = 1`, []any{f["a"].MemoryID}
		},
		"revision claim": func(f map[string]Memory) (string, []any) {
			return `UPDATE memory_revisions SET claim = 'forged' WHERE memory_id = ? AND revision = 2`, []any{f["a"].MemoryID}
		},
		"revision status": func(f map[string]Memory) (string, []any) {
			return `UPDATE memory_revisions SET status = 'confirmed' WHERE memory_id = ? AND revision = 2`, []any{f["a"].MemoryID}
		},
		"revision gap": func(f map[string]Memory) (string, []any) {
			return `DELETE FROM memory_revisions WHERE memory_id = ? AND revision = 3`, []any{f["a"].MemoryID}
		},
		"latest revision removed": func(f map[string]Memory) (string, []any) {
			return `DELETE FROM memory_revisions WHERE memory_id = ? AND revision = ?`, []any{f["a"].MemoryID, f["a"].Revision}
		},
		"current row claim": func(f map[string]Memory) (string, []any) {
			return `UPDATE memories SET claim = 'forged' WHERE memory_id = ?`, []any{f["a"].MemoryID}
		},
		"current row status": func(f map[string]Memory) (string, []any) {
			return `UPDATE memories SET status = 'confirmed' WHERE memory_id = ?`, []any{f["a"].MemoryID}
		},
		"current row updated_at": func(f map[string]Memory) (string, []any) {
			return `UPDATE memories SET updated_at = '2099-01-01T00:00:00.000000000Z' WHERE memory_id = ?`, []any{f["a"].MemoryID}
		},
		"missing current row": func(f map[string]Memory) (string, []any) {
			return `DELETE FROM memories WHERE memory_id = ?`, []any{f["a"].MemoryID}
		},
		"current row without revisions": func(f map[string]Memory) (string, []any) {
			return `INSERT INTO memories SELECT project_id, 'mem_orphan', revision, workspace_id, status, priority, kind,
				claim, scope, evidence_refs, source, confidence, valid_from, valid_until, created_at, updated_at,
				supersedes_memory_id, content_hash FROM memories WHERE memory_id = ?`, []any{f["a"].MemoryID}
		},
		"supersede back-reference": func(f map[string]Memory) (string, []any) {
			return `UPDATE memory_revisions SET related_memory_id = ? WHERE memory_id = ? AND action = 'supersede'`,
				[]any{f["x"].MemoryID, f["old"].MemoryID}
		},
		"correction forward reference": func(f map[string]Memory) (string, []any) {
			return `UPDATE memory_revisions SET related_memory_id = ? WHERE memory_id = ? AND action = 'correct'`,
				[]any{f["x"].MemoryID, f["new"].MemoryID}
		},
		"cross-project supersede reference": func(f map[string]Memory) (string, []any) {
			return `UPDATE memory_revisions SET related_memory_id = ? WHERE memory_id = ? AND action = 'supersede'`,
				[]any{f["foreign"].MemoryID, f["old"].MemoryID}
		},
		"one-sided contest pair": func(f map[string]Memory) (string, []any) {
			return `UPDATE memory_revisions SET related_memory_id = ? WHERE memory_id = ? AND action = 'contest'`,
				[]any{f["a"].MemoryID, f["x"].MemoryID}
		},
		"unexpected related memory": func(f map[string]Memory) (string, []any) {
			return `UPDATE memory_revisions SET related_memory_id = ? WHERE memory_id = ? AND action = 'confirm'`,
				[]any{f["x"].MemoryID, f["a"].MemoryID}
		},
	}
	triggers := []string{"memory_revisions_no_update", "memory_revisions_no_delete", "memories_no_delete",
		"memories_insert_guard", "memories_update_guard"}

	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			store := newTestStore(t)
			fixture := buildVerifiedProject(t, store)
			mustVerify(t, store, "proj_a")

			// A local user editing the file can drop the guards first.
			for _, trigger := range triggers {
				if _, err := store.db.Exec(`DROP TRIGGER ` + trigger); err != nil {
					t.Fatalf("drop %s: %v", trigger, err)
				}
			}
			statement, args := tamper(fixture)
			result, err := store.db.Exec(statement, args...)
			if err != nil {
				t.Fatalf("tamper: %v", err)
			}
			if affected, _ := result.RowsAffected(); affected == 0 {
				t.Fatal("tamper statement changed nothing")
			}
			if err := store.Verify(context.Background(), "proj_a"); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("Verify() error = %v, want ErrIntegrity", err)
			}
			mustVerify(t, store, "proj_b")
		})
	}
}

func TestVerifyUsesSnapshotDuringConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	writer := openTestStore(t, path)
	verifier := openTestStore(t, path)
	ctx := context.Background()

	done := make(chan struct{})
	writeErr := make(chan error, 1)
	go func() {
		defer close(done)
		for i := range 40 {
			value, err := writer.CreateCandidate(ctx, candidateRequest("proj_a", P3, fmt.Sprintf("claim %d", i)))
			if err == nil {
				value, err = writer.ActivateAtRevision(ctx, "proj_a", value.MemoryID, value.Revision, "ok")
			}
			if err == nil {
				value, err = writer.ConfirmAtRevision(ctx, "proj_a", value.MemoryID, value.Revision, []string{"event:x"})
			}
			if err == nil {
				_, err = writer.CorrectAtRevision(ctx, "proj_a", value.MemoryID, value.Revision, correctRequest("fixed"))
			}
			if err != nil {
				writeErr <- err
				return
			}
		}
	}()

	verifications := 0
	for running := true; running; verifications++ {
		select {
		case <-done:
			running = false
		default:
		}
		if err := verifier.Verify(ctx, "proj_a"); err != nil {
			t.Fatalf("Verify() during writes error = %v", err)
		}
	}
	select {
	case err := <-writeErr:
		t.Fatalf("writer error = %v", err)
	default:
	}
	if verifications < 2 {
		t.Fatalf("only %d verifications overlapped the writer", verifications)
	}
	mustVerify(t, verifier, "proj_a")
}

// Checks below sit behind the revision chain, so they are exercised on
// in-memory histories rather than through tampered rows.
func TestVerifyHistoryInvariants(t *testing.T) {
	store := newTestStore(t)
	fixture := buildVerifiedProject(t, store)
	history, err := store.History(context.Background(), "proj_a", fixture["a"].MemoryID)
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	if err := verifyHistory(fixture["a"].MemoryID, history); err != nil {
		t.Fatalf("verifyHistory() of untouched history error = %v", err)
	}
	if history[2].Action != ActionConfirm || len(history[2].Memory.EvidenceRefs) != 2 {
		t.Fatalf("test setup: revision 3 = %+v", history[2])
	}

	dropped := slices.Clone(history)
	dropped[2].Memory.EvidenceRefs = []string{"event:a2"}
	if dropped[2].Memory.ContentHash, err = dropped[2].Memory.computeContentHash(); err != nil {
		t.Fatal(err)
	}
	if err := verifyHistory(fixture["a"].MemoryID, dropped); !errors.Is(err, ErrIntegrity) || !strings.Contains(err.Error(), "dropped evidence") {
		t.Fatalf("verifyHistory() with dropped evidence error = %v", err)
	}

	skewed := slices.Clone(history)
	skewed[1].RecordedAt = skewed[1].RecordedAt.Add(time.Second)
	if err := verifyHistory(fixture["a"].MemoryID, skewed); !errors.Is(err, ErrIntegrity) || !strings.Contains(err.Error(), "recorded_at") {
		t.Fatalf("verifyHistory() with skewed recorded_at error = %v", err)
	}
}

func TestChangesSinceReturnsOnlyCorrections(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	before := mustActive(t, store, candidateRequest("proj_a", P1, "changed before"))
	if _, err := store.Retract(ctx, "proj_a", before.MemoryID, "old news"); err != nil {
		t.Fatal(err)
	}
	since := store.now()
	a := mustActive(t, store, candidateRequest("proj_a", P1, "a"))
	b := mustActive(t, store, candidateRequest("proj_a", P1, "b"))
	if _, err := store.Confirm(ctx, "proj_a", a.MemoryID, []string{"event:2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Correct(ctx, "proj_a", a.MemoryID, correctRequest("a fixed")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkStale(ctx, "proj_a", b.MemoryID, "inputs changed"); err != nil {
		t.Fatal(err)
	}
	other := mustActive(t, store, candidateRequest("proj_b", P1, "other"))
	if _, err := store.Retract(ctx, "proj_b", other.MemoryID, "other project"); err != nil {
		t.Fatal(err)
	}

	changes, err := store.ChangesSince(ctx, "proj_a", since, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, revision := range changes {
		got = append(got, revision.Memory.Claim+":"+string(revision.Action))
	}
	if strings.Join(got, ",") != "a:supersede,b:mark_stale" {
		t.Fatalf("ChangesSince() = %v", got)
	}
	if limited, err := store.ChangesSince(ctx, "proj_a", since, 1); err != nil || len(limited) != 1 {
		t.Fatalf("limited ChangesSince() = %d, %v", len(limited), err)
	}
	if _, err := store.ChangesSince(ctx, "proj_a", since, 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ChangesSince(limit 0) error = %v", err)
	}
}
