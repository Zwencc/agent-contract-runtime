package bootstrap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type Change struct {
	Path         string `json:"path"`
	BeforeSHA256 string `json:"before_sha256,omitempty"`
	AfterSHA256  string `json:"after_sha256,omitempty"`
	BeforeStatus string `json:"before_status,omitempty"`
	AfterStatus  string `json:"after_status,omitempty"`
}

type Diff struct {
	Added   []Change `json:"added,omitempty"`
	Changed []Change `json:"changed,omitempty"`
	Removed []Change `json:"removed,omitempty"`
}

func (diff Diff) Empty() bool {
	return len(diff.Added) == 0 && len(diff.Changed) == 0 && len(diff.Removed) == 0
}

func Compare(previous, current Report) Diff {
	before := make(map[string]Source, len(previous.Sources))
	after := make(map[string]Source, len(current.Sources))
	for _, source := range previous.Sources {
		before[source.Path] = source
	}
	for _, source := range current.Sources {
		after[source.Path] = source
	}
	diff := Diff{}
	for _, source := range current.Sources {
		old, exists := before[source.Path]
		if !exists {
			diff.Added = append(diff.Added, Change{Path: source.Path, AfterSHA256: source.SHA256, AfterStatus: source.Status})
			continue
		}
		if old.SHA256 != source.SHA256 || old.Status != source.Status || old.Reason != source.Reason {
			diff.Changed = append(diff.Changed, Change{Path: source.Path, BeforeSHA256: old.SHA256, AfterSHA256: source.SHA256, BeforeStatus: old.Status, AfterStatus: source.Status})
		}
	}
	for _, source := range previous.Sources {
		if _, exists := after[source.Path]; !exists {
			diff.Removed = append(diff.Removed, Change{Path: source.Path, BeforeSHA256: source.SHA256, BeforeStatus: source.Status})
		}
	}
	return diff
}

func ReadSnapshot(path string) (Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Report{}, fmt.Errorf("read bootstrap snapshot %q: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var report Report
	if err := decoder.Decode(&report); err != nil {
		return Report{}, fmt.Errorf("decode bootstrap snapshot %q: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Report{}, fmt.Errorf("decode bootstrap snapshot %q: trailing data", path)
	}
	return report, nil
}

func WriteSnapshot(path string, report Report) error {
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode bootstrap snapshot: %w", err)
	}
	encoded = append(encoded, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create bootstrap snapshot directory %q: %w", directory, err)
	}
	temporary, err := os.CreateTemp(directory, ".bootstrap-sources-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary bootstrap snapshot: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		temporary.Close()
		if !committed {
			os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect temporary bootstrap snapshot: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		return fmt.Errorf("write temporary bootstrap snapshot: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary bootstrap snapshot: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary bootstrap snapshot: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("commit bootstrap snapshot %q: %w", path, err)
	}
	committed = true
	return nil
}
