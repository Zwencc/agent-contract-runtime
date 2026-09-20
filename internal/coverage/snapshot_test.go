package coverage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanCompareAndRoundTrip(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "src/a.go", "before")
	writeFile(t, root, "delete.txt", "delete")
	writeFile(t, root, ".git/ignored", "ignored")
	writeFile(t, root, "node_modules/ignored.js", "ignored")
	previous, err := Scan(root, 4, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(previous.Files) != 2 {
		t.Fatalf("tracked files = %#v", previous.Files)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("after"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "added.txt", "added")
	current, err := Scan(root, 9, Options{})
	if err != nil {
		t.Fatal(err)
	}
	changes := Compare(previous, current)
	if len(changes) != 3 || changes[0].Path != "added.txt" || changes[1].Kind != "deleted" || changes[2].Kind != "modified" {
		t.Fatalf("changes = %#v", changes)
	}
	path := filepath.Join(root, ".acr", "state", "workspace-snapshot.json")
	if err := Write(path, current); err != nil {
		t.Fatal(err)
	}
	loaded, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(Compare(current, loaded)) != 0 || loaded.EventSequence != 9 {
		t.Fatalf("loaded = %#v", loaded)
	}
}

func TestScanSkipsSymlinksAndOversizedFiles(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "large", "12345")
	snapshot, err := Scan(root, 0, Options{MaximumFileBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Files) != 0 || snapshot.SkippedFiles != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func writeFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
