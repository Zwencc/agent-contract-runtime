package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The decoders accept the hook inputs captured from real hosts in
// testdata/live, so a host schema change shows up here first.
func TestDecodersAcceptCapturedHostInputs(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "live", "*", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("captured inputs: %v, %v", files, err)
	}
	decoded := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		event := strings.SplitN(strings.TrimSuffix(filepath.Base(file), ".json"), "-", 2)[0]
		switch event {
		case "PreToolUse":
			_, err = DecodePreToolUse(data)
		case "PostToolUse":
			_, err = DecodePostToolUse(data)
		case "SessionStart":
			_, err = DecodeSessionStart(data)
		case "UserPromptSubmit":
			_, err = DecodeUserPromptSubmit(data)
		default:
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", file, err)
		}
		decoded++
	}
	if decoded < 13 {
		t.Fatalf("decoded only %d captured inputs", decoded)
	}
}
