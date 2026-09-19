package bootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/agent-contract-runtime/acr/internal/contract"
)

const (
	defaultMaximumFileBytes  = int64(1 << 20)
	defaultMaximumTotalBytes = int64(16 << 20)
	defaultMaximumSources    = 1000
)

type Options struct {
	MaximumFileBytes  int64
	MaximumTotalBytes int64
	MaximumSources    int
}

type Source struct {
	ID           string   `json:"id,omitempty"`
	Path         string   `json:"path"`
	Kind         string   `json:"kind"`
	Authority    int      `json:"authority"`
	Agents       []string `json:"agents,omitempty"`
	Status       string   `json:"status"`
	Reason       string   `json:"reason,omitempty"`
	SHA256       string   `json:"sha256,omitempty"`
	SizeBytes    int64    `json:"size_bytes,omitempty"`
	LineCount    int      `json:"line_count,omitempty"`
	ContractID   string   `json:"contract_id,omitempty"`
	ContractMode string   `json:"contract_status,omitempty"`
}

type Report struct {
	Root       string   `json:"root"`
	Sources    []Source `json:"sources"`
	Indexed    int      `json:"indexed"`
	Skipped    int      `json:"skipped"`
	Invalid    int      `json:"invalid"`
	TotalBytes int64    `json:"total_bytes"`
}

type candidate struct {
	path      string
	kind      string
	authority int
	agents    []string
}

func Discover(root string, options Options) (Report, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Report{}, fmt.Errorf("resolve bootstrap root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Report{}, fmt.Errorf("resolve bootstrap root %q: %w", root, err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return Report{}, fmt.Errorf("inspect bootstrap root %q: %w", root, err)
	}
	if !info.IsDir() {
		return Report{}, fmt.Errorf("bootstrap root %q is not a directory", root)
	}
	options = withDefaults(options)

	candidates, err := discoverCandidates(root)
	if err != nil {
		return Report{}, err
	}
	if len(candidates) > options.MaximumSources {
		return Report{}, fmt.Errorf("bootstrap discovered %d sources, exceeding limit %d", len(candidates), options.MaximumSources)
	}

	report := Report{Root: root, Sources: make([]Source, 0, len(candidates))}
	for _, item := range candidates {
		source, content, err := inspect(root, item, options.MaximumFileBytes)
		if err != nil {
			return Report{}, err
		}
		if source.Status == "indexed" {
			if report.TotalBytes+source.SizeBytes > options.MaximumTotalBytes {
				source.Status = "skipped"
				source.Reason = "total byte limit exceeded"
				content = nil
			} else {
				report.TotalBytes += source.SizeBytes
			}
		}
		if source.Status == "indexed" && source.Kind == "structured_contract" {
			value, decodeErr := contract.DecodeBytes(content)
			if decodeErr != nil {
				source.Status = "invalid"
				source.Reason = decodeErr.Error()
			} else {
				source.ContractID = value.Metadata.ID
				source.ContractMode = string(value.Metadata.Status)
			}
		}
		switch source.Status {
		case "indexed":
			report.Indexed++
		case "skipped":
			report.Skipped++
		case "invalid":
			report.Invalid++
		}
		report.Sources = append(report.Sources, source)
	}
	return report, nil
}

func withDefaults(options Options) Options {
	if options.MaximumFileBytes <= 0 {
		options.MaximumFileBytes = defaultMaximumFileBytes
	}
	if options.MaximumTotalBytes <= 0 {
		options.MaximumTotalBytes = defaultMaximumTotalBytes
	}
	if options.MaximumSources <= 0 {
		options.MaximumSources = defaultMaximumSources
	}
	return options
}

func discoverCandidates(root string) ([]candidate, error) {
	items := map[string]candidate{}
	add := func(relative, kind string, authority int, agents ...string) {
		relative = filepath.ToSlash(filepath.Clean(relative))
		items[relative] = candidate{path: relative, kind: kind, authority: authority, agents: agents}
	}

	for _, name := range []string{"AGENTS.override.md", "AGENTS.md", "CLAUDE.local.md", "CLAUDE.md", filepath.Join(".claude", "CLAUDE.md")} {
		kind, authority, agents := classify(name)
		add(name, kind, authority, agents...)
	}

	rootEntries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read bootstrap root %q: %w", root, err)
	}
	for _, entry := range rootEntries {
		name := entry.Name()
		if entry.Type().IsRegular() && isProjectDocument(name) {
			add(name, "project_document", 40, "codex", "claude-code")
		}
	}
	if err := walkInstructionFiles(root, func(relative string) {
		kind, authority, agents := classify(filepath.Base(relative))
		add(relative, kind, authority, agents...)
	}); err != nil {
		return nil, err
	}

	for _, directory := range []struct {
		path      string
		kind      string
		authority int
		agents    []string
		extension string
	}{
		{filepath.Join(".claude", "rules"), "path_scoped_instruction", 75, []string{"claude-code"}, ".md"},
		{filepath.Join(".acr", "contracts"), "structured_contract", 100, []string{"codex", "claude-code"}, ".json"},
		{filepath.Join("docs", "adr"), "architecture_decision", 60, []string{"codex", "claude-code"}, ".md"},
		{"adr", "architecture_decision", 60, []string{"codex", "claude-code"}, ".md"},
		{"decisions", "architecture_decision", 60, []string{"codex", "claude-code"}, ".md"},
	} {
		if err := walkCandidateDirectory(root, directory.path, directory.extension, func(relative string) {
			add(relative, directory.kind, directory.authority, directory.agents...)
		}); err != nil {
			return nil, err
		}
	}

	result := make([]candidate, 0, len(items))
	for _, item := range items {
		absolute := filepath.Join(root, filepath.FromSlash(item.path))
		if _, err := os.Lstat(absolute); err == nil {
			result = append(result, item)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect bootstrap source %q: %w", absolute, err)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].path < result[right].path })
	return result, nil
}

func walkInstructionFiles(root string, add func(string)) error {
	const maximumDirectories = 10000
	directories := 0
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			directories++
			if directories > maximumDirectories {
				return fmt.Errorf("bootstrap directory traversal exceeds limit %d", maximumDirectories)
			}
			name := entry.Name()
			if shouldSkipDirectory(name, relative) {
				return filepath.SkipDir
			}
			if strings.Count(filepath.ToSlash(relative), "/") > 32 {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		switch entry.Name() {
		case "AGENTS.md", "AGENTS.override.md", "CLAUDE.md", "CLAUDE.local.md":
			add(relative)
		}
		return nil
	})
}

func shouldSkipDirectory(name, relative string) bool {
	if relative == ".claude" {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "vendor", "dist", "build", "target", "__pycache__":
		return true
	default:
		return false
	}
}

func walkCandidateDirectory(root, relative, extension string, add func(string)) error {
	absolute := filepath.Join(root, relative)
	info, err := os.Lstat(absolute)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect bootstrap directory %q: %w", absolute, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil
	}
	return filepath.WalkDir(absolute, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == absolute {
			return nil
		}
		relativePath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if strings.Count(filepath.ToSlash(relativePath), "/") > 8 {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() && strings.EqualFold(filepath.Ext(entry.Name()), extension) {
			add(relativePath)
		}
		return nil
	})
}

func inspect(root string, item candidate, maximumBytes int64) (Source, []byte, error) {
	source := Source{Path: item.path, Kind: item.kind, Authority: item.authority, Agents: item.agents}
	absolute := filepath.Join(root, filepath.FromSlash(item.path))
	info, err := os.Lstat(absolute)
	if err != nil {
		return Source{}, nil, fmt.Errorf("inspect bootstrap source %q: %w", absolute, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		source.Status = "skipped"
		source.Reason = "symbolic links are not followed"
		return source, nil, nil
	}
	if !info.Mode().IsRegular() {
		source.Status = "skipped"
		source.Reason = "not a regular file"
		return source, nil, nil
	}
	if isACRGenerated(item.path) {
		source.Status = "skipped"
		source.Reason = "ACR-generated source is not re-imported"
		return source, nil, nil
	}
	if info.Size() > maximumBytes {
		source.Status = "skipped"
		source.Reason = fmt.Sprintf("file exceeds %d byte limit", maximumBytes)
		return source, nil, nil
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return Source{}, nil, fmt.Errorf("read bootstrap source %q: %w", absolute, err)
	}
	if strings.IndexByte(string(data), 0) >= 0 || !utf8.Valid(data) {
		source.Status = "skipped"
		source.Reason = "binary or invalid UTF-8 content"
		return source, nil, nil
	}
	digest := sha256.Sum256(data)
	source.SHA256 = hex.EncodeToString(digest[:])
	idDigest := sha256.Sum256([]byte(item.path + "\n" + source.SHA256))
	source.ID = "src_" + hex.EncodeToString(idDigest[:12])
	source.SizeBytes = int64(len(data))
	source.LineCount = lineCount(data)
	source.Status = "indexed"
	return source, data, nil
}

func classify(path string) (string, int, []string) {
	path = filepath.ToSlash(path)
	switch filepath.Base(path) {
	case "AGENTS.override.md":
		return "repository_instruction", 90, []string{"codex"}
	case "AGENTS.md":
		return "repository_instruction", 80, []string{"codex"}
	case "CLAUDE.local.md":
		return "user_local_instruction", 85, []string{"claude-code"}
	case "CLAUDE.md", ".claude/CLAUDE.md":
		return "repository_instruction", 80, []string{"claude-code"}
	default:
		return "project_document", 40, []string{"codex", "claude-code"}
	}
}

func isProjectDocument(name string) bool {
	upper := strings.ToUpper(name)
	for _, prefix := range []string{"README", "CONTRIBUTING", "SECURITY", "ARCHITECTURE", "ROADMAP"} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return upper == "PLANS.MD" || upper == "GOALS.MD"
}

func isACRGenerated(path string) bool {
	path = strings.ToUpper(filepath.ToSlash(path))
	return path == ".ACR/ACR_CONTEXT.MD" || strings.HasPrefix(path, ".ACR/EXPORT/")
}

func lineCount(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	count := 1
	for _, value := range data {
		if value == '\n' {
			count++
		}
	}
	if data[len(data)-1] == '\n' {
		count--
	}
	return count
}
