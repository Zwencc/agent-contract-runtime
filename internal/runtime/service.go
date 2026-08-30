// Package runtime orchestrates contract evaluation for normalized ACR actions.
package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/evaluator"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/objectstore"
	"github.com/agent-contract-runtime/acr/internal/protocol"
	"github.com/agent-contract-runtime/acr/internal/redaction"
)

type Service struct {
	engine      *evaluator.Engine
	store       *eventstore.Store
	objects     *objectstore.Store
	inlineLimit int
	now         func() time.Time
}

// DefaultInlineOutputBytes is the largest redacted tool response or error
// kept inside an event row once an object store is configured.
const DefaultInlineOutputBytes = 16 << 10

// UseObjectStore moves redacted tool outputs larger than inlineLimit bytes
// out of event rows into objects; inlineLimit <= 0 selects the default.
func (service *Service) UseObjectStore(objects *objectstore.Store, inlineLimit int) {
	if inlineLimit <= 0 {
		inlineLimit = DefaultInlineOutputBytes
	}
	service.objects = objects
	service.inlineLimit = inlineLimit
}

type ToolOutcome struct {
	Action       protocol.Action
	Response     json.RawMessage
	Failed       bool
	Error        string
	IsInterrupt  bool
	DurationMS   int64
	// ObservedChanges lists project-relative paths ACR saw change while it
	// ran the call itself; it is empty for calls the host executed.
	ObservedChanges []string
}

func New(contracts []contract.Contract) (*Service, error) {
	engine, err := evaluator.New(contracts)
	if err != nil {
		return nil, err
	}
	return &Service{engine: engine, now: time.Now}, nil
}

func NewWithEventStore(contracts []contract.Contract, store *eventstore.Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("event store is required")
	}
	service, err := New(contracts)
	if err != nil {
		return nil, err
	}
	service.store = store
	return service, nil
}

func NewFromPath(path string) (*Service, error) {
	contracts, err := contract.LoadPath(path)
	if err != nil {
		return nil, err
	}
	return New(contracts)
}

func NewFromPathWithEventStore(path string, store *eventstore.Store) (*Service, error) {
	contracts, err := contract.LoadPath(path)
	if err != nil {
		return nil, err
	}
	return NewWithEventStore(contracts, store)
}

// UseAuthorizer lets the user lift contracts that allow it for one turn.
func (service *Service) UseAuthorizer(authorizer evaluator.Authorizer) {
	service.engine = service.engine.WithAuthorizer(authorizer)
}

// UseEvidence lets contracts with evidence verifiers consult checker.
func (service *Service) UseEvidence(checker evaluator.EvidenceChecker) {
	service.engine = service.engine.WithEvidence(checker)
}

func (service *Service) Evaluate(action protocol.Action) (protocol.Decision, error) {
	return service.EvaluateContext(context.Background(), action)
}

func (service *Service) EvaluateContext(ctx context.Context, action protocol.Action) (protocol.Decision, error) {
	if err := validateAction(action); err != nil {
		return protocol.Decision{}, err
	}
	if service.store == nil {
		return service.engine.Evaluate(action)
	}
	if strings.TrimSpace(action.Workspace.ProjectID) == "" {
		return protocol.Decision{}, errors.New("audited evaluation requires action workspace.project_id")
	}

	correlationID := action.Tool.CallID
	if correlationID == "" {
		correlationID = action.Agent.NativeSessionID + ":" + action.Agent.NativeTurnID + ":" + action.Tool.Name
	}
	proposedPayload, err := json.Marshal(action)
	if err != nil {
		return protocol.Decision{}, fmt.Errorf("encode action.proposed payload: %w", err)
	}
	proposedPayload, err = redaction.JSON(proposedPayload)
	if err != nil {
		return protocol.Decision{}, fmt.Errorf("redact action.proposed payload: %w", err)
	}
	// Both events are written in one transaction after the evaluation, which
	// costs one disk flush instead of two; the proposal keeps its own time.
	proposed := eventstore.AppendRequest{
		EventID:     rand.Text(),
		ProjectID:   action.Workspace.ProjectID,
		WorkspaceID: action.Workspace.WorkspaceID,
		OccurredAt:  service.now(),
		Type:        "action.proposed",
		Actor: eventstore.Actor{
			Kind:            "agent",
			AgentKind:       action.Agent.Kind,
			NativeSessionID: action.Agent.NativeSessionID,
			ACRSessionID:    action.Agent.ACRSessionID,
		},
		Causality: eventstore.Causality{
			CorrelationID:   correlationID,
			NativeTurnID:    action.Agent.NativeTurnID,
			NativeToolUseID: action.Tool.CallID,
		},
		Payload: proposedPayload,
	}

	decision, err := service.engine.Evaluate(action)
	if err != nil {
		if _, appendErr := service.store.Append(ctx, proposed); appendErr != nil {
			return protocol.Decision{}, fmt.Errorf("record action.proposed: %w", appendErr)
		}
		return protocol.Decision{}, err
	}
	decisionPayload, err := json.Marshal(decision)
	if err != nil {
		return protocol.Decision{}, fmt.Errorf("encode decision payload: %w", err)
	}
	decisionPayload, err = redaction.JSON(decisionPayload)
	if err != nil {
		return protocol.Decision{}, fmt.Errorf("redact decision payload: %w", err)
	}
	_, err = service.store.AppendBatch(ctx, []eventstore.AppendRequest{proposed, {
		ProjectID:   action.Workspace.ProjectID,
		WorkspaceID: action.Workspace.WorkspaceID,
		OccurredAt:  service.now(),
		Type:        decisionEventType(decision.Kind),
		Actor: eventstore.Actor{
			Kind:            "runtime",
			AgentKind:       action.Agent.Kind,
			NativeSessionID: action.Agent.NativeSessionID,
			ACRSessionID:    action.Agent.ACRSessionID,
		},
		Causality: eventstore.Causality{
			CorrelationID:    correlationID,
			CausationEventID: proposed.EventID,
			NativeTurnID:     action.Agent.NativeTurnID,
			NativeToolUseID:  action.Tool.CallID,
		},
		Payload: decisionPayload,
	}})
	if err != nil {
		return protocol.Decision{}, fmt.Errorf("record action.proposed and %s: %w", decisionEventType(decision.Kind), err)
	}
	return decision, nil
}

func decisionEventType(kind protocol.DecisionKind) string {
	switch kind {
	case protocol.DecisionPass:
		return "action.passed"
	case protocol.DecisionRewrite:
		return "action.rewritten"
	case protocol.DecisionRepair:
		return "repair.planned"
	case protocol.DecisionAsk:
		return "action.asked"
	case protocol.DecisionDeny:
		return "action.denied"
	default:
		return "action.denied"
	}
}

func (service *Service) RecordToolOutcome(ctx context.Context, outcome ToolOutcome) error {
	if service.store == nil {
		return errors.New("record tool outcome: event store is not configured")
	}
	if err := validateAction(outcome.Action); err != nil {
		return err
	}
	if strings.TrimSpace(outcome.Action.Workspace.ProjectID) == "" {
		return errors.New("record tool outcome requires action workspace.project_id")
	}
	payload := struct {
		ToolName    string          `json:"tool_name"`
		Resources   []string        `json:"resources,omitempty"`
		ToolInput   json.RawMessage `json:"tool_input"`
		ToolResponse json.RawMessage `json:"tool_response,omitempty"`
		Error       string          `json:"error,omitempty"`
		IsInterrupt bool            `json:"is_interrupt,omitempty"`
		DurationMS  int64           `json:"duration_ms,omitempty"`
		ObservedChanges []string    `json:"observed_changes,omitempty"`
	}{
		ToolName:     outcome.Action.Tool.Name,
		ObservedChanges: outcome.ObservedChanges,
		Resources:    outcome.Action.Resources,
		ToolInput:    outcome.Action.Tool.Input,
		ToolResponse: outcome.Response,
		Error:        outcome.Error,
		IsInterrupt:  outcome.IsInterrupt,
		DurationMS:   outcome.DurationMS,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode tool outcome: %w", err)
	}
	encoded, err = redaction.JSON(encoded)
	if err != nil {
		return fmt.Errorf("redact tool outcome: %w", err)
	}
	if service.objects != nil {
		if encoded, err = service.externalizeOutputs(encoded); err != nil {
			return fmt.Errorf("store tool outcome output: %w", err)
		}
	}
	eventType := "action.completed"
	if outcome.Failed {
		eventType = "action.failed"
	}
	correlationID := outcome.Action.Tool.CallID
	if correlationID == "" {
		correlationID = outcome.Action.Agent.NativeSessionID + ":" + outcome.Action.Agent.NativeTurnID + ":" + outcome.Action.Tool.Name
	}
	_, err = service.store.Append(ctx, eventstore.AppendRequest{
		ProjectID:   outcome.Action.Workspace.ProjectID,
		WorkspaceID: outcome.Action.Workspace.WorkspaceID,
		OccurredAt:  service.now(),
		Type:        eventType,
		Actor: eventstore.Actor{
			Kind:            "agent",
			AgentKind:       outcome.Action.Agent.Kind,
			NativeSessionID: outcome.Action.Agent.NativeSessionID,
			ACRSessionID:    outcome.Action.Agent.ACRSessionID,
		},
		Causality: eventstore.Causality{
			CorrelationID:   correlationID,
			NativeTurnID:    outcome.Action.Agent.NativeTurnID,
			NativeToolUseID: outcome.Action.Tool.CallID,
		},
		Payload: encoded,
	})
	if err != nil {
		return fmt.Errorf("record %s: %w", eventType, err)
	}
	return nil
}

// externalizeOutputs replaces an oversized tool_response or error in an
// already-redacted payload with an object reference (tool_response_ref,
// error_ref). tool_input stays inline because coverage matching reads it.
func (service *Service) externalizeOutputs(payload []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, fmt.Errorf("decode redacted payload: %w", err)
	}
	changed := false
	if response, ok := fields["tool_response"]; ok && len(response) > service.inlineLimit {
		ref, err := service.objects.Put(response, "application/json")
		if err != nil {
			return nil, err
		}
		if fields["tool_response_ref"], err = json.Marshal(ref); err != nil {
			return nil, fmt.Errorf("encode tool_response_ref: %w", err)
		}
		delete(fields, "tool_response")
		changed = true
	}
	if raw, ok := fields["error"]; ok && len(raw) > service.inlineLimit {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, fmt.Errorf("decode error text: %w", err)
		}
		ref, err := service.objects.Put([]byte(text), "text/plain")
		if err != nil {
			return nil, err
		}
		if fields["error_ref"], err = json.Marshal(ref); err != nil {
			return nil, fmt.Errorf("encode error_ref: %w", err)
		}
		delete(fields, "error")
		changed = true
	}
	if !changed {
		return payload, nil
	}
	return json.Marshal(fields)
}

func DecodeAction(reader io.Reader) (protocol.Action, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()

	var action protocol.Action
	if err := decoder.Decode(&action); err != nil {
		return protocol.Action{}, fmt.Errorf("decode action: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return protocol.Action{}, errors.New("decode action: multiple JSON values")
		}
		return protocol.Action{}, fmt.Errorf("decode action trailing data: %w", err)
	}
	if err := validateAction(action); err != nil {
		return protocol.Action{}, err
	}
	return action, nil
}

func DecodeActionBytes(data []byte) (protocol.Action, error) {
	return DecodeAction(bytes.NewReader(data))
}

func validateAction(action protocol.Action) error {
	if action.SchemaVersion != protocol.ActionSchemaVersion {
		return fmt.Errorf("action schema_version must be %q, got %q", protocol.ActionSchemaVersion, action.SchemaVersion)
	}
	if action.Agent.Kind != protocol.AgentClaudeCode && action.Agent.Kind != protocol.AgentCodex && action.Agent.Kind != protocol.AgentUnknown {
		return fmt.Errorf("action agent.kind %q is invalid", action.Agent.Kind)
	}
	if strings.TrimSpace(action.Agent.NativeSessionID) == "" {
		return errors.New("action agent.native_session_id is required")
	}
	if strings.TrimSpace(action.Workspace.CWD) == "" {
		return errors.New("action workspace.cwd is required")
	}
	if strings.TrimSpace(action.Tool.Name) == "" {
		return errors.New("action tool.name is required")
	}
	if len(bytes.TrimSpace(action.Tool.Input)) != 0 {
		var input map[string]any
		if err := json.Unmarshal(action.Tool.Input, &input); err != nil {
			return fmt.Errorf("action tool.input must be a JSON object: %w", err)
		}
		if input == nil {
			return errors.New("action tool.input must be a JSON object")
		}
	}
	return nil
}
