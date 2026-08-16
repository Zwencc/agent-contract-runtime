package contract

import (
	"strings"
	"testing"
)

const validContractJSON = `{
  "apiVersion": "acr.dev/v1alpha1",
  "kind": "Contract",
  "metadata": {
    "id": "rewrite-generated-path",
    "version": 1,
    "title": "Write source instead of generated output",
    "status": "active",
    "severity": "high",
    "priority": 900
  },
  "scope": {
    "agent_kinds": ["claude-code", "codex"],
    "tool_names": ["write_file"]
  },
  "applicability": {
    "all": [
      {"field": "input.path", "operator": "glob", "value": "generated/*"}
    ]
  },
  "behavior": {
    "instruction": "Write the source file, not generated output",
    "enforcement": "hard"
  },
  "correction": {
    "mode": "rewrite",
    "transform": {
      "type": "set_fields",
      "values": {"input.path": "src/schema.go"}
    }
  },
  "failure_mode": "closed"
}`

func TestDecodeValidContract(t *testing.T) {
	value, err := DecodeBytes([]byte(validContractJSON))
	if err != nil {
		t.Fatalf("DecodeBytes() error = %v", err)
	}
	if value.Metadata.ID != "rewrite-generated-path" {
		t.Fatalf("metadata.id = %q", value.Metadata.ID)
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	input := strings.Replace(validContractJSON, `"failure_mode": "closed"`, `"failure_mode": "closed", "unexpected": true`, 1)
	_, err := DecodeBytes([]byte(input))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("DecodeBytes() error = %v, want unknown field", err)
	}
}

func TestValidateRejectsExecutablePathOutsideInput(t *testing.T) {
	input := strings.Replace(validContractJSON, `"input.path": "src/schema.go"`, `"workspace.cwd": "/tmp"`, 1)
	_, err := DecodeBytes([]byte(input))
	if err == nil || !strings.Contains(err.Error(), "must start with input.") {
		t.Fatalf("DecodeBytes() error = %v, want restricted transform path", err)
	}
}

func TestValidateRejectsInactiveLifecycleValue(t *testing.T) {
	input := strings.Replace(validContractJSON, `"status": "active"`, `"status": "enabled"`, 1)
	_, err := DecodeBytes([]byte(input))
	if err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("DecodeBytes() error = %v, want invalid status", err)
	}
}

func TestValidateRejectsUnknownConditionField(t *testing.T) {
	input := strings.Replace(validContractJSON, `"field": "input.path"`, `"field": "imput.path"`, 1)
	_, err := DecodeBytes([]byte(input))
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("DecodeBytes() error = %v, want unsupported field", err)
	}
}

func TestValidateRejectsInvalidGlob(t *testing.T) {
	input := strings.Replace(validContractJSON, `"value": "generated/*"`, `"value": "[invalid"`, 1)
	_, err := DecodeBytes([]byte(input))
	if err == nil || !strings.Contains(err.Error(), "invalid pattern") {
		t.Fatalf("DecodeBytes() error = %v, want invalid glob", err)
	}
}

func TestValidateAcceptsRestrictedReplacePath(t *testing.T) {
	input := strings.Replace(
		validContractJSON,
		`"type": "set_fields",\n      "values": {"input.path": "src/schema.go"}`,
		`"type": "replace_path",\n      "field": "input.path",\n      "from": "generated",\n      "to": "src"`,
		1,
	)
	if _, err := DecodeBytes([]byte(input)); err != nil {
		t.Fatalf("DecodeBytes() error = %v", err)
	}
}
