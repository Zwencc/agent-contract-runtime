package hookconfig

import (
	"bytes"
	"testing"
)

func TestMergePreservesExistingConfigAndIsIdempotent(t *testing.T) {
	existing := []byte(`{"custom":{"enabled":true},"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"existing"}]}]}}`)
	commands := Commands{PreToolUse: "acr pre", PostToolUse: "acr post", PostToolUseFailure: "acr failure"}
	first, err := Merge(existing, "claude", commands)
	if err != nil {
		t.Fatalf("Merge() error = %v", err)
	}
	second, err := Merge(first, "claude", commands)
	if err != nil {
		t.Fatalf("second Merge() error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("Merge() is not idempotent:\n%s\n%s", first, second)
	}
	for _, expected := range [][]byte{[]byte(`"custom"`), []byte(`"SessionStart"`), []byte(`"PreToolUse"`), []byte(`"PostToolUseFailure"`)} {
		if !bytes.Contains(first, expected) {
			t.Fatalf("merged config lacks %s: %s", expected, first)
		}
	}
}

func TestMergeRejectsInvalidHooksShape(t *testing.T) {
	_, err := Merge([]byte(`{"hooks":[]}`), "codex", Commands{PreToolUse: "acr pre"})
	if err == nil {
		t.Fatal("Merge() accepted non-object hooks")
	}
}
