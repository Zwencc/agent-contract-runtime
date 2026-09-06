package hookconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAtomicCreatesConfigAndBacksUpExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".codex", "hooks.json")
	if backup, err := WriteAtomic(path, []byte("first\n")); err != nil || backup != "" {
		t.Fatalf("first WriteAtomic() backup = %q, error = %v", backup, err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	backup, err := WriteAtomic(path, []byte("second\n"))
	if err != nil {
		t.Fatalf("second WriteAtomic() error = %v", err)
	}
	if !strings.HasPrefix(backup, path+".acr-backup-") {
		t.Fatalf("backup = %q", backup)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "second\n" || string(previous) != "first\n" {
		t.Fatalf("current = %q, previous = %q", current, previous)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestWriteAtomicRefusesSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "hooks.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteAtomic(path, []byte("replace")); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("WriteAtomic() error = %v", err)
	}
}
