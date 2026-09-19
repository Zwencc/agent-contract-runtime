package bootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSnapshotCreatesPrivateValidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "bootstrap-sources.json")
	want := Report{Root: "/project", Sources: []Source{{Path: "AGENTS.md", Status: "indexed"}}, Indexed: 1}
	if err := WriteSnapshot(path, want); err != nil {
		t.Fatalf("WriteSnapshot() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Report
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if got.Root != want.Root || got.Indexed != 1 || len(got.Sources) != 1 {
		t.Fatalf("snapshot = %#v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode = %o", info.Mode().Perm())
	}
}

func TestCompareReportsAddedChangedAndRemovedInSourceOrder(t *testing.T) {
	previous := Report{Sources: []Source{
		{Path: "AGENTS.md", SHA256: "old", Status: "indexed"},
		{Path: "CLAUDE.md", SHA256: "same", Status: "indexed"},
		{Path: "removed.md", SHA256: "gone", Status: "indexed"},
	}}
	current := Report{Sources: []Source{
		{Path: "AGENTS.md", SHA256: "new", Status: "indexed"},
		{Path: "CLAUDE.md", SHA256: "same", Status: "indexed"},
		{Path: "added.md", SHA256: "added", Status: "indexed"},
	}}
	diff := Compare(previous, current)
	if len(diff.Added) != 1 || diff.Added[0].Path != "added.md" {
		t.Fatalf("added = %#v", diff.Added)
	}
	if len(diff.Changed) != 1 || diff.Changed[0].Path != "AGENTS.md" {
		t.Fatalf("changed = %#v", diff.Changed)
	}
	if len(diff.Removed) != 1 || diff.Removed[0].Path != "removed.md" {
		t.Fatalf("removed = %#v", diff.Removed)
	}
}

func TestReadSnapshotRejectsUnknownFieldsAndTrailingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, []byte(`{"root":"/project","sources":[],"indexed":0,"skipped":0,"invalid":0,"total_bytes":0,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(path); err == nil {
		t.Fatal("ReadSnapshot() accepted unknown field")
	}
	if err := os.WriteFile(path, []byte(`{"root":"/project","sources":[],"indexed":0,"skipped":0,"invalid":0,"total_bytes":0} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(path); err == nil {
		t.Fatal("ReadSnapshot() accepted trailing data")
	}
}
