package contract

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func LoadPath(path string) ([]Contract, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect contract path %q: %w", path, err)
	}
	if !info.IsDir() {
		value, err := loadFile(path)
		if err != nil {
			return nil, err
		}
		return []Contract{value}, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read contract directory %q: %w", path, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	values := make([]Contract, 0, len(names))
	for _, name := range names {
		value, err := loadFile(filepath.Join(path, name))
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func loadFile(path string) (Contract, error) {
	file, err := os.Open(path)
	if err != nil {
		return Contract{}, fmt.Errorf("open contract %q: %w", path, err)
	}
	defer file.Close()

	value, err := Decode(file)
	if err != nil {
		return Contract{}, fmt.Errorf("load contract %q: %w", path, err)
	}
	return value, nil
}
