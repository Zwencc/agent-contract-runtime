// Package verification binds verification results to the digest of their
// declared inputs and invalidates them once those inputs change, so a test
// that passed on input digest X is never reused for digest Y.
package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/agent-contract-runtime/acr/internal/coverage"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/protocol"
	"github.com/agent-contract-runtime/acr/internal/redaction"
)

const (
	EventPassed      = "verification.passed"
	EventFailed      = "verification.failed"
	EventInvalidated = "projection.invalidated"

	maximumPatterns = 64
)

// Fingerprint identifies the input files a verification ran against.
type Fingerprint struct {
	// Patterns are slash-separated globs relative to the project root where
	// "**" matches any number of directories; empty means the whole
	// workspace.
	Patterns  []string `json:"patterns"`
	Digest    string   `json:"digest"`
	FileCount int      `json:"file_count"`
	// SkippedFiles counts workspace files outside scan limits, whose changes
	// cannot invalidate this verification.
	SkippedFiles int `json:"skipped_files"`
}

// Result is the payload of verification.passed and verification.failed.
type Result struct {
	Subject       string      `json:"subject"`
	Argv          []string    `json:"argv,omitempty"`
	CWD           string      `json:"cwd,omitempty"`
	ExitCode      int         `json:"exit_code"`
	SourceEventID string      `json:"source_event_id,omitempty"`
	Inputs        Fingerprint `json:"inputs"`
	// Stable is false when the inputs changed while the check was running,
	// so the result describes no single input state.
	Stable bool `json:"stable"`
}

// Invalidation is the payload of projection.invalidated for a verification.
type Invalidation struct {
	Kind                string `json:"kind"`
	Subject             string `json:"subject"`
	VerificationEventID string `json:"verification_event_id"`
	Reason              string `json:"reason"`
	PreviousDigest      string `json:"previous_digest"`
	CurrentDigest       string `json:"current_digest"`
}

type State string

const (
	StateValid       State = "valid"
	StateFailed      State = "failed"
	StateUnstable    State = "unstable"
	StateInvalidated State = "invalidated"
)

// Status is the latest verification of one subject.
type Status struct {
	Subject       string `json:"subject"`
	State         State  `json:"state"`
	EventID       string `json:"event_id"`
	Sequence      uint64 `json:"sequence"`
	Result        Result `json:"result"`
	InvalidatedBy string `json:"invalidated_by,omitempty"`
}

// ValidatePatterns rejects absolute, escaping or malformed input patterns.
func ValidatePatterns(patterns []string) error {
	if len(patterns) > maximumPatterns {
		return fmt.Errorf("verification inputs exceed %d patterns", maximumPatterns)
	}
	for _, pattern := range patterns {
		if strings.TrimSpace(pattern) == "" || strings.HasPrefix(pattern, "/") || strings.Contains(pattern, "\\") {
			return fmt.Errorf("verification input %q must be a relative slash-separated pattern", pattern)
		}
		for _, segment := range strings.Split(pattern, "/") {
			if segment == "" || segment == "." || segment == ".." {
				return fmt.Errorf("verification input %q has an empty, '.' or '..' segment", pattern)
			}
			if _, err := path.Match(segment, ""); err != nil {
				return fmt.Errorf("verification input %q: %w", pattern, err)
			}
		}
	}
	return nil
}

func normalizePatterns(patterns []string) []string {
	normalized := append([]string{}, patterns...)
	sort.Strings(normalized)
	return compactStrings(normalized)
}

func compactStrings(values []string) []string {
	result := values[:0]
	for i, value := range values {
		if i == 0 || value != values[i-1] {
			result = append(result, value)
		}
	}
	return result
}

// Matches reports whether a slash-separated relative path matches pattern.
func Matches(pattern, name string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pattern, name []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			if len(pattern) == 1 {
				return true
			}
			for skip := 0; skip <= len(name); skip++ {
				if matchSegments(pattern[1:], name[skip:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, _ := path.Match(pattern[0], name[0]); !ok {
			return false
		}
		pattern, name = pattern[1:], name[1:]
	}
	return len(name) == 0
}

// Compute fingerprints the snapshot files selected by patterns.
func Compute(snapshot coverage.Snapshot, patterns []string) Fingerprint {
	patterns = normalizePatterns(patterns)
	hash := sha256.New()
	fingerprint := Fingerprint{Patterns: patterns, SkippedFiles: snapshot.SkippedFiles}
	for _, file := range snapshot.Files {
		selected := len(patterns) == 0
		for _, pattern := range patterns {
			if Matches(pattern, file.Path) {
				selected = true
				break
			}
		}
		if !selected {
			continue
		}
		fmt.Fprintf(hash, "%s\t%s\n", file.Path, file.SHA256)
		fingerprint.FileCount++
	}
	fingerprint.Digest = "sha256:" + hex.EncodeToString(hash.Sum(nil))
	return fingerprint
}

// FingerprintInputs scans only the files of root selected by patterns and returns
// the same digest Compute yields over a full workspace snapshot.
func FingerprintInputs(root string, patterns []string) (Fingerprint, error) {
	options := coverage.Options{}
	if len(patterns) > 0 {
		options.Include = func(name string) bool {
			for _, pattern := range patterns {
				if Matches(pattern, name) {
					return true
				}
			}
			return false
		}
	}
	snapshot, err := coverage.Scan(root, 0, options)
	if err != nil {
		return Fingerprint{}, err
	}
	return Compute(snapshot, patterns), nil
}

// Checker answers evidence requirements of contracts: a subject is satisfied
// only when its latest verification is valid and its inputs still have the
// recorded digest in the workspace at Root.
type Checker struct {
	Store *eventstore.Store
	Root  string

	statuses map[string]map[string]Status
}

func (checker *Checker) Satisfied(action protocol.Action, subject string) (bool, string, error) {
	projectID := action.Workspace.ProjectID
	if checker.statuses == nil {
		checker.statuses = map[string]map[string]Status{}
	}
	byProject, ok := checker.statuses[projectID]
	if !ok {
		statuses, err := Statuses(context.Background(), checker.Store, projectID)
		if err != nil {
			return false, "", err
		}
		byProject = map[string]Status{}
		for _, status := range statuses {
			byProject[status.Subject] = status
		}
		checker.statuses[projectID] = byProject
	}
	status, ok := byProject[subject]
	if !ok {
		return false, "missing", nil
	}
	if status.State != StateValid {
		return false, string(status.State), nil
	}
	current, err := FingerprintInputs(checker.Root, status.Result.Inputs.Patterns)
	if err != nil {
		return false, "", fmt.Errorf("fingerprint inputs of %q: %w", subject, err)
	}
	if current.Digest != status.Result.Inputs.Digest {
		return false, "stale because its inputs changed after it passed", nil
	}
	return true, "valid", nil
}

// Record appends verification.passed or verification.failed.
func Record(ctx context.Context, store *eventstore.Store, projectID, workspaceID string, passed bool, result Result, now time.Time) (eventstore.Event, error) {
	if strings.TrimSpace(result.Subject) == "" {
		return eventstore.Event{}, errors.New("record verification: subject is required")
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return eventstore.Event{}, fmt.Errorf("encode verification %q: %w", result.Subject, err)
	}
	if payload, err = redaction.JSON(payload); err != nil {
		return eventstore.Event{}, fmt.Errorf("redact verification %q: %w", result.Subject, err)
	}
	eventType := EventFailed
	if passed {
		eventType = EventPassed
	}
	event, err := store.Append(ctx, eventstore.AppendRequest{
		ProjectID:   projectID,
		WorkspaceID: workspaceID,
		OccurredAt:  now,
		Type:        eventType,
		Actor:       eventstore.Actor{Kind: "runtime"},
		Causality:   eventstore.Causality{CausationEventID: result.SourceEventID},
		Payload:     payload,
	})
	if err != nil {
		return eventstore.Event{}, fmt.Errorf("record %s for %q: %w", eventType, result.Subject, err)
	}
	return event, nil
}

// Statuses folds the project's verification events into the latest status
// of every subject, ordered by subject.
func Statuses(ctx context.Context, store *eventstore.Store, projectID string) ([]Status, error) {
	latest := map[string]*Status{}
	byEventID := map[string]*Status{}
	for after := uint64(0); ; {
		events, err := store.ListMatching(ctx, projectID, after, 500, eventstore.Filter{Types: []string{EventPassed, EventFailed, EventInvalidated}})
		if err != nil {
			return nil, fmt.Errorf("read verification events: %w", err)
		}
		if len(events) == 0 {
			break
		}
		for _, event := range events {
			after = event.Sequence
			switch event.Type {
			case EventPassed, EventFailed:
				var result Result
				if err := json.Unmarshal(event.Payload, &result); err != nil {
					return nil, fmt.Errorf("decode %s event %s: %w", event.Type, event.EventID, err)
				}
				state := StateFailed
				if event.Type == EventPassed {
					state = StateValid
					if !result.Stable {
						state = StateUnstable
					}
				}
				status := &Status{Subject: result.Subject, State: state, EventID: event.EventID, Sequence: event.Sequence, Result: result}
				latest[result.Subject] = status
				byEventID[event.EventID] = status
			case EventInvalidated:
				var invalidation Invalidation
				if err := json.Unmarshal(event.Payload, &invalidation); err != nil {
					return nil, fmt.Errorf("decode %s event %s: %w", event.Type, event.EventID, err)
				}
				if status, ok := byEventID[invalidation.VerificationEventID]; ok && status.State == StateValid {
					status.State = StateInvalidated
					status.InvalidatedBy = event.EventID
				}
			}
		}
	}
	statuses := make([]Status, 0, len(latest))
	for _, status := range latest {
		statuses = append(statuses, *status)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Subject < statuses[j].Subject })
	return statuses, nil
}

// Reconcile appends projection.invalidated for every valid verification whose
// inputs no longer match snapshot, and returns how many it invalidated.
func Reconcile(ctx context.Context, store *eventstore.Store, projectID, workspaceID string, snapshot coverage.Snapshot, now time.Time) (int, error) {
	statuses, err := Statuses(ctx, store, projectID)
	if err != nil {
		return 0, err
	}
	invalidated := 0
	for _, status := range statuses {
		if status.State != StateValid {
			continue
		}
		current := Compute(snapshot, status.Result.Inputs.Patterns)
		if current.Digest == status.Result.Inputs.Digest {
			continue
		}
		payload, err := json.Marshal(Invalidation{
			Kind:                "verification",
			Subject:             status.Subject,
			VerificationEventID: status.EventID,
			Reason:              "inputs_changed",
			PreviousDigest:      status.Result.Inputs.Digest,
			CurrentDigest:       current.Digest,
		})
		if err != nil {
			return invalidated, fmt.Errorf("encode invalidation of %q: %w", status.Subject, err)
		}
		if _, err := store.Append(ctx, eventstore.AppendRequest{
			ProjectID:   projectID,
			WorkspaceID: workspaceID,
			OccurredAt:  now,
			Type:        EventInvalidated,
			Actor:       eventstore.Actor{Kind: "runtime"},
			Causality:   eventstore.Causality{CausationEventID: status.EventID},
			Payload:     payload,
		}); err != nil {
			return invalidated, fmt.Errorf("record invalidation of %q: %w", status.Subject, err)
		}
		invalidated++
	}
	return invalidated, nil
}
