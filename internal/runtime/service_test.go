package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/protocol"
)

const validActionJSON = `{
  "schema_version": "acr.action/v1alpha1",
  "agent": {"kind": "codex", "native_session_id": "session-1"},
  "workspace": {"cwd": "/workspace/project", "project_id": "project-1"},
  "tool": {"name": "write_file", "input": {"path": "generated/schema.go"}},
  "operation_kind": "tool.call"
}`

func TestDecodeActionRejectsUnknownField(t *testing.T) {
	input := strings.Replace(validActionJSON, `"operation_kind": "tool.call"`, `"operation_kind": "tool.call", "unknown": true`, 1)
	_, err := DecodeActionBytes([]byte(input))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("DecodeActionBytes() error = %v, want unknown field", err)
	}
}

func TestDecodeActionRejectsMissingSession(t *testing.T) {
	input := strings.Replace(validActionJSON, `"native_session_id": "session-1"`, `"native_session_id": ""`, 1)
	_, err := DecodeActionBytes([]byte(input))
	if err == nil || !strings.Contains(err.Error(), "native_session_id") {
		t.Fatalf("DecodeActionBytes() error = %v, want session error", err)
	}
}

func TestAuditedEvaluationRecordsProposalAndDecision(t *testing.T) {
	store, err := eventstore.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("eventstore.Open() error = %v", err)
	}
	defer store.Close()
	service, err := NewWithEventStore(nil, store)
	if err != nil {
		t.Fatalf("NewWithEventStore() error = %v", err)
	}
	service.now = func() time.Time { return time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC) }
	action, err := DecodeActionBytes([]byte(validActionJSON))
	if err != nil {
		t.Fatalf("DecodeActionBytes() error = %v", err)
	}
	action.Tool.CallID = "call-1"
	action.Agent.NativeTurnID = "turn-1"

	decision, err := service.EvaluateContext(context.Background(), action)
	if err != nil {
		t.Fatalf("EvaluateContext() error = %v", err)
	}
	if decision.Kind != protocol.DecisionPass {
		t.Fatalf("decision.Kind = %s, want pass", decision.Kind)
	}
	events, err := store.List(context.Background(), "project-1", 0, 10)
	if err != nil {
		t.Fatalf("store.List() error = %v", err)
	}
	if len(events) != 2 || events[0].Type != "action.proposed" || events[1].Type != "action.passed" {
		t.Fatalf("events = %#v", events)
	}
	if events[1].Causality.CausationEventID != events[0].EventID || events[1].Causality.CorrelationID != "call-1" {
		t.Fatalf("event causality was not preserved: %#v", events[1].Causality)
	}
}

func TestAuditedEvaluationRedactsSecretsBeforePersistence(t *testing.T) {
	store, err := eventstore.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("eventstore.Open() error = %v", err)
	}
	defer store.Close()
	service, err := NewWithEventStore(nil, store)
	if err != nil {
		t.Fatalf("NewWithEventStore() error = %v", err)
	}
	action, err := DecodeActionBytes([]byte(strings.Replace(validActionJSON, `{"path": "generated/schema.go"}`, `{"path":"generated/schema.go","api_token":"secret-token-value"}`, 1)))
	if err != nil {
		t.Fatalf("DecodeActionBytes() error = %v", err)
	}
	if _, err := service.Evaluate(action); err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	events, err := store.List(context.Background(), "project-1", 0, 10)
	if err != nil {
		t.Fatalf("store.List() error = %v", err)
	}
	if strings.Contains(string(events[0].Payload), "secret-token-value") || !strings.Contains(string(events[0].Payload), "[REDACTED]") {
		t.Fatalf("persisted payload was not redacted: %s", events[0].Payload)
	}
}

func TestRecordToolOutcomeAppendsCompletedAndFailedEvents(t *testing.T) {
	store, err := eventstore.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("eventstore.Open() error = %v", err)
	}
	defer store.Close()
	service, err := NewWithEventStore(nil, store)
	if err != nil {
		t.Fatalf("NewWithEventStore() error = %v", err)
	}
	action, err := DecodeActionBytes([]byte(validActionJSON))
	if err != nil {
		t.Fatalf("DecodeActionBytes() error = %v", err)
	}
	action.Tool.CallID = "call-1"
	if err := service.RecordToolOutcome(context.Background(), ToolOutcome{Action: action, Response: []byte(`{"ok":true}`)}); err != nil {
		t.Fatalf("RecordToolOutcome(completed) error = %v", err)
	}
	action.Tool.CallID = "call-2"
	if err := service.RecordToolOutcome(context.Background(), ToolOutcome{Action: action, Failed: true, Error: "exit code 1"}); err != nil {
		t.Fatalf("RecordToolOutcome(failed) error = %v", err)
	}
	events, err := store.List(context.Background(), "project-1", 0, 10)
	if err != nil {
		t.Fatalf("store.List() error = %v", err)
	}
	if len(events) != 2 || events[0].Type != "action.completed" || events[1].Type != "action.failed" {
		t.Fatalf("events = %#v", events)
	}
}
