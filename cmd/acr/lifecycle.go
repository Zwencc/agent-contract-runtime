package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/protocol"
	"github.com/agent-contract-runtime/acr/internal/redaction"
)

// lifecycleEvents maps the host's session and subagent events to the events
// ACR records for them.
var lifecycleEvents = map[string]string{
	"Stop":          "turn.stopped",
	"SubagentStart": "subagent.started",
	"SubagentStop":  "subagent.stopped",
	"SessionEnd":    "session.ended",
}

// runLifecycleHook records when a turn stops, a subagent starts or stops, and
// a session ends. At session end it also reconciles workspace coverage, so
// changes no hook explained surface now instead of at the next session.
func runLifecycleHook(agentName string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	kind := protocol.AgentCodex
	switch agentName {
	case "codex":
	case "claude":
		kind = protocol.AgentClaudeCode
	default:
		fmt.Fprintf(stderr, "unsupported hook agent %q; want codex or claude\n", agentName)
		return 2
	}
	flags := flag.NewFlagSet("hook "+agentName+" lifecycle", flag.ContinueOnError)
	flags.SetOutput(stderr)
	eventDB := flags.String("event-db", "", "SQLite event database path")
	projectID := flags.String("project-id", "", "ACR project id")
	workspaceID := flags.String("workspace-id", "", "ACR workspace id")
	root := flags.String("root", "", "project root; defaults to the nearest .acr project above cwd")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *eventDB == "" || *projectID == "" {
		fmt.Fprintln(stderr, "lifecycle hook requires --event-db and --project-id")
		return 2
	}
	raw, err := readHookInput(stdin)
	if err != nil {
		return writePostHookWarning("ACR could not read hook input: "+err.Error(), stdout, stderr)
	}
	var input struct {
		SessionID            string            `json:"session_id"`
		TurnID               string            `json:"turn_id"`
		PromptID             string            `json:"prompt_id"`
		CWD                  string            `json:"cwd"`
		HookEventName        string            `json:"hook_event_name"`
		AgentID              string            `json:"agent_id"`
		AgentType            string            `json:"agent_type"`
		Reason               string            `json:"reason"`
		LastAssistantMessage string            `json:"last_assistant_message"`
		BackgroundTasks      []json.RawMessage `json:"background_tasks"`
	}
	if err := json.Unmarshal(raw, &input); err != nil || input.SessionID == "" {
		return writePostHookWarning("ACR rejected lifecycle hook input: needs session_id", stdout, stderr)
	}
	eventType, ok := lifecycleEvents[input.HookEventName]
	if !ok {
		return writePostHookWarning("ACR does not handle "+input.HookEventName+" with the lifecycle hook", stdout, stderr)
	}
	if input.TurnID == "" {
		input.TurnID = input.PromptID
	}
	payload, err := json.Marshal(map[string]any{
		"agent_id": input.AgentID, "agent_type": input.AgentType, "reason": input.Reason,
		"last_message": limitUTF8(input.LastAssistantMessage, 1000), "background_tasks": len(input.BackgroundTasks),
	})
	if err == nil {
		payload, err = redaction.JSON(payload)
	}
	if err != nil {
		return writePostHookWarning("ACR could not encode lifecycle event: "+err.Error(), stdout, stderr)
	}
	store, release, err := openEvents(*eventDB)
	if err != nil {
		return writePostHookWarning("ACR could not open event store: "+err.Error(), stdout, stderr)
	}
	defer release()
	ctx := context.Background()
	if _, err := store.Append(ctx, eventstore.AppendRequest{
		ProjectID: *projectID, WorkspaceID: *workspaceID, OccurredAt: time.Now(), Type: eventType,
		Actor:     eventstore.Actor{Kind: "agent", AgentKind: kind, NativeSessionID: input.SessionID, ACRSessionID: os.Getenv("ACR_LAUNCH_ID")},
		Causality: eventstore.Causality{NativeTurnID: input.TurnID},
		Payload:   payload,
	}); err != nil {
		return writePostHookWarning("ACR could not record "+input.HookEventName+": "+err.Error(), stdout, stderr)
	}
	if input.HookEventName == "SessionEnd" {
		if *root == "" {
			*root = projectRootFrom(input.CWD)
		}
		if err := reconcileWorkspaceCoverage(ctx, store, *root, *projectID, *workspaceID, kind); err != nil {
			fmt.Fprintln(stderr, "ACR coverage warning at session end: "+err.Error())
		}
	}
	fmt.Fprintln(stdout, "{}")
	return 0
}
