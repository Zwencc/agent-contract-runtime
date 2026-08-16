package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

func Decode(reader io.Reader) (Contract, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	decoder.UseNumber()

	var value Contract
	if err := decoder.Decode(&value); err != nil {
		return Contract{}, fmt.Errorf("decode contract: %w", err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Contract{}, errors.New("decode contract: multiple JSON values")
		}
		return Contract{}, fmt.Errorf("decode contract trailing data: %w", err)
	}

	if err := value.Validate(); err != nil {
		return Contract{}, err
	}
	return value, nil
}

func DecodeBytes(data []byte) (Contract, error) {
	return Decode(bytes.NewReader(data))
}

func (contract Contract) Validate() error {
	if contract.APIVersion != APIVersion {
		return fmt.Errorf("contract apiVersion %q is unsupported", contract.APIVersion)
	}
	if contract.Kind != "Contract" {
		return fmt.Errorf("contract kind must be Contract, got %q", contract.Kind)
	}
	if strings.TrimSpace(contract.Metadata.ID) == "" {
		return errors.New("contract metadata.id is required")
	}
	if contract.Metadata.Version < 1 {
		return errors.New("contract metadata.version must be at least 1")
	}
	if !validStatus(contract.Metadata.Status) {
		return fmt.Errorf("contract metadata.status %q is invalid", contract.Metadata.Status)
	}
	if strings.TrimSpace(contract.Behavior.Instruction) == "" {
		return errors.New("contract behavior.instruction is required")
	}
	if !validEnforcement(contract.Behavior.Enforcement) {
		return fmt.Errorf("contract behavior.enforcement %q is invalid", contract.Behavior.Enforcement)
	}
	if !validFailureMode(contract.FailureMode) {
		return fmt.Errorf("contract failure_mode %q is invalid", contract.FailureMode)
	}
	if len(contract.Applicability.All) == 0 {
		return errors.New("contract applicability.all must contain at least one condition")
	}
	for index, pattern := range contract.Scope.WorkspaceGlobs {
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("contract scope.workspace_globs[%d] is invalid: %w", index, err)
		}
	}
	for index, condition := range contract.Applicability.All {
		if err := condition.validate(); err != nil {
			return fmt.Errorf("contract applicability.all[%d]: %w", index, err)
		}
	}
	if err := contract.Correction.validate(); err != nil {
		return fmt.Errorf("contract correction: %w", err)
	}
	if contract.Verifiers != nil {
		if len(contract.Verifiers.Evidence) == 0 || len(contract.Verifiers.Evidence) > 16 {
			return errors.New("contract verifiers.evidence must list 1 to 16 requirements")
		}
		if contract.Correction.Mode == CorrectionRewrite {
			return errors.New("contract with evidence verifiers cannot use correction mode rewrite")
		}
		for index, requirement := range contract.Verifiers.Evidence {
			if strings.TrimSpace(requirement.Subject) == "" || len(requirement.Subject) > 256 || len(requirement.Remedy) > 1024 {
				return fmt.Errorf("contract verifiers.evidence[%d] needs a subject of at most 256 bytes and a remedy of at most 1024 bytes", index)
			}
			if plan := requirement.Plan; plan != "" {
				clean := path.Clean(plan)
				if path.IsAbs(plan) || strings.Contains(plan, "\\") || clean == ".." || strings.HasPrefix(clean, "../") {
					return fmt.Errorf("contract verifiers.evidence[%d].plan must be a project-relative path", index)
				}
			}
		}
	}
	if contract.Authorization != nil {
		if !contract.Authorization.UserInTurn {
			return errors.New("contract authorization must set user_in_turn")
		}
		for _, mention := range contract.Authorization.Mentions {
			if strings.TrimSpace(mention) == "" {
				return errors.New("contract authorization.mentions must not contain empty words")
			}
		}
	}
	return nil
}

func (condition Condition) validate() error {
	if strings.TrimSpace(condition.Field) == "" {
		return errors.New("field is required")
	}
	if !validConditionField(condition.Field) {
		return fmt.Errorf("field %q is unsupported", condition.Field)
	}
	if condition.Operator != OperatorEquals && condition.Operator != OperatorNotEquals && condition.Operator != OperatorGlob && condition.Operator != OperatorContains {
		return fmt.Errorf("operator %q is invalid", condition.Operator)
	}
	if len(condition.Value) == 0 || !json.Valid(condition.Value) {
		return errors.New("value must be valid JSON")
	}
	if condition.Operator == OperatorGlob || condition.Operator == OperatorContains {
		var value string
		if err := json.Unmarshal(condition.Value, &value); err != nil {
			return fmt.Errorf("operator %s requires a string value", condition.Operator)
		}
		if condition.Operator == OperatorGlob {
			if _, err := path.Match(value, ""); err != nil {
				return fmt.Errorf("operator glob has invalid pattern: %w", err)
			}
		}
	}
	return nil
}

func validConditionField(field string) bool {
	switch field {
	case "schema_version", "agent.kind", "agent.native_session_id", "workspace.cwd", "workspace.project_id", "workspace.workspace_id", "tool.name", "tool.call_id", "operation_kind", FieldPaths:
		return true
	}
	return hasNonEmptyPath(field, "input.") || hasNonEmptyPath(field, "attributes.")
}

func hasNonEmptyPath(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, part := range strings.Split(strings.TrimPrefix(value, prefix), ".") {
		if strings.TrimSpace(part) == "" {
			return false
		}
	}
	return true
}

func (correction Correction) validate() error {
	switch correction.Mode {
	case CorrectionNone, CorrectionAsk, CorrectionDeny:
		if correction.Transform != nil {
			return fmt.Errorf("mode %s cannot define a transform", correction.Mode)
		}
		return nil
	case CorrectionRewrite:
		if correction.Transform == nil {
			return errors.New("rewrite mode requires a transform")
		}
	default:
		return fmt.Errorf("mode %q is invalid", correction.Mode)
	}

	switch correction.Transform.Type {
	case TransformSetFields:
		if len(correction.Transform.Values) == 0 {
			return errors.New("set_fields requires values")
		}
		if len(correction.Transform.Fields) != 0 {
			return errors.New("set_fields cannot define fields")
		}
		for field := range correction.Transform.Values {
			if err := validateInputPath(field); err != nil {
				return err
			}
		}
		if correction.Transform.Field != "" || correction.Transform.From != "" || correction.Transform.To != "" {
			return errors.New("set_fields cannot define field, from, or to")
		}
	case TransformRemoveFields:
		if len(correction.Transform.Fields) == 0 {
			return errors.New("remove_fields requires fields")
		}
		if len(correction.Transform.Values) != 0 {
			return errors.New("remove_fields cannot define values")
		}
		for _, field := range correction.Transform.Fields {
			if err := validateInputPath(field); err != nil {
				return err
			}
		}
		if correction.Transform.Field != "" || correction.Transform.From != "" || correction.Transform.To != "" {
			return errors.New("remove_fields cannot define field, from, or to")
		}
	case TransformReplacePath:
		if len(correction.Transform.Values) != 0 || len(correction.Transform.Fields) != 0 {
			return errors.New("replace_path cannot define values or fields")
		}
		if correction.Transform.Field == FieldPaths {
			for _, value := range []string{correction.Transform.From, correction.Transform.To} {
				if clean := path.Clean(value); value == "" || path.IsAbs(value) || strings.Contains(value, "\\") || clean != value || clean == ".." || strings.HasPrefix(clean, "../") {
					return fmt.Errorf("replace_path on %s requires clean project-relative from and to, got %q", FieldPaths, value)
				}
			}
		} else if err := validateInputPath(correction.Transform.Field); err != nil {
			return err
		}
		if correction.Transform.From == "" || correction.Transform.To == "" {
			return errors.New("replace_path requires non-empty from and to")
		}
		if correction.Transform.From == correction.Transform.To {
			return errors.New("replace_path from and to must differ")
		}
	default:
		return fmt.Errorf("transform type %q is invalid", correction.Transform.Type)
	}
	return nil
}

func validateInputPath(path string) error {
	if !strings.HasPrefix(path, "input.") || strings.TrimSpace(strings.TrimPrefix(path, "input.")) == "" {
		return fmt.Errorf("transform field %q must start with input.", path)
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "input."), ".") {
		if strings.TrimSpace(part) == "" {
			return fmt.Errorf("transform field %q contains an empty path component", path)
		}
	}
	return nil
}

func validStatus(status Status) bool {
	return status == StatusDraft || status == StatusValidated || status == StatusApproved || status == StatusActive || status == StatusSuspended || status == StatusRetired
}

func validEnforcement(enforcement Enforcement) bool {
	return enforcement == EnforcementAdvisory || enforcement == EnforcementSoft || enforcement == EnforcementHard
}

func validFailureMode(mode FailureMode) bool {
	return mode == FailureOpen || mode == FailureClosed || mode == FailureAsk
}
