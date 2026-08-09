package project

import (
	"os"
	"testing"
)

func TestInitializeCreatesIsolatedProjectLayout(t *testing.T) {
	root := t.TempDir()
	config, err := NewConfig(root, "example")
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	paths, err := Initialize(root, config)
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	loaded, loadedPaths, err := Load(root)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded != config || loadedPaths != paths {
		t.Fatalf("loaded = %#v, paths = %#v", loaded, loadedPaths)
	}
	info, err := os.Stat(paths.Config)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
	}
}

func TestInitializeDoesNotOverwriteExistingProject(t *testing.T) {
	root := t.TempDir()
	config, err := NewConfig(root, "example")
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	if _, err := Initialize(root, config); err != nil {
		t.Fatalf("first Initialize() error = %v", err)
	}
	if _, err := Initialize(root, config); err == nil {
		t.Fatal("second Initialize() succeeded")
	}
}
