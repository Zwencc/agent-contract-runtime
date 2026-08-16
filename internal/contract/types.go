// Package contract defines versioned executable constraints consumed by ACR.
package contract

import "encoding/json"

const APIVersion = "acr.dev/v1alpha1"

type Status string

const (
	StatusDraft     Status = "draft"
	StatusValidated Status = "validated"
	StatusApproved  Status = "approved"
	StatusActive    Status = "active"
	StatusSuspended Status = "suspended"
	StatusRetired   Status = "retired"
)

type Enforcement string

const (
	EnforcementAdvisory Enforcement = "advisory"
	EnforcementSoft     Enforcement = "soft"
	EnforcementHard     Enforcement = "hard"
)

type FailureMode string

const (
	FailureOpen   FailureMode = "open"
	FailureClosed FailureMode = "closed"
	FailureAsk    FailureMode = "ask"
)

type CorrectionMode string

const (
	CorrectionNone    CorrectionMode = "none"
	CorrectionRewrite CorrectionMode = "rewrite"
	CorrectionAsk     CorrectionMode = "ask"
	CorrectionDeny    CorrectionMode = "deny"
)

// FieldPaths is the condition and replace_path field that names the files an
// action touches, as clean project-relative slash paths, whatever tool the
// agent used. Glob conditions on it match when any path matches and support
// "**"; replace_path on it rewrites the path arguments of the tool.
const FieldPaths = "paths"

type Operator string

const (
	OperatorEquals    Operator = "equals"
	OperatorNotEquals Operator = "not_equals"
	OperatorGlob      Operator = "glob"
	OperatorContains  Operator = "contains"
)

type TransformType string

const (
	TransformSetFields    TransformType = "set_fields"
	TransformRemoveFields TransformType = "remove_fields"
	TransformReplacePath  TransformType = "replace_path"
)

type Contract struct {
	APIVersion    string        `json:"apiVersion"`
	Kind          string        `json:"kind"`
	Metadata      Metadata      `json:"metadata"`
	Scope         Scope         `json:"scope"`
	Applicability Applicability `json:"applicability"`
	Behavior      Behavior      `json:"behavior"`
	Correction    Correction    `json:"correction"`
	Verifiers     *Verifiers    `json:"verifiers,omitempty"`
	Authorization *Authorization `json:"authorization,omitempty"`
	FailureMode   FailureMode   `json:"failure_mode"`
}

// Authorization lets the user lift a contract for one turn: when the user's
// latest prompt in the session explicitly allows the action, the agent
// quotes those words to ACR, and the contract does not apply until the user
// sends another prompt.
type Authorization struct {
	UserInTurn bool `json:"user_in_turn"`
	// Mentions, when set, are words the quoted permission must contain, such
	// as the name of the protected database, so that a permission given for
	// something else does not lift this contract.
	Mentions []string `json:"mentions,omitempty"`
}

// Verifiers declares checks that decide whether an applicable contract is
// already satisfied.
type Verifiers struct {
	// Evidence lists verifications that must be valid on the current inputs.
	// When all are, the contract is satisfied and does not apply; otherwise
	// its correction mode decides the outcome.
	Evidence []EvidenceRequirement `json:"evidence,omitempty"`
}

type EvidenceRequirement struct {
	// Subject names a verification, such as "repair:<plan>/<step>".
	Subject string `json:"subject"`
	// Remedy tells the agent how to produce the evidence, for example the
	// repair plan command to run.
	Remedy string `json:"remedy,omitempty"`
	// Plan is a project-relative repair plan that produces the evidence.
	// In guarded mode ACR runs it itself when the plan is repeatable.
	Plan string `json:"plan,omitempty"`
}

type Metadata struct {
	ID       string   `json:"id"`
	Version  int      `json:"version"`
	Title    string   `json:"title"`
	Status   Status   `json:"status"`
	Severity string   `json:"severity"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags,omitempty"`
}

type Scope struct {
	ProjectIDs     []string `json:"project_ids,omitempty"`
	WorkspaceGlobs []string `json:"workspace_globs,omitempty"`
	AgentKinds     []string `json:"agent_kinds,omitempty"`
	ToolNames      []string `json:"tool_names,omitempty"`
	OperationKinds []string `json:"operation_kinds,omitempty"`
}

type Applicability struct {
	All []Condition `json:"all"`
}

type Condition struct {
	Field    string          `json:"field"`
	Operator Operator        `json:"operator"`
	Value    json.RawMessage `json:"value"`
}

type Behavior struct {
	Instruction   string      `json:"instruction"`
	Enforcement   Enforcement `json:"enforcement"`
	NonOverridable bool       `json:"non_overridable,omitempty"`
}

type Correction struct {
	Mode      CorrectionMode `json:"mode"`
	Transform *Transform     `json:"transform,omitempty"`
}

type Transform struct {
	Type   TransformType  `json:"type"`
	Values map[string]any `json:"values,omitempty"`
	Fields []string       `json:"fields,omitempty"`
	Field  string         `json:"field,omitempty"`
	From   string         `json:"from,omitempty"`
	To     string         `json:"to,omitempty"`
}
