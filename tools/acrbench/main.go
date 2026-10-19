// Command acrbench fills an initialized ACR project with a realistic event
// history, for measuring hook latency at scale. It is a development tool:
// the events it writes describe sessions that never happened.
//
//	acrbench -root DIR -events 100000
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/memory"
	"github.com/agent-contract-runtime/acr/internal/project"
	"github.com/agent-contract-runtime/acr/internal/protocol"
	"github.com/agent-contract-runtime/acr/internal/registry"
)

const (
	turnsPerSession = 30
	callsPerTurn    = 3
	batchSize       = 2000
)

func main() {
	root := flag.String("root", "", "initialized ACR project")
	total := flag.Int("events", 10000, "events to add")
	memories := flag.Int("memories", 1000, "active memories to add")
	flag.Parse()
	if err := generate(*root, *total, *memories); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(root string, total, memoryCount int) error {
	config, paths, err := project.Load(root)
	if err != nil {
		return err
	}
	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx := context.Background()
	start := time.Now()

	batch := []eventstore.AppendRequest{}
	written := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, err := store.AppendBatch(ctx, batch); err != nil {
			return err
		}
		written += len(batch)
		batch = batch[:0]
		return nil
	}
	add := func(eventType string, actor eventstore.Actor, turn string, payload any) error {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		batch = append(batch, eventstore.AppendRequest{ProjectID: config.ProjectID, WorkspaceID: config.WorkspaceID, OccurredAt: time.Now(),
			Type: eventType, Actor: actor, Causality: eventstore.Causality{NativeTurnID: turn}, Payload: encoded})
		if len(batch) == batchSize {
			return flush()
		}
		return nil
	}

	rules, err := contract.LoadPath(paths.Contracts)
	if err != nil {
		return err
	}
	for session := 0; written+len(batch) < total; session++ {
		agent := protocol.AgentClaudeCode
		if session%2 == 1 {
			agent = protocol.AgentCodex
		}
		native := fmt.Sprintf("bench-session-%06d", session)
		launch := ""
		if session%3 == 0 {
			launch = fmt.Sprintf("bench-launch-%06d", session)
		}
		actor := eventstore.Actor{Kind: "agent", AgentKind: agent, NativeSessionID: native, ACRSessionID: launch}
		if err := add("session.started", actor, "", map[string]string{"source": "startup", "cwd": paths.Root}); err != nil {
			return err
		}
		for turn := 0; turn < turnsPerSession && written+len(batch) < total; turn++ {
			turnID := fmt.Sprintf("%s-turn-%02d", native, turn)
			user := actor
			user.Kind = "user"
			prompt := fmt.Sprintf("请修改 internal/module%03d/handler.go 里的分页逻辑，并补测试，然后跑 go test ./internal/module%03d/...", turn, turn)
			if err := add("intent.observed", user, turnID, map[string]any{"prompt": prompt, "mentioned_paths": []string{}, "source": "UserPromptSubmit"}); err != nil {
				return err
			}
			for call := 0; call < callsPerTurn; call++ {
				file := fmt.Sprintf("%s/internal/module%03d/handler.go", paths.Root, (session+call)%500)
				action := protocol.NewAction(protocol.AgentContext{Kind: agent, NativeSessionID: native, ACRSessionID: launch, NativeTurnID: turnID},
					protocol.WorkspaceContext{CWD: paths.Root, Root: paths.Root, ProjectID: config.ProjectID, WorkspaceID: config.WorkspaceID},
					protocol.ToolCall{Name: "Edit", CallID: fmt.Sprintf("%s-call-%d", turnID, call),
						Input: json.RawMessage(fmt.Sprintf(`{"file_path":%q,"old_string":"limit := 20","new_string":"limit := pageSize"}`, file))})
				action.OperationKind, action.Resources = "filesystem.write", []string{file}
				if err := add("action.proposed", actor, turnID, action); err != nil {
					return err
				}
				decision := "action.passed"
				if (written+len(batch))%20 == 0 {
					decision = "action.denied"
				}
				runtime := actor
				runtime.Kind = "runtime"
				if err := add(decision, runtime, turnID, protocol.NewDecision(protocol.DecisionPass, "no applicable active contract")); err != nil {
					return err
				}
				if err := add("action.completed", actor, turnID, map[string]any{"tool_name": "Edit", "resources": []string{file},
					"tool_input": json.RawMessage(action.Tool.Input), "tool_response": map[string]string{"output": "The file has been updated."}, "duration_ms": 12}); err != nil {
					return err
				}
			}
			if (written+len(batch))%2000 < 10 {
				runtime := eventstore.Actor{Kind: "runtime"}
				if err := add("verification.passed", runtime, "", map[string]any{"subject": "repair:checks/unit", "stable": true,
					"input_fingerprint": map[string]any{"digest": "sha256:bench", "files": 120}}); err != nil {
					return err
				}
			}
		}
		if err := add("session.ended", actor, "", map[string]string{"reason": "other"}); err != nil {
			return err
		}
		// Rules change now and then; each change goes through the registry.
		if session%200 == 0 && len(rules) > 0 {
			if err := flush(); err != nil {
				return err
			}
			value := rules[(session/200)%len(rules)]
			if _, err := registry.Activate(ctx, store, config.ProjectID, config.WorkspaceID, paths.Contracts, value, registry.Source{Kind: registry.SourceCLI}, time.Now()); err != nil {
				return err
			}
			written++
		}
	}
	if err := flush(); err != nil {
		return err
	}

	store2, err := memory.Open(paths.MemoryDB)
	if err != nil {
		return err
	}
	defer store2.Close()
	for index := 0; index < memoryCount; index++ {
		value, err := store2.CreateCandidate(ctx, memory.CreateRequest{ProjectID: config.ProjectID, Kind: "constraint", Priority: memory.Priority(index % 7),
			Claim: fmt.Sprintf("Module %03d keeps its public API stable; change handlers only through the adapter layer", index), Scope: json.RawMessage(`{}`),
			EvidenceRefs: []string{"event:bench"}, Source: "user", Confidence: 1})
		if err != nil {
			return err
		}
		if _, err := store2.Activate(ctx, config.ProjectID, value.MemoryID, "bench"); err != nil {
			return err
		}
	}
	fmt.Printf("added %d events and %d memories in %s\n", written, memoryCount, time.Since(start).Round(time.Millisecond))
	return nil
}
