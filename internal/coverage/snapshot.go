// Package coverage detects workspace changes that have no corresponding ACR
// tool-outcome evidence. It does not attempt to reconstruct missing actions.
package coverage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const SchemaVersion = "acr.workspace-snapshot/v1alpha1"

type Options struct {
	MaximumFiles     int
	MaximumFileBytes int64
	MaximumTotalBytes int64
	// Include, when set, limits the scan to files whose slash-separated
	// project-relative path it accepts; other files are neither read nor
	// counted.
	Include func(path string) bool
}

type File struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

type Snapshot struct {
	SchemaVersion string `json:"schema_version"`
	Root          string `json:"root"`
	EventSequence uint64 `json:"event_sequence"`
	Files         []File `json:"files"`
	SkippedFiles  int    `json:"skipped_files"`
	TotalBytes    int64  `json:"total_bytes"`
}

type Change struct {
	Path         string `json:"path"`
	Kind         string `json:"kind"`
	BeforeSHA256 string `json:"before_sha256,omitempty"`
	AfterSHA256  string `json:"after_sha256,omitempty"`
}

func Scan(root string, eventSequence uint64, options Options) (Snapshot, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Snapshot{}, fmt.Errorf("resolve workspace root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Snapshot{}, fmt.Errorf("resolve workspace root %q: %w", root, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect workspace root %q: %w", root, err)
	}
	if !info.IsDir() {
		return Snapshot{}, fmt.Errorf("workspace root %q is not a directory", root)
	}
	options = defaults(options)
	snapshot := Snapshot{SchemaVersion: SchemaVersion, Root: root, EventSequence: eventSequence, Files: []File{}}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			snapshot.SkippedFiles++
			return nil
		}
		if entry.IsDir() {
			if ignoredDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			snapshot.SkippedFiles++
			return nil
		}
		if options.Include != nil {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if !options.Include(filepath.ToSlash(relative)) {
				return nil
			}
		}
		if len(snapshot.Files) >= options.MaximumFiles {
			return fmt.Errorf("workspace contains more than %d tracked files", options.MaximumFiles)
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if fileInfo.Size() > options.MaximumFileBytes || snapshot.TotalBytes+fileInfo.Size() > options.MaximumTotalBytes {
			snapshot.SkippedFiles++
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		snapshot.Files = append(snapshot.Files, File{Path: filepath.ToSlash(relative), SHA256: hex.EncodeToString(digest[:]), SizeBytes: int64(len(data))})
		snapshot.TotalBytes += int64(len(data))
		return nil
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("scan workspace %q: %w", root, err)
	}
	sort.Slice(snapshot.Files, func(left, right int) bool { return snapshot.Files[left].Path < snapshot.Files[right].Path })
	return snapshot, nil
}

func Compare(previous, current Snapshot) []Change {
	before := make(map[string]File, len(previous.Files))
	after := make(map[string]File, len(current.Files))
	for _, file := range previous.Files {
		before[file.Path] = file
	}
	for _, file := range current.Files {
		after[file.Path] = file
	}
	changes := []Change{}
	for _, file := range current.Files {
		old, exists := before[file.Path]
		if !exists {
			changes = append(changes, Change{Path: file.Path, Kind: "added", AfterSHA256: file.SHA256})
		} else if old.SHA256 != file.SHA256 {
			changes = append(changes, Change{Path: file.Path, Kind: "modified", BeforeSHA256: old.SHA256, AfterSHA256: file.SHA256})
		}
	}
	for _, file := range previous.Files {
		if _, exists := after[file.Path]; !exists {
			changes = append(changes, Change{Path: file.Path, Kind: "deleted", BeforeSHA256: file.SHA256})
		}
	}
	sort.Slice(changes, func(left, right int) bool { return changes[left].Path < changes[right].Path })
	return changes
}

func Read(path string) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read workspace snapshot %q: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var snapshot Snapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode workspace snapshot %q: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Snapshot{}, fmt.Errorf("decode workspace snapshot %q: trailing data", path)
	}
	if snapshot.SchemaVersion != SchemaVersion {
		return Snapshot{}, fmt.Errorf("workspace snapshot schema %q is unsupported", snapshot.SchemaVersion)
	}
	return snapshot, nil
}

func Write(path string, snapshot Snapshot) error {
	if snapshot.SchemaVersion != SchemaVersion {
		return fmt.Errorf("workspace snapshot schema must be %q", SchemaVersion)
	}
	encoded, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode workspace snapshot: %w", err)
	}
	encoded = append(encoded, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create workspace snapshot directory %q: %w", directory, err)
	}
	temporary, err := os.CreateTemp(directory, ".workspace-snapshot-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary workspace snapshot: %w", err)
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
		return fmt.Errorf("protect temporary workspace snapshot: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		return fmt.Errorf("write temporary workspace snapshot: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary workspace snapshot: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary workspace snapshot: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("commit workspace snapshot %q: %w", path, err)
	}
	committed = true
	return nil
}

func defaults(options Options) Options {
	if options.MaximumFiles <= 0 {
		options.MaximumFiles = 10000
	}
	if options.MaximumFileBytes <= 0 {
		options.MaximumFileBytes = 8 << 20
	}
	if options.MaximumTotalBytes <= 0 {
		options.MaximumTotalBytes = 64 << 20
	}
	return options
}

func ignoredDirectory(name string) bool {
	switch name {
	case ".git", ".acr", ".codex", ".claude", ".venv", "node_modules", "vendor", "dist", "build", "target", "coverage", "__pycache__":
		return true
	default:
		return false
	}
}
