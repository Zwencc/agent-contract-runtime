package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/agent-contract-runtime/acr/internal/adapter"
	"github.com/agent-contract-runtime/acr/internal/contextpack"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/protocol"
	"github.com/agent-contract-runtime/acr/internal/redaction"
)

const maximumMentionedPaths = 20

// runUserPromptSubmitHook records the prompt as intent.observed and, when it
// names existing project paths, adds the verification status and failure
// patterns recorded for them. Prompts without paths get no extra context.
func runUserPromptSubmitHook(agentName string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	kind := protocol.AgentCodex
	switch agentName {
	case "codex":
	case "claude":
		kind = protocol.AgentClaudeCode
	default:
		fmt.Fprintf(stderr, "unsupported hook agent %q; want codex or claude\n", agentName)
		return 2
	}
	flags := flag.NewFlagSet("hook "+agentName+" user-prompt-submit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	eventDB := flags.String("event-db", "", "SQLite event database path")
	projectID := flags.String("project-id", "", "ACR project id")
	workspaceID := flags.String("workspace-id", "", "ACR workspace id")
	root := flags.String("root", "", "project root; defaults to the hook cwd")
	projectionDB := flags.String("projection-db", "", "projection database; defaults to projections.db next to --event-db")
	memoryDB := flags.String("memory-db", "", "memory database; defaults to memories.db next to --event-db")
	maximumCharacters := flags.Int("max-chars", 3000, "maximum context characters")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *eventDB == "" || *projectID == "" {
		fmt.Fprintln(stderr, "user-prompt-submit requires --event-db and --project-id")
		return 2
	}
	raw, err := readHookInput(stdin)
	if err != nil {
		return writePostHookWarning("ACR could not read UserPromptSubmit input: "+err.Error(), stdout, stderr)
	}
	input, err := adapter.DecodeUserPromptSubmit(raw)
	if err != nil {
		return writePostHookWarning("ACR rejected UserPromptSubmit input: "+err.Error(), stdout, stderr)
	}
	if *root == "" {
		*root = input.CWD
	}
	store, release, err := openEvents(*eventDB)
	if err != nil {
		return writePostHookWarning("ACR could not open event store: "+err.Error(), stdout, stderr)
	}
	defer release()

	// Claude Code also submits background task notifications as prompts; they
	// are host messages, not the user's words, and never count as an
	// instruction from the user.
	eventType, actor := "intent.observed", "user"
	if strings.HasPrefix(strings.TrimSpace(input.Prompt), "<task-notification>") {
		eventType, actor = "host.notification", "host"
	}
	paths := mentionedPaths(*root, input.Prompt)
	payload, err := json.Marshal(map[string]any{"prompt": input.Prompt, "mentioned_paths": paths, "source": "UserPromptSubmit"})
	if err == nil {
		payload, err = redaction.JSON(payload)
	}
	if err != nil {
		return writePostHookWarning("ACR could not encode prompt: "+err.Error(), stdout, stderr)
	}
	if _, err := store.Append(context.Background(), eventstore.AppendRequest{
		ProjectID:   *projectID,
		WorkspaceID: *workspaceID,
		OccurredAt:  time.Now(),
		Type:        eventType,
		Actor:       eventstore.Actor{Kind: actor, AgentKind: kind, NativeSessionID: input.SessionID, ACRSessionID: os.Getenv("ACR_LAUNCH_ID")},
		Causality:   eventstore.Causality{NativeTurnID: input.TurnID},
		Payload:     payload,
	}); err != nil {
		return writePostHookWarning("ACR could not record prompt: "+err.Error(), stdout, stderr)
	}
	corrections, err := sessionCorrections(store, *eventDB, *memoryDB, *projectID, input.SessionID)
	if err != nil {
		fmt.Fprintln(stderr, "ACR memory correction warning: "+err.Error())
	}
	if len(paths) == 0 && corrections.Text == "" {
		fmt.Fprintln(stdout, "{}")
		return 0
	}
	text := corrections.Text
	if len(paths) > 0 {
		relevant, err := promptPathContext(store, *eventDB, *projectionDB, *projectID, *root, paths, *maximumCharacters)
		if err != nil {
			return writePostHookWarning(err.Error(), stdout, stderr)
		}
		if text != "" {
			text += "\n"
		}
		text += relevant
	}
	encoded, err := adapter.EncodeHookContext("UserPromptSubmit", text)
	if err != nil {
		return writePostHookWarning("ACR could not encode prompt context: "+err.Error(), stdout, stderr)
	}
	fmt.Fprintln(stdout, string(encoded))
	if err := contextpack.RecordDelivery(context.Background(), store, *projectID, *workspaceID, kind, input.SessionID, "UserPromptSubmit", corrections); err != nil {
		fmt.Fprintln(stderr, err)
	}
	return 0
}

// sessionCorrections opens the memory store when it exists and returns the
// corrections the session has not been told about. Hooks never create it.
func sessionCorrections(store *eventstore.Store, eventDB, memoryDB, projectID, sessionID string) (contextpack.Corrections, error) {
	if memoryDB == "" {
		memoryDB = filepath.Join(filepath.Dir(eventDB), "memories.db")
	}
	if _, err := os.Stat(memoryDB); err != nil {
		return contextpack.Corrections{}, nil
	}
	memories, release, err := openMemories(memoryDB)
	if err != nil {
		return contextpack.Corrections{}, err
	}
	defer release()
	return contextpack.MemoryCorrections(context.Background(), store, memories, projectID, sessionID)
}

func promptPathContext(store *eventstore.Store, eventDB, projectionDB, projectID, root string, paths []string, maximum int) (string, error) {
	if projectionDB == "" {
		projectionDB = filepath.Join(filepath.Dir(eventDB), "projections.db")
	}
	projections, release, err := openProjections(projectionDB)
	if err != nil {
		return "", fmt.Errorf("ACR could not open project state: %w", err)
	}
	defer release()
	text, err := contextpack.BuildRelevant(context.Background(), store, projections, projectID, root, paths, maximum)
	if err != nil {
		return "", fmt.Errorf("ACR could not build prompt context: %w", err)
	}
	return text, nil
}

// mentionedPaths returns the project-relative paths named in prompt that
// exist inside root, resolving symlinks so nothing outside root is reported.
func mentionedPaths(root, prompt string) []string {
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil
	}
	separators := "\"'`()[]{}<>,;|，。；：、（）《》“”‘’"
	tokens := strings.FieldsFunc(prompt, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(separators, r) })
	seen := map[string]bool{}
	paths := []string{}
	for _, token := range tokens {
		token = strings.TrimRight(token, ".:!?")
		if token == "" || token == "." || strings.Contains(token, "://") {
			continue
		}
		candidate := token
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(base, candidate)
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(base, resolved)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if _, err := os.Stat(resolved); err != nil {
			continue
		}
		relative = filepath.ToSlash(relative)
		if seen[relative] || strings.HasPrefix(relative, ".acr/") || relative == ".acr" {
			continue
		}
		seen[relative] = true
		paths = append(paths, relative)
		if len(paths) == maximumMentionedPaths {
			break
		}
	}
	return paths
}
