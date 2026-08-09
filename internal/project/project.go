// Package project manages the local identity and paths of an ACR project.
package project

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const SchemaVersion = "acr.project/v1alpha1"

type Config struct {
	SchemaVersion string `json:"schema_version"`
	ProjectID     string `json:"project_id"`
	WorkspaceID   string `json:"workspace_id"`
	Name          string `json:"name"`
}

type Paths struct {
	Root         string
	ACRDirectory string
	Config       string
	Contracts    string
	State        string
	EventDB      string
	MemoryDB     string
	Objects      string
	ProjectionDB string
	WorkspaceSnapshot string
}

func ResolvePaths(root string) (Paths, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve project root %q: %w", root, err)
	}
	absolute = filepath.Clean(absolute)
	acrDirectory := filepath.Join(absolute, ".acr")
	return Paths{
		Root:         absolute,
		ACRDirectory: acrDirectory,
		Config:       filepath.Join(acrDirectory, "project.json"),
		Contracts:    filepath.Join(acrDirectory, "contracts"),
		State:        filepath.Join(acrDirectory, "state"),
		EventDB:      filepath.Join(acrDirectory, "state", "events.db"),
		MemoryDB:     filepath.Join(acrDirectory, "state", "memories.db"),
		Objects:      filepath.Join(acrDirectory, "state", "objects"),
		ProjectionDB: filepath.Join(acrDirectory, "state", "projections.db"),
		WorkspaceSnapshot: filepath.Join(acrDirectory, "state", "workspace-snapshot.json"),
	}, nil
}

func NewConfig(root, name string) (Config, error) {
	paths, err := ResolvePaths(root)
	if err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(name) == "" {
		name = filepath.Base(paths.Root)
	}
	return Config{
		SchemaVersion: SchemaVersion,
		ProjectID:     "proj_" + rand.Text(),
		WorkspaceID:   "ws_" + rand.Text(),
		Name:          name,
	}, nil
}

func Initialize(root string, config Config) (Paths, error) {
	paths, err := ResolvePaths(root)
	if err != nil {
		return Paths{}, err
	}
	if err := config.Validate(); err != nil {
		return Paths{}, err
	}
	rootInfo, err := os.Stat(paths.Root)
	if err != nil {
		return Paths{}, fmt.Errorf("inspect project root %q: %w", paths.Root, err)
	}
	if !rootInfo.IsDir() {
		return Paths{}, fmt.Errorf("project root %q is not a directory", paths.Root)
	}
	if _, err := os.Lstat(paths.Config); err == nil {
		return Paths{}, fmt.Errorf("initialize project: %s already exists", paths.Config)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Paths{}, fmt.Errorf("inspect project config %q: %w", paths.Config, err)
	}
	if err := os.MkdirAll(paths.Contracts, 0o755); err != nil {
		return Paths{}, fmt.Errorf("create contract directory %q: %w", paths.Contracts, err)
	}
	if err := os.MkdirAll(paths.State, 0o700); err != nil {
		return Paths{}, fmt.Errorf("create state directory %q: %w", paths.State, err)
	}
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return Paths{}, fmt.Errorf("encode project config: %w", err)
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(paths.ACRDirectory, ".project-*.tmp")
	if err != nil {
		return Paths{}, fmt.Errorf("create temporary project config: %w", err)
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
		return Paths{}, fmt.Errorf("protect temporary project config: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		return Paths{}, fmt.Errorf("write temporary project config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return Paths{}, fmt.Errorf("sync temporary project config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return Paths{}, fmt.Errorf("close temporary project config: %w", err)
	}
	if err := os.Rename(temporaryPath, paths.Config); err != nil {
		return Paths{}, fmt.Errorf("commit project config: %w", err)
	}
	committed = true
	return paths, nil
}

func Load(root string) (Config, Paths, error) {
	paths, err := ResolvePaths(root)
	if err != nil {
		return Config{}, Paths{}, err
	}
	file, err := os.Open(paths.Config)
	if err != nil {
		return Config{}, Paths{}, fmt.Errorf("open project config %q: %w", paths.Config, err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, Paths{}, fmt.Errorf("decode project config %q: %w", paths.Config, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Config{}, Paths{}, fmt.Errorf("decode project config %q: trailing data", paths.Config)
	}
	if err := config.Validate(); err != nil {
		return Config{}, Paths{}, fmt.Errorf("validate project config %q: %w", paths.Config, err)
	}
	return config, paths, nil
}

func (config Config) Validate() error {
	if config.SchemaVersion != SchemaVersion {
		return fmt.Errorf("project schema_version must be %q, got %q", SchemaVersion, config.SchemaVersion)
	}
	if strings.TrimSpace(config.ProjectID) == "" {
		return errors.New("project project_id is required")
	}
	if strings.TrimSpace(config.WorkspaceID) == "" {
		return errors.New("project workspace_id is required")
	}
	if strings.TrimSpace(config.Name) == "" {
		return errors.New("project name is required")
	}
	return nil
}

func Encode(config Config) ([]byte, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(config); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
