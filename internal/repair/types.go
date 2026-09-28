// Package repair executes explicitly approved multi-step prerequisite plans
// while preserving enough evidence to avoid blind replay after a crash.
package repair

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/agent-contract-runtime/acr/internal/verification"
)

const SchemaVersion = "acr.repair-plan/v1alpha1"

var (
	ErrAlreadyCompleted = errors.New("repair already completed")
	ErrAlreadyFailed    = errors.New("repair already failed")
	ErrRecoveryRequired = errors.New("repair recovery required")
	ErrResourceLocked   = errors.New("repair resource locked")
)

type Plan struct {
	SchemaVersion  string          `json:"schema_version"`
	ID             string          `json:"id"`
	IdempotencyKey string          `json:"idempotency_key"`
	ResourceKeys   []string        `json:"resource_keys"`
	Steps          []Step          `json:"steps"`
	OriginalAction json.RawMessage `json:"original_action,omitempty"`
	// Repeatable lets a completed or failed plan run again under the same
	// idempotency key. Only plans made entirely of verification steps may
	// set it, because re-running checks has no side effects to repeat.
	Repeatable bool `json:"repeatable,omitempty"`
}

type Step struct {
	ID               string   `json:"id"`
	Argv             []string `json:"argv"`
	CWD              string   `json:"cwd,omitempty"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	AllowedExitCodes []int    `json:"allowed_exit_codes,omitempty"`
	// Verification marks the step as a check whose result is bound to the
	// digest of its inputs; it is recorded as verification.passed or
	// verification.failed.
	Verification *StepVerification `json:"verification,omitempty"`
	Compensation *Compensation     `json:"compensation,omitempty"`
}

// Compensation undoes a completed step when a later step fails. It runs only
// for steps that declare it.
type Compensation struct {
	Argv             []string `json:"argv"`
	CWD              string   `json:"cwd,omitempty"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	AllowedExitCodes []int    `json:"allowed_exit_codes,omitempty"`
}

type StepVerification struct {
	// Inputs are project-relative globs; empty means the whole workspace.
	Inputs []string `json:"inputs,omitempty"`
}

type Outcome struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	TimedOut bool   `json:"timed_out,omitempty"`
}

type Result struct {
	PlanID           string `json:"plan_id"`
	IdempotencyKey   string `json:"idempotency_key"`
	CompletedSteps   int    `json:"completed_steps"`
	TerminalEventID  string `json:"terminal_event_id"`
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func (plan *Plan) Validate() error {
	if plan.SchemaVersion != SchemaVersion {
		return fmt.Errorf("repair schema_version must be %q", SchemaVersion)
	}
	if !identifierPattern.MatchString(plan.ID) {
		return fmt.Errorf("repair id %q is invalid", plan.ID)
	}
	if !identifierPattern.MatchString(plan.IdempotencyKey) {
		return fmt.Errorf("repair idempotency_key %q is invalid", plan.IdempotencyKey)
	}
	if len(plan.ResourceKeys) == 0 {
		return errors.New("repair resource_keys must not be empty")
	}
	seenResources := map[string]bool{}
	for _, resource := range plan.ResourceKeys {
		if !identifierPattern.MatchString(resource) {
			return fmt.Errorf("repair resource key %q is invalid", resource)
		}
		if seenResources[resource] {
			return fmt.Errorf("repair resource key %q is duplicated", resource)
		}
		seenResources[resource] = true
	}
	sort.Strings(plan.ResourceKeys)
	if len(plan.Steps) == 0 {
		return errors.New("repair steps must not be empty")
	}
	seenSteps := map[string]bool{}
	for index := range plan.Steps {
		step := &plan.Steps[index]
		if !identifierPattern.MatchString(step.ID) {
			return fmt.Errorf("repair step id %q is invalid", step.ID)
		}
		if seenSteps[step.ID] {
			return fmt.Errorf("repair step id %q is duplicated", step.ID)
		}
		seenSteps[step.ID] = true
		if len(step.Argv) == 0 || strings.TrimSpace(step.Argv[0]) == "" {
			return fmt.Errorf("repair step %q argv must not be empty", step.ID)
		}
		for _, argument := range step.Argv {
			if strings.IndexByte(argument, 0) >= 0 {
				return fmt.Errorf("repair step %q argv contains NUL", step.ID)
			}
		}
		if step.TimeoutSeconds < 1 || step.TimeoutSeconds > 600 {
			return fmt.Errorf("repair step %q timeout_seconds must be 1 through 600", step.ID)
		}
		if filepath.IsAbs(step.CWD) {
			return fmt.Errorf("repair step %q cwd must be relative", step.ID)
		}
		clean := filepath.Clean(step.CWD)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("repair step %q cwd escapes the project root", step.ID)
		}
		if len(step.AllowedExitCodes) == 0 {
			step.AllowedExitCodes = []int{0}
		}
		seenCodes := map[int]bool{}
		for _, code := range step.AllowedExitCodes {
			if code < 0 || code > 255 || seenCodes[code] {
				return fmt.Errorf("repair step %q has invalid or duplicate allowed exit code %d", step.ID, code)
			}
			seenCodes[code] = true
		}
		sort.Ints(step.AllowedExitCodes)
		if step.Compensation != nil {
			if err := validateCommand(step.ID+" compensation", step.Compensation.Argv, step.Compensation.CWD, step.Compensation.TimeoutSeconds); err != nil {
				return err
			}
			if len(step.Compensation.AllowedExitCodes) == 0 {
				step.Compensation.AllowedExitCodes = []int{0}
			}
		}
		if step.Verification != nil {
			if err := verification.ValidatePatterns(step.Verification.Inputs); err != nil {
				return fmt.Errorf("repair step %q: %w", step.ID, err)
			}
		}
	}
	if plan.Repeatable {
		for _, step := range plan.Steps {
			if step.Verification == nil {
				return fmt.Errorf("repeatable repair plan step %q must be a verification step", step.ID)
			}
		}
	}
	if len(plan.OriginalAction) != 0 && !json.Valid(plan.OriginalAction) {
		return errors.New("repair original_action must be valid JSON")
	}
	return nil
}

func validateCommand(name string, argv []string, cwd string, timeout int) error {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return fmt.Errorf("repair step %s argv must not be empty", name)
	}
	for _, argument := range argv {
		if strings.IndexByte(argument, 0) >= 0 {
			return fmt.Errorf("repair step %s argv contains NUL", name)
		}
	}
	if timeout < 1 || timeout > 600 {
		return fmt.Errorf("repair step %s timeout_seconds must be 1 through 600", name)
	}
	clean := filepath.Clean(cwd)
	if filepath.IsAbs(cwd) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("repair step %s cwd must stay inside the project root", name)
	}
	return nil
}

func Decode(reader io.Reader) (Plan, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var plan Plan
	if err := decoder.Decode(&plan); err != nil {
		return Plan{}, fmt.Errorf("decode repair plan: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Plan{}, errors.New("decode repair plan: trailing data")
	}
	if err := plan.Validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (plan Plan) Hash() (string, error) {
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func allows(codes []int, actual int) bool {
	for _, code := range codes {
		if code == actual {
			return true
		}
	}
	return false
}
