package contextpack

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/memory"
	"github.com/agent-contract-runtime/acr/internal/protocol"
	"github.com/agent-contract-runtime/acr/internal/registry"
)

const (
	EventContextDelivered = "context.delivered"
	maximumCorrections    = 20
)

// Corrections are memory changes a running session has not been told about.
type Corrections struct {
	Text string
	// Keys identify the delivered revisions ("<memory_id>@<revision>").
	Keys []string
}

// MemoryCorrections returns notices for memories superseded, retracted,
// contested or marked stale after the session's first recorded event and not
// yet delivered to it. A session ACR has no events for gets nothing, because
// its start is unknown.
func MemoryCorrections(ctx context.Context, events *eventstore.Store, memories *memory.Store, projectID, sessionID string) (Corrections, error) {
	if sessionID == "" {
		return Corrections{}, nil
	}
	history, err := sessionHistory(ctx, events, projectID, sessionID)
	if err != nil || !history.found {
		return Corrections{}, err
	}
	started, delivered := history.started, history.delivered
	revisions, err := memories.RevisionsSince(ctx, projectID, started, maximumCorrections, noticeActions)
	if err != nil {
		return Corrections{}, err
	}
	lines := []string{}
	keys := []string{}
	for _, rule := range history.rules {
		if delivered[rule.key] || len(lines) == maximumCorrections {
			continue
		}
		lines = append(lines, rule.line+"\n")
		keys = append(keys, rule.key)
	}
	for _, revision := range revisions {
		value := revision.Memory
		key := fmt.Sprintf("%s@%d", value.MemoryID, value.Revision)
		if delivered[key] || history.ownChange(value.EvidenceRefs) {
			continue
		}
		value.Claim = clipClaim(value.Claim)
		line := ""
		switch revision.Action {
		case memory.ActionSupersede:
			replacement := "a newer memory"
			if current, err := memories.Get(ctx, projectID, revision.RelatedMemoryID); err == nil {
				replacement = fmt.Sprintf("%s: %q", current.MemoryID, clipClaim(current.Claim))
			}
			line = fmt.Sprintf("- [corrected] %s %q is wrong; use %s instead", value.MemoryID, value.Claim, replacement)
		case memory.ActionRetract:
			line = fmt.Sprintf("- [retracted] %s %q is wrong; do not rely on it", value.MemoryID, value.Claim)
		case memory.ActionContest:
			line = fmt.Sprintf("- [contested] %s %q has conflicting evidence; treat it as uncertain", value.MemoryID, value.Claim)
		case memory.ActionMarkStale:
			line = fmt.Sprintf("- [stale] %s %q may be outdated; verify before relying on it", value.MemoryID, value.Claim)
		case memory.ActionActivate:
			line = fmt.Sprintf("- [new] %s %q now applies to this project", value.MemoryID, value.Claim)
		default:
			continue
		}
		if revision.Reason != "" {
			line += " (reason: " + revision.Reason + ")"
		}
		lines = append(lines, line+"\n")
		keys = append(keys, key)
	}
	if len(lines) == 0 {
		return Corrections{}, nil
	}
	text := "# ACR memory corrections\n\nThese memories and rules changed after this session started. They override anything you were told about them earlier.\n\n" + strings.Join(lines, "")
	return Corrections{Text: text, Keys: keys}, nil
}

// clipClaim keeps notices short enough to be delivered whole.
func clipClaim(claim string) string {
	if len(claim) <= 300 {
		return claim
	}
	return bounded(claim, 297) + "…"
}

// noticeActions are the memory changes a running session is told about: a
// memory that became unsafe to rely on, or one that newly applies.
var noticeActions = []memory.Action{memory.ActionSupersede, memory.ActionRetract, memory.ActionContest, memory.ActionMarkStale, memory.ActionActivate}

type ruleNotice struct{ key, line string }

type history struct {
	started   time.Time
	found     bool
	delivered map[string]bool
	// own are the session's prompts; a change quoting one came from this
	// session, which already knows about it.
	own   map[string]bool
	rules []ruleNotice
}

func (value history) ownChange(evidence []string) bool {
	for _, ref := range evidence {
		if value.own[strings.TrimPrefix(ref, "event:")] {
			return true
		}
	}
	return false
}

// sessionHistory finds when ACR first saw the session, which memory
// revisions and rule changes were already delivered to it, and the rule
// changes since it started.
func sessionHistory(ctx context.Context, events *eventstore.Store, projectID, sessionID string) (history, error) {
	result := history{delivered: map[string]bool{}, own: map[string]bool{}}
	opening, err := events.ListMatching(ctx, projectID, 0, 1, eventstore.Filter{NativeSessionID: sessionID})
	if err != nil || len(opening) == 0 {
		return result, wrapHistory(err)
	}
	first := opening[0].Sequence
	result.started, result.found = opening[0].RecordedAt, true
	err = events.Each(ctx, projectID, 0, eventstore.Filter{NativeSessionID: sessionID, Types: []string{"intent.observed", EventContextDelivered}}, func(event eventstore.Event) error {
		if event.Actor.Kind == "user" {
			result.own[event.EventID] = true
		}
		if event.Type != EventContextDelivered {
			return nil
		}
		var payload struct {
			MemoryRevisions []string `json:"memory_revisions"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode %s %s: %w", event.Type, event.EventID, err)
		}
		for _, key := range payload.MemoryRevisions {
			result.delivered[key] = true
		}
		return nil
	})
	if err != nil {
		return result, wrapHistory(err)
	}
	err = events.Each(ctx, projectID, first, eventstore.Filter{Types: []string{registry.EventActivated, registry.EventRetired}}, func(event eventstore.Event) error {
		if notice, ok := ruleChange(event); ok && !result.ownChange(event.EvidenceRefs) {
			result.rules = append(result.rules, notice)
		}
		return nil
	})
	return result, wrapHistory(err)
}

func wrapHistory(err error) error {
	if err != nil {
		return fmt.Errorf("read session events: %w", err)
	}
	return nil
}

// ruleChange describes a contract activation or retirement for the agent.
// Importing the contracts already in force changes nothing it needs to know.
func ruleChange(event eventstore.Event) (ruleNotice, bool) {
	var payload struct {
		Contract   contract.Contract `json:"contract"`
		ContractID string            `json:"contract_id"`
		Source     registry.Source   `json:"source"`
	}
	if json.Unmarshal(event.Payload, &payload) != nil || payload.Source.Kind == registry.SourceLegacyImport {
		return ruleNotice{}, false
	}
	key := "rule:" + event.EventID
	if event.Type == registry.EventRetired {
		return ruleNotice{key, fmt.Sprintf("- [rule removed] %s no longer applies", payload.ContractID)}, true
	}
	label := "new rule"
	if payload.Contract.Metadata.Version > 1 {
		label = "rule updated"
	}
	return ruleNotice{key, fmt.Sprintf("- [%s] %s: %s", label, payload.Contract.Metadata.ID, clipClaim(payload.Contract.Behavior.Instruction))}, true
}

// RecordDelivery appends context.delivered so the same corrections are not
// repeated to the session.
func RecordDelivery(ctx context.Context, events *eventstore.Store, projectID, workspaceID string, agent protocol.AgentKind, sessionID, hook string, corrections Corrections) error {
	if len(corrections.Keys) == 0 {
		return nil
	}
	payload, err := json.Marshal(map[string]any{"hook": hook, "memory_revisions": corrections.Keys})
	if err != nil {
		return err
	}
	_, err = events.Append(ctx, eventstore.AppendRequest{
		ProjectID: projectID, WorkspaceID: workspaceID, OccurredAt: time.Now(), Type: EventContextDelivered,
		Actor:   eventstore.Actor{Kind: "runtime", AgentKind: agent, NativeSessionID: sessionID},
		Payload: payload,
	})
	if err != nil {
		return fmt.Errorf("record context delivery: %w", err)
	}
	return nil
}
