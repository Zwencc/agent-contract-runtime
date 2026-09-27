// Package directive carries out what the user asks an agent to remember or
// enforce. The agent calls these tools with the user's exact words; ACR
// checks that the words are in a prompt the user sent, records the change
// with that prompt as evidence, and only then lets it take effect. What the
// agent decides to remember on its own stays a candidate until the user
// confirms it.
package directive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/guarded"
	"github.com/agent-contract-runtime/acr/internal/memory"
	"github.com/agent-contract-runtime/acr/internal/protocol"
	"github.com/agent-contract-runtime/acr/internal/registry"
)

const (
	EventAuthorizationGranted = "authorization.granted"
	eventUserIntent           = "intent.observed"

	minimumQuoteRunes = 4
	// A remembered fact may come from earlier in the conversation; a
	// permission must be fresh.
	memoryQuoteAge        = 24 * time.Hour
	authorizationQuoteAge = time.Hour
)

const instructions = "ACR keeps the project's memories and rules for every agent session. " +
	"When the user asks you to remember something, change or forget a memory, or add or remove a rule, call the matching tool with the user's exact words in quote; " +
	"ACR checks the words against the user's messages, and the change takes effect for all sessions. " +
	"If you want to remember something the user did not ask for, call remember without quote: it stays a candidate until the user agrees and you call confirm_memory with their words. " +
	"Never invent or paraphrase a quote."

// Service implements the directive tools for one project.
type Service struct {
	Events       *eventstore.Store
	Memories     *memory.Store
	ProjectID    string
	WorkspaceID  string
	ContractsDir string
	Agent        protocol.AgentKind
	// LaunchID, when set, limits quotes to prompts of that acr launch.
	LaunchID string
	Now      func() time.Time
}

func (service Service) now() time.Time {
	if service.Now != nil {
		return service.Now()
	}
	return time.Now()
}

// Instructions tells the agent when to use the tools.
func (service Service) Instructions() string { return instructions }

// Tools lists the directive tools in MCP form.
func (service Service) Tools() []map[string]any {
	object := func(required []string, properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	quote := text("The user's exact words that ask for this, copied from their message")
	return []map[string]any{
		{"name": "remember", "description": "Remember something for all future agent sessions of this project. With quote it takes effect now; without, it is a candidate until the user confirms it.",
			"inputSchema": object([]string{"claim"}, map[string]any{
				"claim": text("What to remember, as one self-contained statement"), "quote": quote,
				"kind":     text("decision, constraint, preference, fact or procedure; default constraint"),
				"priority": text("P0 for safety rules and hard constraints the user set, P1 (default) for other instructions, up to P6 for minor notes"),
			})},
		{"name": "confirm_memory", "description": "Make a candidate memory take effect after the user agreed to it.",
			"inputSchema": object([]string{"memory_id", "quote"}, map[string]any{"memory_id": text("Memory ID"), "quote": quote})},
		{"name": "correct_memory", "description": "Replace a memory the user says is wrong or outdated with a corrected statement.",
			"inputSchema": object([]string{"memory_id", "claim", "quote"}, map[string]any{"memory_id": text("Memory ID"), "claim": text("Corrected statement"), "quote": quote, "reason": text("Why it changed")})},
		{"name": "forget_memory", "description": "Stop using a memory the user says is wrong or no longer applies.",
			"inputSchema": object([]string{"memory_id", "quote"}, map[string]any{"memory_id": text("Memory ID"), "quote": quote, "reason": text("Why")})},
		{"name": "list_memories", "description": "List the memories in effect, with their IDs.",
			"inputSchema": object([]string{}, map[string]any{})},
		{"name": "add_rule", "description": "Enforce a rule the user asked for on every agent's tool calls, as an ACR contract (acr.dev/v1alpha1 JSON). Use correction mode deny or ask to block, rewrite to correct, and authorization {\"user_in_turn\": true, \"mentions\": [...]} when the user said the action is allowed once they explicitly permit it.",
			"inputSchema": object([]string{"contract", "quote"}, map[string]any{"contract": map[string]any{"type": "object", "description": "The contract"}, "quote": quote})},
		{"name": "retire_rule", "description": "Stop enforcing a rule the user no longer wants.",
			"inputSchema": object([]string{"contract_id", "quote"}, map[string]any{"contract_id": text("Contract ID"), "quote": quote, "reason": text("Why")})},
		{"name": "list_rules", "description": "List the rules in force.",
			"inputSchema": object([]string{}, map[string]any{})},
		{"name": "authorize", "description": "Lift a rule for the rest of the current turn because the user's latest message explicitly allows the action it blocks. Only when the user really said so.",
			"inputSchema": object([]string{"contract_id", "quote"}, map[string]any{"contract_id": text("Contract ID from the refusal"), "quote": quote})},
	}
}

type arguments struct {
	Claim      string          `json:"claim"`
	Quote      string          `json:"quote"`
	Kind       string          `json:"kind"`
	Priority   string          `json:"priority"`
	MemoryID   string          `json:"memory_id"`
	Reason     string          `json:"reason"`
	Contract   json.RawMessage `json:"contract"`
	ContractID string          `json:"contract_id"`
}

// Call runs one tool.
func (service Service) Call(ctx context.Context, name string, raw json.RawMessage) guarded.Result {
	var input arguments
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return failure("invalid arguments: %v", err)
	}
	text, err := service.call(ctx, name, input)
	if err != nil {
		return failure("%v", err)
	}
	return guarded.Result{Text: text}
}

func (service Service) call(ctx context.Context, name string, input arguments) (string, error) {
	needsMemories := strings.Contains(name, "memor") || name == "remember"
	if needsMemories && service.Memories == nil {
		return "", errors.New("the memory store is not available")
	}
	switch name {
	case "remember":
		return service.remember(ctx, input)
	case "confirm_memory":
		prompt, err := service.findQuote(ctx, input.Quote, memoryQuoteAge)
		if err != nil {
			return "", err
		}
		value, err := service.Memories.Activate(ctx, service.ProjectID, input.MemoryID, "user confirmed: "+quoted(input.Quote)+" (event:"+prompt.EventID+")")
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("memory %s is now in effect: %s", value.MemoryID, value.Claim), nil
	case "correct_memory":
		prompt, err := service.findQuote(ctx, input.Quote, memoryQuoteAge)
		if err != nil {
			return "", err
		}
		reason := input.Reason
		if reason == "" {
			reason = "user said: " + quoted(input.Quote)
		}
		value, err := service.Memories.Correct(ctx, service.ProjectID, input.MemoryID, memory.CorrectRequest{
			Claim: input.Claim, EvidenceRefs: []string{"event:" + prompt.EventID}, Source: "user", Confidence: 1, Reason: reason,
		})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("memory %s replaces %s: %s", value.MemoryID, input.MemoryID, value.Claim), nil
	case "forget_memory":
		prompt, err := service.findQuote(ctx, input.Quote, memoryQuoteAge)
		if err != nil {
			return "", err
		}
		reason := strings.TrimSpace(input.Reason + " (user said: " + quoted(input.Quote) + ", event:" + prompt.EventID + ")")
		if _, err := service.Memories.Retract(ctx, service.ProjectID, input.MemoryID, reason); err != nil {
			return "", err
		}
		return fmt.Sprintf("memory %s is retracted and no longer used", input.MemoryID), nil
	case "list_memories":
		values, err := service.Memories.Retrieve(ctx, memory.Query{ProjectID: service.ProjectID, WorkspaceID: service.WorkspaceID, Limit: 200})
		if err != nil {
			return "", err
		}
		if len(values) == 0 {
			return "no memories are in effect", nil
		}
		lines := []string{}
		for _, value := range values {
			lines = append(lines, fmt.Sprintf("%s [%s %s %s] %s", value.MemoryID, value.Priority, value.Kind, value.Status, value.Claim))
		}
		return strings.Join(lines, "\n"), nil
	case "add_rule":
		return service.addRule(ctx, input)
	case "retire_rule":
		prompt, err := service.findQuote(ctx, input.Quote, memoryQuoteAge)
		if err != nil {
			return "", err
		}
		reason := input.Reason
		if reason == "" {
			reason = "user asked"
		}
		if err := registry.Retire(ctx, service.Events, service.ProjectID, service.WorkspaceID, service.ContractsDir, input.ContractID, reason, service.source(prompt, input.Quote), service.now()); err != nil {
			return "", err
		}
		return fmt.Sprintf("rule %s is no longer enforced", input.ContractID), nil
	case "list_rules":
		values, err := registry.Load(ctx, service.Events, service.ProjectID, service.ContractsDir)
		if err != nil {
			return "", err
		}
		lines := []string{}
		for _, value := range values {
			if value.Metadata.Status == contract.StatusActive {
				lines = append(lines, fmt.Sprintf("%s v%d: %s", value.Metadata.ID, value.Metadata.Version, value.Behavior.Instruction))
			}
		}
		return strings.Join(lines, "\n"), nil
	case "authorize":
		return service.authorize(ctx, input)
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

func (service Service) remember(ctx context.Context, input arguments) (string, error) {
	kind := input.Kind
	if kind == "" {
		kind = "constraint"
	}
	priority := memory.P1
	if input.Priority != "" {
		if len(input.Priority) != 2 || input.Priority[0] != 'P' || input.Priority[1] < '0' || input.Priority[1] > '6' {
			return "", fmt.Errorf("priority %q must be P0 to P6", input.Priority)
		}
		priority = memory.Priority(input.Priority[1] - '0')
	}
	request := memory.CreateRequest{ProjectID: service.ProjectID, Kind: kind, Priority: priority, Claim: input.Claim, Scope: json.RawMessage(`{}`)}
	var prompt eventstore.Event
	if strings.TrimSpace(input.Quote) != "" {
		var err error
		if prompt, err = service.findQuote(ctx, input.Quote, memoryQuoteAge); err != nil {
			return "", err
		}
		request.EvidenceRefs, request.Source, request.Confidence = []string{"event:" + prompt.EventID}, "user", 1
	} else {
		request.EvidenceRefs, request.Source, request.Confidence = []string{"agent:" + string(service.Agent)}, "agent", 0.5
	}
	value, err := service.Memories.CreateCandidate(ctx, request)
	if err != nil {
		return "", err
	}
	if prompt.EventID == "" {
		return fmt.Sprintf("candidate memory %s recorded; it takes effect only after the user agrees and you call confirm_memory with their words", value.MemoryID), nil
	}
	if value, err = service.Memories.Activate(ctx, service.ProjectID, value.MemoryID, "user asked: "+quoted(input.Quote)); err != nil {
		return "", err
	}
	return fmt.Sprintf("memory %s is in effect for all sessions: %s", value.MemoryID, value.Claim), nil
}

func (service Service) addRule(ctx context.Context, input arguments) (string, error) {
	prompt, err := service.findQuote(ctx, input.Quote, memoryQuoteAge)
	if err != nil {
		return "", err
	}
	value, err := contract.DecodeBytes(input.Contract)
	if err != nil {
		return "", fmt.Errorf("the rule is not a valid contract: %w", err)
	}
	if strings.HasPrefix(value.Metadata.ID, "acr-builtin-") {
		return "", errors.New("built-in rules cannot be replaced")
	}
	entry, err := registry.Activate(ctx, service.Events, service.ProjectID, service.WorkspaceID, service.ContractsDir, value, service.source(prompt, input.Quote), service.now())
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("rule %s version %d is enforced for every agent from the next tool call: %s", entry.Contract.Metadata.ID, entry.Contract.Metadata.Version, entry.Contract.Behavior.Instruction), nil
}

func (service Service) source(prompt eventstore.Event, quote string) registry.Source {
	return registry.Source{Kind: registry.SourceUserInstruction, QuoteEventID: prompt.EventID, Quote: quote}
}

func failure(format string, values ...any) guarded.Result {
	return guarded.Result{Text: fmt.Sprintf(format, values...), IsError: true}
}

func quoted(value string) string {
	return fmt.Sprintf("%q", normalize(value))
}

func normalize(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
