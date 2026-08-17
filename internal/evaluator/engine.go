// Package evaluator applies active contracts to normalized agent actions.
package evaluator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/protocol"
)

type Engine struct {
	contracts  []contract.Contract
	evidence   EvidenceChecker
	authorizer Authorizer
}

// Authorizer reports whether the user lifted a contract for the current turn
// of the action's session.
type Authorizer interface {
	Authorized(action protocol.Action, contractID string) (bool, error)
}

// WithAuthorizer returns an engine that consults authorizer for contracts
// the user may lift for a turn. Without one such contracts always apply.
func (engine *Engine) WithAuthorizer(authorizer Authorizer) *Engine {
	return &Engine{contracts: engine.contracts, evidence: engine.evidence, authorizer: authorizer}
}

// EvidenceChecker reports whether a named verification is valid on the
// action's current inputs, with a short description of its state.
type EvidenceChecker interface {
	Satisfied(action protocol.Action, subject string) (bool, string, error)
}

// WithEvidence returns an engine that consults checker for contracts with
// evidence verifiers. Without a checker such contracts are never satisfied.
func (engine *Engine) WithEvidence(checker EvidenceChecker) *Engine {
	return &Engine{contracts: engine.contracts, evidence: checker, authorizer: engine.authorizer}
}

type applicableContract struct {
	contract contract.Contract
	match    protocol.ContractMatch
}

type mutation struct {
	path          string
	remove        bool
	value         any
	replacePath   bool
	replaceFrom   string
	replaceTo     string
	origin        contract.Contract
}

func New(contracts []contract.Contract) (*Engine, error) {
	seen := make(map[string]struct{}, len(contracts))
	active := make(map[string]int, len(contracts))
	values := make([]contract.Contract, len(contracts))
	copy(values, contracts)

	for _, value := range values {
		if err := value.Validate(); err != nil {
			return nil, fmt.Errorf("validate contract %q version %d: %w", value.Metadata.ID, value.Metadata.Version, err)
		}
		key := fmt.Sprintf("%s@%d", value.Metadata.ID, value.Metadata.Version)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate contract %s", key)
		}
		seen[key] = struct{}{}
		if value.Metadata.Status == contract.StatusActive {
			if version, exists := active[value.Metadata.ID]; exists {
				return nil, fmt.Errorf("contract %s has multiple active versions: %d and %d", value.Metadata.ID, version, value.Metadata.Version)
			}
			active[value.Metadata.ID] = value.Metadata.Version
		}
	}

	sort.SliceStable(values, func(left, right int) bool {
		if values[left].Metadata.Priority == values[right].Metadata.Priority {
			if values[left].Metadata.ID == values[right].Metadata.ID {
				return values[left].Metadata.Version > values[right].Metadata.Version
			}
			return values[left].Metadata.ID < values[right].Metadata.ID
		}
		return values[left].Metadata.Priority > values[right].Metadata.Priority
	})

	return &Engine{contracts: values}, nil
}

// unattendedModes are host permission modes in which nobody answers a
// confirmation prompt, so a decision that needs one becomes a denial.
var unattendedModes = map[string]bool{"bypassPermissions": true, "dontAsk": true}

func (engine *Engine) Evaluate(action protocol.Action) (protocol.Decision, error) {
	decision, err := engine.evaluate(action)
	if err != nil || decision.Kind != protocol.DecisionAsk {
		return decision, err
	}
	if mode := action.Attributes["permission_mode"]; unattendedModes[mode] {
		decision.Kind = protocol.DecisionDeny
		decision.Reason = "human confirmation is required, but nobody answers prompts in permission mode " + mode + ": " + decision.Reason
	} else if action.Attributes["unattended"] == "true" {
		decision.Kind = protocol.DecisionDeny
		decision.Reason = "human confirmation is required, but this session runs unattended: " + decision.Reason
	}
	return decision, nil
}

func (engine *Engine) evaluate(action protocol.Action) (protocol.Decision, error) {
	input, err := decodeInput(action.Tool.Input)
	if err != nil {
		return protocol.Decision{}, err
	}

	applicable, terminal := engine.findApplicable(action, input)
	if terminal != nil {
		return *terminal, nil
	}
	if len(applicable) == 0 {
		return protocol.NewDecision(protocol.DecisionPass, "no applicable active contract"), nil
	}

	if decision := selectTerminalDecision(applicable); decision != nil {
		return *decision, nil
	}

	mutations, conflict := collectMutations(applicable)
	if conflict != "" {
		decision := protocol.NewDecision(protocol.DecisionAsk, conflict)
		decision.Matches = matchesOf(applicable)
		return decision, nil
	}
	if len(mutations) == 0 {
		decision := protocol.NewDecision(protocol.DecisionPass, "applicable advisory contracts require no correction")
		decision.Matches = matchesOf(applicable)
		decision.AdditionalContext = instructionsOf(applicable)
		return decision, nil
	}

	corrected, resources, err := applyMutations(action, input, mutations)
	if err != nil {
		return protocol.Decision{}, err
	}
	encoded, err := json.Marshal(corrected)
	if err != nil {
		return protocol.Decision{}, fmt.Errorf("encode corrected tool input: %w", err)
	}

	correctedAction := action
	correctedAction.Tool.Input = encoded
	correctedAction.Resources = resources
	remaining, terminal := engine.findApplicable(correctedAction, corrected)
	if terminal != nil {
		terminal.Matches = mergeMatches(matchesOf(applicable), terminal.Matches)
		terminal.Reason = "corrected action failed policy evaluation: " + terminal.Reason
		return *terminal, nil
	}
	if terminal = selectTerminalDecision(remaining); terminal != nil {
		terminal.Matches = mergeMatches(matchesOf(applicable), terminal.Matches)
		terminal.Reason = "corrected action failed policy evaluation: " + terminal.Reason
		return *terminal, nil
	}
	if len(remaining) != 0 {
		decision := protocol.NewDecision(protocol.DecisionAsk, "corrected action still matches active contracts; automatic correction stopped after one pass")
		decision.Matches = mergeMatches(matchesOf(applicable), matchesOf(remaining))
		return decision, nil
	}

	decision := protocol.NewDecision(protocol.DecisionRewrite, "tool input corrected and verified against all active contracts")
	decision.EffectiveInput = encoded
	decision.Matches = matchesOf(applicable)
	return decision, nil
}

func (engine *Engine) findApplicable(action protocol.Action, input map[string]any) ([]applicableContract, *protocol.Decision) {
	var applicable []applicableContract
	for _, value := range engine.contracts {
		if value.Metadata.Status != contract.StatusActive || !matchesScope(value.Scope, action) {
			continue
		}
		matches, err := matchesApplicability(value.Applicability, action, input)
		reason := value.Behavior.Instruction
		if err == nil && matches && value.Verifiers != nil {
			var satisfied bool
			satisfied, reason, err = engine.checkEvidence(action, value)
			matches = !satisfied
		}
		if err == nil && matches && value.Authorization != nil {
			authorized := false
			if engine.authorizer != nil {
				authorized, err = engine.authorizer.Authorized(action, value.Metadata.ID)
			}
			matches = !authorized
			reason += fmt.Sprintf(" (the user can allow this for the current turn: only if their latest message explicitly allows it, call mcp__acr__authorize with contract_id %q and their exact words, then retry)", value.Metadata.ID)
		}
		if err != nil {
			match := protocol.ContractMatch{ContractID: value.Metadata.ID, ContractVersion: value.Metadata.Version, Reason: err.Error()}
			switch value.FailureMode {
			case contract.FailureOpen:
				continue
			case contract.FailureAsk:
				decision := protocol.NewDecision(protocol.DecisionAsk, "contract evaluation failed: "+err.Error())
				decision.Matches = []protocol.ContractMatch{match}
				return nil, &decision
			default:
				decision := protocol.NewDecision(protocol.DecisionDeny, "contract evaluation failed closed: "+err.Error())
				decision.Matches = []protocol.ContractMatch{match}
				return nil, &decision
			}
		}
		if matches {
			applicable = append(applicable, applicableContract{
				contract: value,
				match: protocol.ContractMatch{
					ContractID:      value.Metadata.ID,
					ContractVersion: value.Metadata.Version,
					Reason:          reason,
				},
			})
		}
	}
	return applicable, nil
}

// checkEvidence reports whether every evidence requirement of value holds,
// and otherwise returns the instruction extended with what is missing.
func (engine *Engine) checkEvidence(action protocol.Action, value contract.Contract) (bool, string, error) {
	missing := []string{}
	for _, requirement := range value.Verifiers.Evidence {
		satisfied, state := false, "no evidence checker configured"
		if engine.evidence != nil {
			var err error
			if satisfied, state, err = engine.evidence.Satisfied(action, requirement.Subject); err != nil {
				return false, "", fmt.Errorf("check evidence %q: %w", requirement.Subject, err)
			}
		}
		if satisfied {
			continue
		}
		item := requirement.Subject + " is " + state
		if requirement.Remedy != "" {
			item += "; run: " + requirement.Remedy
		}
		missing = append(missing, item)
	}
	if len(missing) == 0 {
		return true, "", nil
	}
	return false, value.Behavior.Instruction + " (missing evidence: " + strings.Join(missing, " | ") + ")", nil
}

func selectTerminalDecision(applicable []applicableContract) *protocol.Decision {
	for _, item := range applicable {
		if item.contract.Correction.Mode == contract.CorrectionDeny || item.contract.Correction.Mode == contract.CorrectionNone && item.contract.Behavior.Enforcement == contract.EnforcementHard {
			decision := protocol.NewDecision(protocol.DecisionDeny, item.match.Reason)
			decision.Matches = matchesOf(applicable)
			return &decision
		}
	}
	for _, item := range applicable {
		if item.contract.Correction.Mode == contract.CorrectionAsk {
			decision := protocol.NewDecision(protocol.DecisionAsk, item.match.Reason)
			decision.Matches = matchesOf(applicable)
			return &decision
		}
	}
	return nil
}

func collectMutations(applicable []applicableContract) ([]mutation, string) {
	byPath := make(map[string]mutation)
	for _, item := range applicable {
		if item.contract.Correction.Mode != contract.CorrectionRewrite {
			continue
		}
		transform := item.contract.Correction.Transform
		switch transform.Type {
		case contract.TransformSetFields:
			for field, value := range transform.Values {
				candidate := mutation{path: field, value: value, origin: item.contract}
				if current, exists := byPath[field]; exists && (current.remove || !reflect.DeepEqual(current.value, value)) {
					return nil, fmt.Sprintf("contracts %s and %s define conflicting corrections for %s", current.origin.Metadata.ID, item.contract.Metadata.ID, field)
				}
				byPath[field] = candidate
			}
		case contract.TransformRemoveFields:
			for _, field := range transform.Fields {
				candidate := mutation{path: field, remove: true, origin: item.contract}
				if current, exists := byPath[field]; exists && !current.remove {
					return nil, fmt.Sprintf("contracts %s and %s define conflicting corrections for %s", current.origin.Metadata.ID, item.contract.Metadata.ID, field)
				}
				byPath[field] = candidate
			}
		case contract.TransformReplacePath:
			field := transform.Field
			candidate := mutation{
				path:        field,
				replacePath: true,
				replaceFrom: transform.From,
				replaceTo:   transform.To,
				origin:      item.contract,
			}
			if current, exists := byPath[field]; exists && (!current.replacePath || current.replaceFrom != candidate.replaceFrom || current.replaceTo != candidate.replaceTo) {
				return nil, fmt.Sprintf("contracts %s and %s define conflicting corrections for %s", current.origin.Metadata.ID, item.contract.Metadata.ID, field)
			}
			byPath[field] = candidate
		}
	}

	paths := make([]string, 0, len(byPath))
	for field := range byPath {
		paths = append(paths, field)
	}
	sort.Strings(paths)
	for index, field := range paths {
		for _, other := range paths[index+1:] {
			if strings.HasPrefix(other, field+".") {
				return nil, fmt.Sprintf("corrections for %s and %s overlap and have no explicit ordering", field, other)
			}
		}
		if _, exists := byPath[contract.FieldPaths]; exists && field != contract.FieldPaths && (field == "input.command" || slices.Contains(pathFields, strings.TrimPrefix(field, "input."))) {
			return nil, fmt.Sprintf("corrections for %s and %s overlap and have no explicit ordering", contract.FieldPaths, field)
		}
	}
	result := make([]mutation, 0, len(paths))
	for _, field := range paths {
		result = append(result, byPath[field])
	}
	return result, ""
}

func applyMutations(action protocol.Action, input map[string]any, mutations []mutation) (map[string]any, []string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, nil, fmt.Errorf("copy tool input: %w", err)
	}
	var output map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&output); err != nil {
		return nil, nil, fmt.Errorf("copy tool input: %w", err)
	}

	resources := action.Resources
	for _, mutation := range mutations {
		if mutation.path == contract.FieldPaths {
			var changed bool
			if resources, changed = rewriteToolPaths(action, output, mutation.replaceFrom, mutation.replaceTo); !changed {
				return nil, nil, fmt.Errorf("cannot correct %s: no %s argument of %s is under %q", contract.FieldPaths, contract.FieldPaths, action.Tool.Name, mutation.replaceFrom)
			}
			continue
		}
		parts := strings.Split(strings.TrimPrefix(mutation.path, "input."), ".")
		current := output
		for _, part := range parts[:len(parts)-1] {
			next, exists := current[part]
			if !exists {
				nested := make(map[string]any)
				current[part] = nested
				current = nested
				continue
			}
			nested, ok := next.(map[string]any)
			if !ok {
				return nil, nil, fmt.Errorf("cannot correct %s: %s is not an object", mutation.path, part)
			}
			current = nested
		}
		field := parts[len(parts)-1]
		if mutation.remove {
			delete(current, field)
		} else if mutation.replacePath {
			value, ok := current[field].(string)
			if !ok {
				return nil, nil, fmt.Errorf("cannot correct %s: value is not a string", mutation.path)
			}
			replaced, ok := replacePath(value, mutation.replaceFrom, mutation.replaceTo, mutation.path == "input.command")
			if !ok {
				return nil, nil, fmt.Errorf("cannot correct %s: path prefix %q was not found in a supported position", mutation.path, mutation.replaceFrom)
			}
			current[field] = replaced
		} else {
			current[field] = mutation.value
		}
	}
	return output, resources, nil
}

func replacePath(value, from, to string, patchCommand bool) (string, bool) {
	if !patchCommand {
		return replacePathPrefix(value, from, to)
	}

	markers := []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "}
	lines := strings.SplitAfter(value, "\n")
	replacedAny := false
	for index, line := range lines {
		ending := ""
		body := line
		if strings.HasSuffix(body, "\n") {
			ending = "\n"
			body = strings.TrimSuffix(body, "\n")
		}
		if strings.HasSuffix(body, "\r") {
			ending = "\r" + ending
			body = strings.TrimSuffix(body, "\r")
		}
		for _, marker := range markers {
			if !strings.HasPrefix(body, marker) {
				continue
			}
			pathValue, ok := replacePathPrefix(strings.TrimPrefix(body, marker), from, to)
			if ok {
				lines[index] = marker + pathValue + ending
				replacedAny = true
			}
			break
		}
	}
	return strings.Join(lines, ""), replacedAny
}

func replacePathPrefix(value, from, to string) (string, bool) {
	if value == from {
		return to, true
	}
	if !strings.HasPrefix(value, from) {
		return value, false
	}
	if !strings.HasSuffix(from, "/") && !strings.HasSuffix(from, "\\") {
		next := value[len(from)]
		if next != '/' && next != '\\' {
			return value, false
		}
	}
	return to + value[len(from):], true
}

func matchesScope(scope contract.Scope, action protocol.Action) bool {
	return stringIn(scope.ProjectIDs, action.Workspace.ProjectID) &&
		globIn(scope.WorkspaceGlobs, action.Workspace.CWD) &&
		stringIn(scope.AgentKinds, string(action.Agent.Kind)) &&
		stringIn(scope.ToolNames, action.Tool.Name) &&
		stringIn(scope.OperationKinds, action.OperationKind)
}

func matchesApplicability(applicability contract.Applicability, action protocol.Action, input map[string]any) (bool, error) {
	for _, condition := range applicability.All {
		actual, exists := resolveField(condition.Field, action, input)
		var expected any
		decoder := json.NewDecoder(bytes.NewReader(condition.Value))
		decoder.UseNumber()
		if err := decoder.Decode(&expected); err != nil {
			return false, fmt.Errorf("decode condition %s: %w", condition.Field, err)
		}

		var matches bool
		switch condition.Operator {
		case contract.OperatorEquals:
			matches = exists && reflect.DeepEqual(actual, expected)
		case contract.OperatorNotEquals:
			matches = !exists || !reflect.DeepEqual(actual, expected)
		case contract.OperatorGlob:
			if paths, ok := actual.([]any); ok && condition.Field == contract.FieldPaths {
				matches = matchesAnyPath(paths, expected.(string), true)
				break
			}
			actualString, ok := actual.(string)
			if !ok || !exists {
				matches = false
				break
			}
			pattern := expected.(string)
			var err error
			matches, err = path.Match(pattern, actualString)
			if err != nil {
				return false, fmt.Errorf("condition %s has invalid glob: %w", condition.Field, err)
			}
		case contract.OperatorContains:
			if paths, ok := actual.([]any); ok && condition.Field == contract.FieldPaths {
				matches = matchesAnyPath(paths, expected.(string), false)
				break
			}
			actualString, ok := actual.(string)
			matches = exists && ok && strings.Contains(actualString, expected.(string))
		default:
			return false, fmt.Errorf("condition %s has unsupported operator %s", condition.Field, condition.Operator)
		}
		if !matches {
			return false, nil
		}
	}
	return true, nil
}

func resolveField(field string, action protocol.Action, input map[string]any) (any, bool) {
	switch field {
	case "schema_version":
		return action.SchemaVersion, true
	case "agent.kind":
		return string(action.Agent.Kind), true
	case "agent.native_session_id":
		return action.Agent.NativeSessionID, true
	case "workspace.cwd":
		return action.Workspace.CWD, true
	case "workspace.project_id":
		return action.Workspace.ProjectID, true
	case "workspace.workspace_id":
		return action.Workspace.WorkspaceID, true
	case "tool.name":
		return action.Tool.Name, true
	case "tool.call_id":
		return action.Tool.CallID, true
	case "operation_kind":
		return action.OperationKind, true
	case contract.FieldPaths:
		return projectPaths(action), true
	}

	if strings.HasPrefix(field, "attributes.") {
		value, exists := action.Attributes[strings.TrimPrefix(field, "attributes.")]
		return value, exists
	}
	if !strings.HasPrefix(field, "input.") {
		return nil, false
	}
	var current any = input
	for _, part := range strings.Split(strings.TrimPrefix(field, "input."), ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func decodeInput(input json.RawMessage) (map[string]any, error) {
	if len(bytes.TrimSpace(input)) == 0 {
		return map[string]any{}, nil
	}
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode tool input: %w", err)
	}
	if value == nil {
		return nil, errors.New("decode tool input: expected a JSON object")
	}
	return value, nil
}

func stringIn(values []string, candidate string) bool {
	if len(values) == 0 {
		return true
	}
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func globIn(patterns []string, candidate string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if matched, err := path.Match(pattern, candidate); err == nil && matched {
			return true
		}
	}
	return false
}

func matchesOf(values []applicableContract) []protocol.ContractMatch {
	result := make([]protocol.ContractMatch, 0, len(values))
	for _, value := range values {
		result = append(result, value.match)
	}
	return result
}

func mergeMatches(groups ...[]protocol.ContractMatch) []protocol.ContractMatch {
	seen := make(map[string]struct{})
	var result []protocol.ContractMatch
	for _, group := range groups {
		for _, match := range group {
			key := fmt.Sprintf("%s@%d", match.ContractID, match.ContractVersion)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, match)
		}
	}
	return result
}

func instructionsOf(values []applicableContract) string {
	instructions := make([]string, 0, len(values))
	for _, value := range values {
		instructions = append(instructions, value.match.Reason)
	}
	return strings.Join(instructions, "\n")
}
