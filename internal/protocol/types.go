// Package protocol defines the stable, agent-neutral messages exchanged by
// ACR adapters, the contract engine, and the event store.
package protocol

import "encoding/json"

const (
	ActionSchemaVersion   = "acr.action/v1alpha1"
	DecisionSchemaVersion = "acr.decision/v1alpha1"
)

type AgentKind string

const (
	AgentClaudeCode AgentKind = "claude-code"
	AgentCodex      AgentKind = "codex"
	AgentUnknown    AgentKind = "unknown"
)

type DecisionKind string

const (
	DecisionPass    DecisionKind = "pass"
	DecisionRewrite DecisionKind = "rewrite"
	DecisionRepair  DecisionKind = "repair"
	DecisionAsk     DecisionKind = "ask"
	DecisionDeny    DecisionKind = "deny"
)

type AgentContext struct {
	Kind            AgentKind `json:"kind"`
	NativeSessionID string    `json:"native_session_id"`
	ACRSessionID    string    `json:"acr_session_id,omitempty"` // the acr launch that started the agent
	NativeTurnID    string    `json:"native_turn_id,omitempty"`
	Version         string    `json:"version,omitempty"`
}

type WorkspaceContext struct {
	CWD         string `json:"cwd"`
	Root        string `json:"root,omitempty"` // project root; contract paths are relative to it
	ProjectID   string `json:"project_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

type ToolCall struct {
	Name   string          `json:"name"`
	Input  json.RawMessage `json:"input"`
	CallID string          `json:"call_id,omitempty"`
}

type Action struct {
	SchemaVersion string            `json:"schema_version"`
	Agent         AgentContext      `json:"agent"`
	Workspace     WorkspaceContext  `json:"workspace"`
	Tool          ToolCall          `json:"tool"`
	OperationKind string            `json:"operation_kind"`
	Resources     []string          `json:"resources,omitempty"`
	Attributes    map[string]string `json:"attributes,omitempty"`
}

func NewAction(agent AgentContext, workspace WorkspaceContext, tool ToolCall) Action {
	return Action{
		SchemaVersion: ActionSchemaVersion,
		Agent:         agent,
		Workspace:     workspace,
		Tool:          tool,
		OperationKind: "tool.call",
	}
}

type ContractMatch struct {
	ContractID      string `json:"contract_id"`
	ContractVersion int    `json:"contract_version"`
	Reason          string `json:"reason"`
}

type Decision struct {
	SchemaVersion    string          `json:"schema_version"`
	Kind             DecisionKind    `json:"kind"`
	Reason           string          `json:"reason"`
	EffectiveInput   json.RawMessage `json:"effective_input,omitempty"`
	Matches          []ContractMatch `json:"matches,omitempty"`
	AdditionalContext string         `json:"additional_context,omitempty"`
}

func NewDecision(kind DecisionKind, reason string) Decision {
	return Decision{
		SchemaVersion: DecisionSchemaVersion,
		Kind:          kind,
		Reason:        reason,
	}
}
