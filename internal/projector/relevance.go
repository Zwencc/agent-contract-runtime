package projector

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/agent-contract-runtime/acr/internal/verification"
)

const MaximumRelevant = 50

// Failure is a projected failure pattern and why it is relevant.
type Failure struct {
	Node       Node              `json:"node"`
	Attributes FailureAttributes `json:"attributes"`
	// Match is "file" for a failure on a requested path or inside a
	// requested directory, "module" for one in the same directory as a
	// requested path, or "recent" when no paths were requested.
	Match string `json:"match"`
}

type Verification struct {
	Node       Node                   `json:"node"`
	Attributes VerificationAttributes `json:"attributes"`
}

type Relevant struct {
	Paths         []string       `json:"paths"`
	Failures      []Failure      `json:"failures"`
	Verifications []Verification `json:"verifications"`
}

// Relevant returns the state that bears on paths, path first: failures on
// those files, then failures in their directories, then verifications whose
// inputs cover them. Without paths it returns the most recent failures and
// every verification. Each list holds at most limit entries.
func (store *Store) Relevant(ctx context.Context, projectID string, paths []string, limit int) (Relevant, error) {
	if limit <= 0 || limit > MaximumRelevant {
		return Relevant{}, fmt.Errorf("relevant state limit must be within 1..%d", MaximumRelevant)
	}
	result := Relevant{Paths: cleanPaths(paths), Failures: []Failure{}, Verifications: []Verification{}}

	failureNodes, err := queryNodes(ctx, store.db, projectID, `type = ?`, NodeFailure)
	if err != nil {
		return Relevant{}, err
	}
	for _, node := range failureNodes {
		var attributes FailureAttributes
		if err := json.Unmarshal(node.Attributes, &attributes); err != nil {
			return Relevant{}, fmt.Errorf("decode failure %s: %w", node.ID, err)
		}
		match := failureMatch(result.Paths, attributes.Paths)
		if match != "" {
			result.Failures = append(result.Failures, Failure{Node: node, Attributes: attributes, Match: match})
		}
	}
	rank := map[string]int{"file": 0, "module": 1, "recent": 2}
	sort.SliceStable(result.Failures, func(i, j int) bool {
		left, right := result.Failures[i], result.Failures[j]
		if rank[left.Match] != rank[right.Match] {
			return rank[left.Match] < rank[right.Match]
		}
		if left.Attributes.LastSequence != right.Attributes.LastSequence {
			return left.Attributes.LastSequence > right.Attributes.LastSequence
		}
		return left.Node.ID < right.Node.ID
	})
	if len(result.Failures) > limit {
		result.Failures = result.Failures[:limit]
	}

	verificationNodes, err := queryNodes(ctx, store.db, projectID, `type = ?`, NodeVerification)
	if err != nil {
		return Relevant{}, err
	}
	for _, node := range verificationNodes {
		var attributes VerificationAttributes
		if err := json.Unmarshal(node.Attributes, &attributes); err != nil {
			return Relevant{}, fmt.Errorf("decode verification %s: %w", node.ID, err)
		}
		if covers(attributes.Patterns, result.Paths) {
			result.Verifications = append(result.Verifications, Verification{Node: node, Attributes: attributes})
		}
	}
	// Results that must not be relied on come first.
	sort.SliceStable(result.Verifications, func(i, j int) bool {
		leftValid := result.Verifications[i].Attributes.State == verification.StateValid
		rightValid := result.Verifications[j].Attributes.State == verification.StateValid
		if leftValid != rightValid {
			return !leftValid
		}
		return result.Verifications[i].Node.ID < result.Verifications[j].Node.ID
	})
	if len(result.Verifications) > limit {
		result.Verifications = result.Verifications[:limit]
	}
	return result, nil
}

func cleanPaths(paths []string) []string {
	cleaned := []string{}
	seen := map[string]bool{}
	for _, value := range paths {
		value = filepath.ToSlash(filepath.Clean(value))
		if value == "." || value == ".." || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "/") || seen[value] {
			continue
		}
		seen[value] = true
		cleaned = append(cleaned, value)
	}
	sort.Strings(cleaned)
	return cleaned
}

func failureMatch(requested, failed []string) string {
	if len(requested) == 0 {
		return "recent"
	}
	match := ""
	for _, want := range requested {
		for _, got := range failed {
			if got == want || strings.HasPrefix(got, want+"/") {
				return "file"
			}
			if path.Dir(got) == path.Dir(want) {
				match = "module"
			}
		}
	}
	return match
}

func covers(patterns, paths []string) bool {
	if len(patterns) == 0 || len(paths) == 0 {
		return true
	}
	for _, value := range paths {
		for _, pattern := range patterns {
			if verification.Matches(pattern, value) {
				return true
			}
		}
	}
	return false
}
