package verification

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-contract-runtime/acr/internal/coverage"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
)

func snapshot(files map[string]string) coverage.Snapshot {
	value := coverage.Snapshot{}
	for _, path := range []string{"README.md", "go.mod", "internal/a/a.go", "internal/a/a_test.go", "internal/b/b.go", "docs/x.md"} {
		if digest, ok := files[path]; ok {
			value.Files = append(value.Files, coverage.File{Path: path, SHA256: digest})
		}
	}
	return value
}

var baseFiles = map[string]string{
	"README.md": "r1", "go.mod": "m1", "internal/a/a.go": "a1", "internal/a/a_test.go": "t1", "internal/b/b.go": "b1", "docs/x.md": "d1",
}

func with(changes map[string]string) map[string]string {
	result := map[string]string{}
	for key, value := range baseFiles {
		result[key] = value
	}
	for key, value := range changes {
		result[key] = value
	}
	return result
}

func TestMatches(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"go.mod", "go.mod", true},
		{"go.mod", "sub/go.mod", false},
		{"internal/**", "internal/a/a.go", true},
		{"internal/**", "internal", true},
		{"internal/*", "internal/a/a.go", false},
		{"**/*.go", "internal/a/a.go", true},
		{"**/*.go", "main.go", true},
		{"internal/**/*_test.go", "internal/a/a_test.go", true},
		{"internal/**/*_test.go", "internal/a/a.go", false},
		{"docs/*.md", "docs/x.md", true},
	}
	for _, test := range cases {
		if got := Matches(test.pattern, test.name); got != test.want {
			t.Fatalf("Matches(%q, %q) = %v, want %v", test.pattern, test.name, got, test.want)
		}
	}
}

func TestValidatePatterns(t *testing.T) {
	if err := ValidatePatterns([]string{"internal/**", "go.mod", "**/*.go"}); err != nil {
		t.Fatalf("ValidatePatterns() error = %v", err)
	}
	for _, bad := range []string{"", "/etc/passwd", "../outside", "a/../b", "a//b", "./a", `a\b`, "[bad"} {
		if err := ValidatePatterns([]string{bad}); err == nil {
			t.Fatalf("ValidatePatterns(%q) error = nil", bad)
		}
	}
}

func TestComputeSelectsOnlyMatchingFiles(t *testing.T) {
	scoped := []string{"internal/a/**", "go.mod"}
	base := Compute(snapshot(baseFiles), scoped)
	if base.FileCount != 3 {
		t.Fatalf("FileCount = %d, want 3", base.FileCount)
	}
	if again := Compute(snapshot(baseFiles), []string{"go.mod", "internal/a/**", "go.mod"}); again.Digest != base.Digest {
		t.Fatal("pattern order or duplicates changed the fingerprint")
	}
	if unrelated := Compute(snapshot(with(map[string]string{"docs/x.md": "d2"})), scoped); unrelated.Digest != base.Digest {
		t.Fatal("a file outside the inputs changed the fingerprint")
	}
	if related := Compute(snapshot(with(map[string]string{"internal/a/a.go": "a2"})), scoped); related.Digest == base.Digest {
		t.Fatal("an input change kept the fingerprint")
	}
	removed := with(nil)
	delete(removed, "go.mod")
	if gone := Compute(snapshot(removed), scoped); gone.Digest == base.Digest {
		t.Fatal("a deleted input kept the fingerprint")
	}
	whole := Compute(snapshot(baseFiles), nil)
	if whole.FileCount != len(baseFiles) {
		t.Fatalf("whole-workspace FileCount = %d", whole.FileCount)
	}
	if changed := Compute(snapshot(with(map[string]string{"docs/x.md": "d2"})), nil); changed.Digest == whole.Digest {
		t.Fatal("whole-workspace fingerprint ignored a change")
	}
}

func openStore(t *testing.T) *eventstore.Store {
	t.Helper()
	store, err := eventstore.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func record(t *testing.T, store *eventstore.Store, subject string, passed, stable bool, files map[string]string, patterns []string) eventstore.Event {
	t.Helper()
	event, err := Record(context.Background(), store, "project-1", "", passed, Result{
		Subject: subject, Argv: []string{"go", "test"}, Inputs: Compute(snapshot(files), patterns), Stable: stable,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func statusOf(t *testing.T, store *eventstore.Store) map[string]Status {
	t.Helper()
	statuses, err := Statuses(context.Background(), store, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]Status{}
	for _, status := range statuses {
		result[status.Subject] = status
	}
	return result
}

func TestReconcileInvalidatesOnlyChangedInputs(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	a := record(t, store, "test-a", true, true, baseFiles, []string{"internal/a/**"})
	record(t, store, "test-b", true, true, baseFiles, []string{"internal/b/**"})
	record(t, store, "test-all", true, true, baseFiles, nil)
	record(t, store, "lint", false, true, baseFiles, nil)
	record(t, store, "flaky", true, false, baseFiles, nil)

	statuses := statusOf(t, store)
	if statuses["test-a"].State != StateValid || statuses["lint"].State != StateFailed || statuses["flaky"].State != StateUnstable {
		t.Fatalf("initial statuses = %+v", statuses)
	}

	changed := snapshot(with(map[string]string{"internal/a/a.go": "a2"}))
	count, err := Reconcile(ctx, store, "project-1", "", changed, time.Now())
	if err != nil || count != 2 {
		t.Fatalf("Reconcile() = %d, %v; want test-a and test-all invalidated", count, err)
	}
	statuses = statusOf(t, store)
	if statuses["test-a"].State != StateInvalidated || statuses["test-all"].State != StateInvalidated || statuses["test-b"].State != StateValid {
		t.Fatalf("statuses after change = %+v", statuses)
	}
	if statuses["test-a"].InvalidatedBy == "" || statuses["test-a"].EventID != a.EventID {
		t.Fatalf("invalidation not linked: %+v", statuses["test-a"])
	}

	if count, err := Reconcile(ctx, store, "project-1", "", changed, time.Now()); err != nil || count != 0 {
		t.Fatalf("second Reconcile() = %d, %v; want no duplicate invalidations", count, err)
	}

	// A fresh pass on the new inputs supersedes the invalidated result.
	record(t, store, "test-a", true, true, with(map[string]string{"internal/a/a.go": "a2"}), []string{"internal/a/**"})
	if state := statusOf(t, store)["test-a"].State; state != StateValid {
		t.Fatalf("re-verified state = %s, want valid", state)
	}
}

func TestStatusesOfUnknownProjectIsEmpty(t *testing.T) {
	statuses, err := Statuses(context.Background(), openStore(t), "missing")
	if err != nil || len(statuses) != 0 {
		t.Fatalf("Statuses() = %v, %v", statuses, err)
	}
}
