package bootstrap

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDiscoverIndexesKnownSourcesDeterministically(t *testing.T) {
	root := t.TempDir()
	write(t, root, "AGENTS.md", "# Rules\nDo the safe thing.\n")
	write(t, root, "CLAUDE.md", "# Claude\n")
	write(t, root, "services/api/AGENTS.override.md", "# API rules\n")
	write(t, root, ".claude/rules/go.md", "# Go\n")
	write(t, root, "docs/adr/0001-store.md", "# Store\n")
	write(t, root, "README.md", "# Project\n")
	write(t, root, ".acr/contracts/guard.json", validContract)

	first, err := Discover(root, Options{})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	second, err := Discover(root, Options{})
	if err != nil {
		t.Fatalf("Discover() second error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("reports differ:\n%#v\n%#v", first, second)
	}
	if first.Indexed != 7 || first.Skipped != 0 || first.Invalid != 0 {
		t.Fatalf("counts = indexed %d skipped %d invalid %d", first.Indexed, first.Skipped, first.Invalid)
	}
	paths := make([]string, 0, len(first.Sources))
	for _, source := range first.Sources {
		paths = append(paths, source.Path)
		if source.Status == "indexed" && (source.ID == "" || source.SHA256 == "") {
			t.Fatalf("indexed source lacks identity: %#v", source)
		}
	}
	if !reflect.DeepEqual(paths, []string{".acr/contracts/guard.json", ".claude/rules/go.md", "AGENTS.md", "CLAUDE.md", "README.md", "docs/adr/0001-store.md", "services/api/AGENTS.override.md"}) {
		t.Fatalf("paths = %#v", paths)
	}
	if first.Sources[0].ContractID != "guard" || first.Sources[0].ContractMode != "draft" {
		t.Fatalf("contract metadata = %#v", first.Sources[0])
	}
}

func TestDiscoverSkipsUnsafeContentAndReportsInvalidContract(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "README.bin", "text\x00binary")
	write(t, root, "SECURITY.md", strings.Repeat("x", 33))
	write(t, root, ".acr/contracts/bad.json", `{}`)

	report, err := Discover(root, Options{MaximumFileBytes: 32})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if report.Skipped != 3 || report.Invalid != 1 || report.Indexed != 0 {
		t.Fatalf("counts = indexed %d skipped %d invalid %d, sources %#v", report.Indexed, report.Skipped, report.Invalid, report.Sources)
	}
	for _, source := range report.Sources {
		if source.Path == "AGENTS.md" && source.Reason != "symbolic links are not followed" {
			t.Fatalf("symlink reason = %q", source.Reason)
		}
	}
}

func TestDiscoverHonorsTotalAndSourceLimits(t *testing.T) {
	root := t.TempDir()
	write(t, root, "README.md", "12345")
	write(t, root, "SECURITY.md", "67890")

	report, err := Discover(root, Options{MaximumTotalBytes: 5})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if report.Indexed != 1 || report.Skipped != 1 || report.TotalBytes != 5 {
		t.Fatalf("report = %#v", report)
	}
	if _, err := Discover(root, Options{MaximumSources: 1}); err == nil || !strings.Contains(err.Error(), "exceeding limit") {
		t.Fatalf("source limit error = %v", err)
	}
}

func write(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

const validContract = `{
  "apiVersion":"acr.dev/v1alpha1",
  "kind":"Contract",
  "metadata":{"id":"guard","version":1,"title":"guard","status":"draft","severity":"high","priority":900},
  "scope":{},
  "applicability":{"all":[{"field":"tool.name","operator":"equals","value":"shell"}]},
  "behavior":{"instruction":"guard","enforcement":"hard"},
  "correction":{"mode":"deny"},
  "failure_mode":"closed"
}`
