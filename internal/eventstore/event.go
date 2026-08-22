// Package eventstore persists ACR events in an append-only SQLite log where
// every project has its own strictly increasing sequence and SHA-256 hash
// chain.
package eventstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/agent-contract-runtime/acr/internal/protocol"
)

const SchemaVersion = "acr.event/v1alpha1"

var (
	// ErrProjectNotFound reports that a project has no events yet.
	ErrProjectNotFound = errors.New("project not found")
	// ErrIntegrity reports that stored events do not form a valid hash chain.
	ErrIntegrity = errors.New("event chain integrity violation")
)

type Actor struct {
	Kind            string             `json:"kind"`
	AgentKind       protocol.AgentKind `json:"agent_kind,omitempty"`
	NativeSessionID string             `json:"native_session_id,omitempty"`
	ACRSessionID    string             `json:"acr_session_id,omitempty"`
}

type Causality struct {
	CorrelationID    string `json:"correlation_id,omitempty"`
	CausationEventID string `json:"causation_event_id,omitempty"`
	NativeTurnID     string `json:"native_turn_id,omitempty"`
	NativeToolUseID  string `json:"native_tool_use_id,omitempty"`
}

// AppendRequest carries the caller-owned fields of a new event. The store
// assigns schema_version, sequence, recorded_at and all hashes. EventID is
// generated when empty.
type AppendRequest struct {
	EventID      string
	ProjectID    string
	WorkspaceID  string
	OccurredAt   time.Time
	Type         string
	Actor        Actor
	Causality    Causality
	Payload      json.RawMessage
	EvidenceRefs []string
}

type Event struct {
	SchemaVersion string          `json:"schema_version"`
	EventID       string          `json:"event_id"`
	ProjectID     string          `json:"project_id"`
	WorkspaceID   string          `json:"workspace_id"`
	Sequence      uint64          `json:"sequence"`
	OccurredAt    time.Time       `json:"occurred_at"`
	RecordedAt    time.Time       `json:"recorded_at"`
	Type          string          `json:"type"`
	Actor         Actor           `json:"actor"`
	Causality     Causality       `json:"causality"`
	Payload       json.RawMessage `json:"payload"`
	EvidenceRefs  []string        `json:"evidence_refs"`
	PreviousHash  string          `json:"previous_hash"`
	PayloadHash   string          `json:"payload_hash"`
	EventHash     string          `json:"event_hash"`
}

type ProjectHead struct {
	ProjectID string    `json:"project_id"`
	Sequence  uint64    `json:"sequence"`
	EventHash string    `json:"event_hash"`
	UpdatedAt time.Time `json:"updated_at"`
}

// record mirrors one events row exactly as stored. Hashes are always computed
// from these stored texts so verification does not depend on Go struct layout.
type record struct {
	SchemaVersion string
	EventID       string
	ProjectID     string
	WorkspaceID   string
	Sequence      uint64
	OccurredAt    string
	RecordedAt    string
	Type          string
	Actor         string
	Causality     string
	Payload       string
	EvidenceRefs  string
	PreviousHash  string
	PayloadHash   string
	EventHash     string
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func hashHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// canonicalJSON re-encodes one JSON value with object keys sorted, no
// insignificant whitespace, no HTML escaping and number literals preserved.
// The result is stable for equal input values and idempotent.
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

func canonicalMarshal(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode JSON: %w", err)
	}
	canonical, err := canonicalJSON(encoded)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}

// computeEventHash returns
// SHA-256(canonical_event_without_integrity || "\n" || previous_hash || "\n" || payload_hash).
func (row record) computeEventHash() (string, error) {
	body := struct {
		SchemaVersion string          `json:"schema_version"`
		EventID       string          `json:"event_id"`
		ProjectID     string          `json:"project_id"`
		WorkspaceID   string          `json:"workspace_id"`
		Sequence      uint64          `json:"sequence"`
		OccurredAt    string          `json:"occurred_at"`
		RecordedAt    string          `json:"recorded_at"`
		Type          string          `json:"type"`
		Actor         json.RawMessage `json:"actor"`
		Causality     json.RawMessage `json:"causality"`
		Payload       json.RawMessage `json:"payload"`
		EvidenceRefs  json.RawMessage `json:"evidence_refs"`
	}{
		SchemaVersion: row.SchemaVersion,
		EventID:       row.EventID,
		ProjectID:     row.ProjectID,
		WorkspaceID:   row.WorkspaceID,
		Sequence:      row.Sequence,
		OccurredAt:    row.OccurredAt,
		RecordedAt:    row.RecordedAt,
		Type:          row.Type,
		Actor:         json.RawMessage(row.Actor),
		Causality:     json.RawMessage(row.Causality),
		Payload:       json.RawMessage(row.Payload),
		EvidenceRefs:  json.RawMessage(row.EvidenceRefs),
	}
	canonical, err := canonicalMarshal(body)
	if err != nil {
		return "", fmt.Errorf("canonicalize event %q: %w", row.EventID, err)
	}

	hash := sha256.New()
	hash.Write([]byte(canonical))
	hash.Write([]byte("\n"))
	hash.Write([]byte(row.PreviousHash))
	hash.Write([]byte("\n"))
	hash.Write([]byte(row.PayloadHash))
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (row record) event() (Event, error) {
	value := Event{
		SchemaVersion: row.SchemaVersion,
		EventID:       row.EventID,
		ProjectID:     row.ProjectID,
		WorkspaceID:   row.WorkspaceID,
		Sequence:      row.Sequence,
		Type:          row.Type,
		Payload:       json.RawMessage(row.Payload),
		PreviousHash:  row.PreviousHash,
		PayloadHash:   row.PayloadHash,
		EventHash:     row.EventHash,
	}

	var err error
	if value.OccurredAt, err = time.Parse(time.RFC3339Nano, row.OccurredAt); err != nil {
		return Event{}, fmt.Errorf("decode event %q occurred_at: %w", row.EventID, err)
	}
	if value.RecordedAt, err = time.Parse(time.RFC3339Nano, row.RecordedAt); err != nil {
		return Event{}, fmt.Errorf("decode event %q recorded_at: %w", row.EventID, err)
	}
	if err := json.Unmarshal([]byte(row.Actor), &value.Actor); err != nil {
		return Event{}, fmt.Errorf("decode event %q actor: %w", row.EventID, err)
	}
	if err := json.Unmarshal([]byte(row.Causality), &value.Causality); err != nil {
		return Event{}, fmt.Errorf("decode event %q causality: %w", row.EventID, err)
	}
	if err := json.Unmarshal([]byte(row.EvidenceRefs), &value.EvidenceRefs); err != nil {
		return Event{}, fmt.Errorf("decode event %q evidence_refs: %w", row.EventID, err)
	}
	return value, nil
}
