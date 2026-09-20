// Package memory stores structured cross-agent memories with an append-only
// revision history and an explicit lifecycle state machine.
package memory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	// ErrNotFound reports that a memory does not exist in the given project.
	ErrNotFound = errors.New("memory not found")
	// ErrInvalidTransition reports a lifecycle change the current status forbids.
	ErrInvalidTransition = errors.New("invalid memory status transition")
	// ErrInvalidInput reports a rejected request field.
	ErrInvalidInput = errors.New("invalid memory input")
	// ErrRevisionConflict reports that a memory is no longer at the revision
	// the caller based its change on.
	ErrRevisionConflict = errors.New("memory revision conflict")
	// ErrIntegrity reports stored memories that do not match their history.
	ErrIntegrity = errors.New("memory integrity violation")
)

type Status string

const (
	StatusCandidate  Status = "candidate"
	StatusActive     Status = "active"
	StatusConfirmed  Status = "confirmed"
	StatusStale      Status = "stale"
	StatusContested  Status = "contested"
	StatusSuperseded Status = "superseded"
	StatusRetracted  Status = "retracted"
	StatusArchived   Status = "archived"
)

// Priority orders retrieval: P0 is the most important, P6 the least.
type Priority int

const (
	P0 Priority = iota // safety and human hard constraints
	P1                 // current task constraints, unfinished work, blockers
	P2                 // verified architecture decisions
	P3                 // recent failure patterns and verification evidence
	P4                 // stable module knowledge
	P5                 // ordinary work history
	P6                 // low-value temporary information
)

func (priority Priority) Valid() bool {
	return priority >= P0 && priority <= P6
}

func (priority Priority) String() string {
	return fmt.Sprintf("P%d", int(priority))
}

// Action names the lifecycle operation recorded on a revision.
type Action string

const (
	ActionCreate    Action = "create"
	ActionCorrect   Action = "correct"
	ActionSupersede Action = "supersede"
	ActionActivate  Action = "activate"
	ActionConfirm   Action = "confirm"
	ActionMarkStale Action = "mark_stale"
	ActionContest   Action = "contest"
	ActionRetract   Action = "retract"
	ActionArchive   Action = "archive"
	ActionExpire    Action = "expire"
)

type transitionRule struct {
	target Status
	from   []Status
}

// transitions is the complete lifecycle table for existing memories. create
// and correct are not listed because they start a new memory.
var transitions = map[Action]transitionRule{
	ActionActivate:  {StatusActive, []Status{StatusCandidate, StatusStale, StatusContested}},
	ActionConfirm:   {StatusConfirmed, []Status{StatusActive, StatusConfirmed, StatusStale}},
	ActionMarkStale: {StatusStale, []Status{StatusActive, StatusConfirmed}},
	ActionContest:   {StatusContested, []Status{StatusActive, StatusConfirmed, StatusStale}},
	ActionSupersede: {StatusSuperseded, []Status{StatusActive, StatusConfirmed, StatusStale, StatusContested}},
	ActionRetract:   {StatusRetracted, []Status{StatusCandidate, StatusActive, StatusConfirmed, StatusStale, StatusContested}},
	ActionArchive:   {StatusArchived, []Status{StatusCandidate, StatusActive, StatusConfirmed, StatusStale, StatusContested, StatusSuperseded}},
	ActionExpire:    {StatusArchived, []Status{StatusCandidate, StatusActive, StatusConfirmed, StatusStale, StatusContested, StatusSuperseded}},
}

func (rule transitionRule) allows(status Status) bool {
	for _, from := range rule.from {
		if from == status {
			return true
		}
	}
	return false
}

type Memory struct {
	MemoryID           string          `json:"memory_id"`
	ProjectID          string          `json:"project_id"`
	WorkspaceID        string          `json:"workspace_id,omitempty"`
	Revision           uint64          `json:"revision"`
	Status             Status          `json:"status"`
	Priority           Priority        `json:"priority"`
	Kind               string          `json:"kind"`
	Claim              string          `json:"claim"`
	Scope              json.RawMessage `json:"scope"`
	EvidenceRefs       []string        `json:"evidence_refs"`
	Source             string          `json:"source"`
	Confidence         float64         `json:"confidence"`
	ValidFrom          time.Time       `json:"valid_from"`
	ValidUntil         time.Time       `json:"valid_until,omitzero"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	SupersedesMemoryID string          `json:"supersedes_memory_id,omitempty"`
	ContentHash        string          `json:"content_hash"`
}

// Revision is one immutable history entry holding the full memory snapshot
// after the change, plus why it happened.
type Revision struct {
	Memory             Memory    `json:"memory"`
	Action             Action    `json:"action"`
	Reason             string    `json:"reason,omitempty"`
	ChangeEvidenceRefs []string  `json:"change_evidence_refs"`
	RelatedMemoryID    string    `json:"related_memory_id,omitempty"`
	RecordedAt         time.Time `json:"recorded_at"`
	// ProjectSequence orders every revision of a project; PreviousHash and
	// RevisionHash link those revisions into one SHA-256 chain.
	ProjectSequence uint64 `json:"project_sequence"`
	PreviousHash    string `json:"previous_hash"`
	RevisionHash    string `json:"revision_hash"`
}

// CreateRequest describes a new candidate. ValidFrom defaults to the creation
// time; a zero ValidUntil means no expiry.
type CreateRequest struct {
	ProjectID    string
	WorkspaceID  string
	Kind         string
	Priority     Priority
	Claim        string
	Scope        json.RawMessage
	EvidenceRefs []string
	Source       string
	Confidence   float64
	ValidFrom    time.Time
	ValidUntil   time.Time
}

// CorrectRequest describes the replacement for a wrong memory. Kind, priority
// and workspace are inherited; a nil Scope inherits the old scope.
type CorrectRequest struct {
	Claim        string
	Scope        json.RawMessage
	EvidenceRefs []string
	Source       string
	Confidence   float64
	ValidUntil   time.Time
	Reason       string
}

const (
	MaximumRetrieveLimit = 200
	maximumKinds         = 32
	maximumClaimBytes    = 16 << 10
	maximumReasonBytes   = 4 << 10
	maximumSourceBytes   = 256
	maximumEvidenceRefs  = 256
	maximumEvidenceBytes = 512
)

type Query struct {
	ProjectID string
	// WorkspaceID, when set, returns memories of that workspace plus
	// project-wide memories without a workspace.
	WorkspaceID     string
	Kinds           []string
	MaximumPriority *Priority
	Limit           int
	// AsOf defaults to the current time.
	AsOf time.Time
	// IncludeContested additionally returns contested memories; for audit
	// tools only.
	IncludeContested bool
}

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	kindPattern       = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), ErrInvalidInput)
}

func validateIdentifier(name, value string) error {
	if !identifierPattern.MatchString(value) {
		return invalid("%s %q must match %s", name, value, identifierPattern)
	}
	return nil
}

func validateKind(kind string) error {
	if !kindPattern.MatchString(kind) {
		return invalid("kind %q must match %s", kind, kindPattern)
	}
	return nil
}

func normalizeClaim(claim string) (string, error) {
	claim = strings.TrimSpace(claim)
	if claim == "" {
		return "", invalid("claim is required")
	}
	if len(claim) > maximumClaimBytes {
		return "", invalid("claim exceeds %d bytes", maximumClaimBytes)
	}
	return claim, nil
}

func normalizeReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", invalid("reason is required")
	}
	if len(reason) > maximumReasonBytes {
		return "", invalid("reason exceeds %d bytes", maximumReasonBytes)
	}
	return reason, nil
}

func normalizeSource(source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", invalid("source is required")
	}
	if len(source) > maximumSourceBytes {
		return "", invalid("source exceeds %d bytes", maximumSourceBytes)
	}
	return source, nil
}

func validateConfidence(confidence float64) error {
	if math.IsNaN(confidence) || confidence < 0 || confidence > 1 {
		return invalid("confidence %v must be within [0, 1]", confidence)
	}
	return nil
}

// normalizeEvidence trims, de-duplicates and sorts references so the same
// evidence set always hashes identically.
func normalizeEvidence(refs []string) ([]string, error) {
	if len(refs) > maximumEvidenceRefs {
		return nil, invalid("evidence_refs exceeds %d entries", maximumEvidenceRefs)
	}
	seen := map[string]bool{}
	normalized := []string{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return nil, invalid("evidence_refs must not contain empty references")
		}
		if len(ref) > maximumEvidenceBytes {
			return nil, invalid("evidence reference exceeds %d bytes", maximumEvidenceBytes)
		}
		if !seen[ref] {
			seen[ref] = true
			normalized = append(normalized, ref)
		}
	}
	sort.Strings(normalized)
	return normalized, nil
}

func mergeEvidence(current, added []string) ([]string, error) {
	return normalizeEvidence(append(append([]string{}, current...), added...))
}

// normalizeScope returns the canonical encoding of a JSON object.
func normalizeScope(scope json.RawMessage) (json.RawMessage, error) {
	canonical, err := canonicalJSON(scope)
	if err != nil {
		return nil, fmt.Errorf("scope: %w: %w", ErrInvalidInput, err)
	}
	if len(canonical) == 0 || canonical[0] != '{' {
		return nil, invalid("scope must be a JSON object")
	}
	return canonical, nil
}

// canonicalJSON re-encodes one JSON value with object keys sorted, no
// insignificant whitespace, no HTML escaping and number literals preserved.
func canonicalJSON(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode JSON: multiple JSON values")
		}
		return nil, fmt.Errorf("decode JSON trailing data: %w", err)
	}

	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("encode JSON: %w", err)
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

// timeLayout is fixed-width UTC so stored timestamps compare correctly as text.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

func formatTime(value time.Time) string {
	return value.UTC().Format(timeLayout)
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(timeLayout, value)
}

// computeContentHash covers every normalized field that changes what the
// memory means or where it applies. Lifecycle status, revision and
// bookkeeping timestamps are excluded.
func (memory Memory) computeContentHash() (string, error) {
	validUntil := ""
	if !memory.ValidUntil.IsZero() {
		validUntil = formatTime(memory.ValidUntil)
	}
	body := struct {
		ProjectID          string          `json:"project_id"`
		WorkspaceID        string          `json:"workspace_id"`
		Kind               string          `json:"kind"`
		Priority           Priority        `json:"priority"`
		Claim              string          `json:"claim"`
		Scope              json.RawMessage `json:"scope"`
		EvidenceRefs       []string        `json:"evidence_refs"`
		Source             string          `json:"source"`
		Confidence         float64         `json:"confidence"`
		ValidFrom          string          `json:"valid_from"`
		ValidUntil         string          `json:"valid_until"`
		SupersedesMemoryID string          `json:"supersedes_memory_id"`
	}{
		ProjectID:          memory.ProjectID,
		WorkspaceID:        memory.WorkspaceID,
		Kind:               memory.Kind,
		Priority:           memory.Priority,
		Claim:              memory.Claim,
		Scope:              memory.Scope,
		EvidenceRefs:       memory.EvidenceRefs,
		Source:             memory.Source,
		Confidence:         memory.Confidence,
		ValidFrom:          formatTime(memory.ValidFrom),
		ValidUntil:         validUntil,
		SupersedesMemoryID: memory.SupersedesMemoryID,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("encode memory %q content: %w", memory.MemoryID, err)
	}
	canonical, err := canonicalJSON(encoded)
	if err != nil {
		return "", fmt.Errorf("canonicalize memory %q content: %w", memory.MemoryID, err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func emptyIfNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// computeRevisionHash covers every stored column of a revision except
// revision_hash itself, including project_sequence and previous_hash, so
// rewriting, reordering or dropping any revision breaks the project chain.
func (revision Revision) computeRevisionHash() (string, error) {
	value := revision.Memory
	validUntil := ""
	if !value.ValidUntil.IsZero() {
		validUntil = formatTime(value.ValidUntil)
	}
	body := struct {
		ProjectID          string          `json:"project_id"`
		MemoryID           string          `json:"memory_id"`
		Revision           uint64          `json:"revision"`
		WorkspaceID        string          `json:"workspace_id"`
		Status             Status          `json:"status"`
		Priority           Priority        `json:"priority"`
		Kind               string          `json:"kind"`
		Claim              string          `json:"claim"`
		Scope              json.RawMessage `json:"scope"`
		EvidenceRefs       []string        `json:"evidence_refs"`
		Source             string          `json:"source"`
		Confidence         float64         `json:"confidence"`
		ValidFrom          string          `json:"valid_from"`
		ValidUntil         string          `json:"valid_until"`
		CreatedAt          string          `json:"created_at"`
		UpdatedAt          string          `json:"updated_at"`
		SupersedesMemoryID string          `json:"supersedes_memory_id"`
		ContentHash        string          `json:"content_hash"`
		Action             Action          `json:"action"`
		Reason             string          `json:"reason"`
		ChangeEvidenceRefs []string        `json:"change_evidence_refs"`
		RelatedMemoryID    string          `json:"related_memory_id"`
		RecordedAt         string          `json:"recorded_at"`
		ProjectSequence    uint64          `json:"project_sequence"`
		PreviousHash       string          `json:"previous_hash"`
	}{
		ProjectID:          value.ProjectID,
		MemoryID:           value.MemoryID,
		Revision:           value.Revision,
		WorkspaceID:        value.WorkspaceID,
		Status:             value.Status,
		Priority:           value.Priority,
		Kind:               value.Kind,
		Claim:              value.Claim,
		Scope:              value.Scope,
		EvidenceRefs:       emptyIfNil(value.EvidenceRefs),
		Source:             value.Source,
		Confidence:         value.Confidence,
		ValidFrom:          formatTime(value.ValidFrom),
		ValidUntil:         validUntil,
		CreatedAt:          formatTime(value.CreatedAt),
		UpdatedAt:          formatTime(value.UpdatedAt),
		SupersedesMemoryID: value.SupersedesMemoryID,
		ContentHash:        value.ContentHash,
		Action:             revision.Action,
		Reason:             revision.Reason,
		ChangeEvidenceRefs: emptyIfNil(revision.ChangeEvidenceRefs),
		RelatedMemoryID:    revision.RelatedMemoryID,
		RecordedAt:         formatTime(revision.RecordedAt),
		ProjectSequence:    revision.ProjectSequence,
		PreviousHash:       revision.PreviousHash,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("encode memory %q revision %d: %w", value.MemoryID, value.Revision, err)
	}
	canonical, err := canonicalJSON(encoded)
	if err != nil {
		return "", fmt.Errorf("canonicalize memory %q revision %d: %w", value.MemoryID, value.Revision, err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
