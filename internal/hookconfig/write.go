package hookconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func WriteAtomic(path string, data []byte) (string, error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create hook config directory %q: %w", directory, err)
	}
	mode := os.FileMode(0o600)
	backupPath := ""
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refuse to replace symbolic link %q", path)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("hook config %q is not a regular file", path)
		}
		mode = info.Mode().Perm()
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", fmt.Errorf("read hook config for backup %q: %w", path, readErr)
		}
		backupPath = path + ".acr-backup-" + time.Now().UTC().Format("20060102T150405.000000000Z")
		if writeErr := writeExclusive(backupPath, existing, mode); writeErr != nil {
			return "", fmt.Errorf("backup hook config %q: %w", path, writeErr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect hook config %q: %w", path, err)
	}

	if err := replaceFile(path, data, mode); err != nil {
		return backupPath, err
	}
	return backupPath, nil
}

func writeExclusive(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func replaceFile(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".hooks-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary hook config: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		temporary.Close()
		if !committed {
			os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("set temporary hook config permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write temporary hook config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary hook config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary hook config: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("commit hook config %q: %w", path, err)
	}
	committed = true
	return nil
}
