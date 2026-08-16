package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPathLoadsJSONFilesInNameOrder(t *testing.T) {
	directory := t.TempDir()
	second := strings.Replace(validContractJSON, `"id": "rewrite-generated-path"`, `"id": "contract-b"`, 1)
	first := strings.Replace(validContractJSON, `"id": "rewrite-generated-path"`, `"id": "contract-a"`, 1)
	if err := os.WriteFile(filepath.Join(directory, "20-b.json"), []byte(second), 0o600); err != nil {
		t.Fatalf("write second contract: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "10-a.json"), []byte(first), 0o600); err != nil {
		t.Fatalf("write first contract: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "README.md"), []byte("ignored"), 0o600); err != nil {
		t.Fatalf("write ignored file: %v", err)
	}

	values, err := LoadPath(directory)
	if err != nil {
		t.Fatalf("LoadPath() error = %v", err)
	}
	if len(values) != 2 || values[0].Metadata.ID != "contract-a" || values[1].Metadata.ID != "contract-b" {
		t.Fatalf("LoadPath() returned unexpected order: %#v", values)
	}
}

func TestLoadPathAllowsEmptyContractDirectory(t *testing.T) {
	values, err := LoadPath(t.TempDir())
	if err != nil {
		t.Fatalf("LoadPath() error = %v", err)
	}
	if len(values) != 0 {
		t.Fatalf("LoadPath() = %#v", values)
	}
}
