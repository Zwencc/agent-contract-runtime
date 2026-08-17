package evaluator

import (
	"path/filepath"
	"strings"

	"github.com/agent-contract-runtime/acr/internal/protocol"
	"github.com/agent-contract-runtime/acr/internal/verification"
)

// pathFields are the tool input fields that hold one file path.
var pathFields = []string{"file_path", "path", "notebook_path"}

var patchMarkers = []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "}

// projectPaths returns the action's file resources relative to the project
// root, slash-separated and cleaned. A path outside the project keeps its
// leading "../", so it never matches a project glob.
func projectPaths(action protocol.Action) []any {
	paths := []any{}
	for _, resource := range action.Resources {
		if relative, ok := projectRelative(action, resource); ok {
			paths = append(paths, relative)
		}
	}
	return paths
}

func projectRoot(action protocol.Action) string {
	if action.Workspace.Root != "" {
		return action.Workspace.Root
	}
	return action.Workspace.CWD
}

func projectRelative(action protocol.Action, value string) (string, bool) {
	if !filepath.IsAbs(value) {
		value = filepath.Join(action.Workspace.CWD, value)
	}
	relative, err := filepath.Rel(projectRoot(action), filepath.Clean(value))
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

// matchesAnyPath applies a glob or contains condition to every path.
func matchesAnyPath(paths []any, pattern string, glob bool) bool {
	for _, value := range paths {
		candidate := value.(string)
		if glob && verification.Matches(pattern, candidate) || !glob && strings.Contains(candidate, pattern) {
			return true
		}
	}
	return false
}

// rewriteProjectPath moves value from the project-relative prefix from to
// to, keeping it absolute or relative to the working directory as it was.
func rewriteProjectPath(action protocol.Action, value, from, to string) (string, bool) {
	relative, ok := projectRelative(action, value)
	if !ok {
		return value, false
	}
	replaced, ok := replacePathPrefix(relative, from, to)
	if !ok || strings.HasPrefix(relative, "../") {
		return value, false
	}
	target := filepath.Join(projectRoot(action), filepath.FromSlash(replaced))
	if filepath.IsAbs(value) {
		return target, true
	}
	if back, err := filepath.Rel(action.Workspace.CWD, target); err == nil {
		return filepath.ToSlash(back), true
	}
	return target, true
}

// rewriteToolPaths applies a paths replacement to the tool's path arguments
// and to the action's resources. It reports whether any argument changed.
func rewriteToolPaths(action protocol.Action, input map[string]any, from, to string) ([]string, bool) {
	changed := false
	for _, field := range pathFields {
		if value, ok := input[field].(string); ok {
			if replaced, ok := rewriteProjectPath(action, value, from, to); ok {
				input[field], changed = replaced, true
			}
		}
	}
	if command, ok := input["command"].(string); ok && action.Tool.Name == "apply_patch" {
		lines := strings.SplitAfter(command, "\n")
		for index, line := range lines {
			for _, marker := range patchMarkers {
				if !strings.HasPrefix(line, marker) {
					continue
				}
				body := strings.TrimPrefix(line, marker)
				trimmed := strings.TrimRight(body, "\r\n")
				if replaced, ok := rewriteProjectPath(action, strings.TrimSpace(trimmed), from, to); ok {
					lines[index], changed = marker+replaced+body[len(trimmed):], true
				}
				break
			}
		}
		input["command"] = strings.Join(lines, "")
	}
	resources := make([]string, 0, len(action.Resources))
	for _, resource := range action.Resources {
		replaced, _ := rewriteProjectPath(action, resource, from, to)
		resources = append(resources, replaced)
	}
	return resources, changed
}
