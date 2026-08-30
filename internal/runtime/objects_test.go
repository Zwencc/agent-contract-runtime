package runtime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/objectstore"
)

func TestRecordToolOutcomeMovesLargeOutputsToObjects(t *testing.T) {
	directory := t.TempDir()
	store, err := eventstore.Open(filepath.Join(directory, "events.db"))
	if err != nil {
		t.Fatalf("eventstore.Open() error = %v", err)
	}
	defer store.Close()
	objects, err := objectstore.Open(filepath.Join(directory, "objects"))
	if err != nil {
		t.Fatalf("objectstore.Open() error = %v", err)
	}
	service, err := NewWithEventStore(nil, store)
	if err != nil {
		t.Fatalf("NewWithEventStore() error = %v", err)
	}
	service.UseObjectStore(objects, 1024)
	action, err := DecodeActionBytes([]byte(validActionJSON))
	if err != nil {
		t.Fatalf("DecodeActionBytes() error = %v", err)
	}

	secret := "sk-" + strings.Repeat("a", 24)
	large, _ := json.Marshal(map[string]string{"stdout": strings.Repeat("line\n", 2000) + "token " + secret})
	outcomes := []ToolOutcome{
		{Action: action, Response: large},
		{Action: action, Response: []byte(`{"stdout":"small"}`)},
		{Action: action, Failed: true, Error: strings.Repeat("stack frame\n", 500) + secret},
	}
	for _, outcome := range outcomes {
		if err := service.RecordToolOutcome(context.Background(), outcome); err != nil {
			t.Fatalf("RecordToolOutcome() error = %v", err)
		}
	}
	events, err := store.List(context.Background(), "project-1", 0, 10)
	if err != nil || len(events) != 3 {
		t.Fatalf("store.List() = %d events, %v", len(events), err)
	}

	type payload struct {
		ToolInput       json.RawMessage  `json:"tool_input"`
		ToolResponse    json.RawMessage  `json:"tool_response"`
		ToolResponseRef *objectstore.Ref `json:"tool_response_ref"`
		Error           string           `json:"error"`
		ErrorRef        *objectstore.Ref `json:"error_ref"`
	}
	decoded := make([]payload, len(events))
	for i, event := range events {
		if len(event.Payload) > 2048 {
			t.Fatalf("event %d payload is %d bytes; large output was kept inline", i, len(event.Payload))
		}
		if strings.Contains(string(event.Payload), secret) {
			t.Fatalf("event %d payload contains the secret", i)
		}
		if err := json.Unmarshal(event.Payload, &decoded[i]); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(decoded[i].ToolInput), "generated/schema.go") {
			t.Fatalf("event %d lost inline tool_input: %s", i, event.Payload)
		}
	}

	if decoded[0].ToolResponseRef == nil || decoded[0].ToolResponse != nil || decoded[0].ToolResponseRef.MediaType != "application/json" {
		t.Fatalf("large response payload = %s", events[0].Payload)
	}
	stored, err := objects.Get(*decoded[0].ToolResponseRef)
	if err != nil {
		t.Fatalf("objects.Get() error = %v", err)
	}
	if strings.Contains(string(stored), secret) || !strings.Contains(string(stored), "[REDACTED]") {
		t.Fatal("stored response was not redacted before entering the object store")
	}

	if decoded[1].ToolResponseRef != nil || string(decoded[1].ToolResponse) != `{"stdout":"small"}` {
		t.Fatalf("small response payload = %s", events[1].Payload)
	}

	if decoded[2].ErrorRef == nil || decoded[2].Error != "" || decoded[2].ErrorRef.MediaType != "text/plain" {
		t.Fatalf("large error payload = %s", events[2].Payload)
	}
	storedError, err := objects.Get(*decoded[2].ErrorRef)
	if err != nil || strings.Contains(string(storedError), secret) || !strings.HasPrefix(string(storedError), "stack frame\n") {
		t.Fatalf("stored error = %.40q, %v", storedError, err)
	}
	if count, err := objects.Verify(); err != nil || count != 2 {
		t.Fatalf("objects.Verify() = %d, %v; want 2", count, err)
	}
}
