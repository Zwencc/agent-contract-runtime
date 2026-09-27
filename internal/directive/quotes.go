package directive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/protocol"
	"github.com/agent-contract-runtime/acr/internal/registry"
)

// findQuote returns the most recent prompt the user sent, at most maxAge
// ago, that contains quote. Only prompts recorded as the user's own count:
// file contents, tool output and host notifications never do.
func (service Service) findQuote(ctx context.Context, quote string, maxAge time.Duration) (eventstore.Event, error) {
	wanted := normalize(quote)
	if utf8.RuneCountInString(wanted) < minimumQuoteRunes {
		return eventstore.Event{}, fmt.Errorf("quote the user's words (at least %d characters) that ask for this", minimumQuoteRunes)
	}
	oldest := service.now().Add(-maxAge)
	var found eventstore.Event
	err := service.Events.Each(ctx, service.ProjectID, 0, eventstore.Filter{Types: []string{eventUserIntent}, ACRSessionID: service.LaunchID}, func(event eventstore.Event) error {
		if event.Actor.Kind == "user" && !event.RecordedAt.Before(oldest) && strings.Contains(normalize(promptOf(event)), wanted) {
			found = event
		}
		return nil
	})
	if err != nil {
		return eventstore.Event{}, err
	}
	if found.EventID == "" {
		return eventstore.Event{}, errors.New("these words are not in any recent message from the user; quote their exact words, or ask them first")
	}
	return found, nil
}

func (service Service) authorize(ctx context.Context, input arguments) (string, error) {
	values, err := registry.Load(ctx, service.Events, service.ProjectID, service.ContractsDir)
	if err != nil {
		return "", err
	}
	var mentions []string
	liftable := false
	for _, value := range values {
		if value.Metadata.ID == input.ContractID && value.Authorization != nil && value.Authorization.UserInTurn {
			liftable, mentions = true, value.Authorization.Mentions
		}
	}
	if !liftable {
		return "", fmt.Errorf("rule %s cannot be lifted by the user for a turn", input.ContractID)
	}
	prompt, err := service.findQuote(ctx, input.Quote, authorizationQuoteAge)
	if err != nil {
		return "", err
	}
	latest, err := latestUserPrompt(ctx, service.Events, service.ProjectID, eventstore.Filter{NativeSessionID: prompt.Actor.NativeSessionID})
	if err != nil {
		return "", err
	}
	if latest != prompt.EventID {
		return "", errors.New("a permission only counts in the turn it was given; these words are from an earlier message, so ask the user again")
	}
	if len(mentions) > 0 {
		lower := strings.ToLower(normalize(input.Quote))
		named := false
		for _, mention := range mentions {
			named = named || strings.Contains(lower, strings.ToLower(mention))
		}
		if !named {
			return "", fmt.Errorf("the quoted permission must name what it allows (one of %q)", mentions)
		}
	}
	payload, _ := json.Marshal(map[string]string{
		"contract_id": input.ContractID, "quote": normalize(input.Quote), "quote_event_id": prompt.EventID,
		"native_session_id": prompt.Actor.NativeSessionID, "acr_session_id": prompt.Actor.ACRSessionID,
	})
	if _, err := service.Events.Append(ctx, eventstore.AppendRequest{ProjectID: service.ProjectID, WorkspaceID: service.WorkspaceID, OccurredAt: service.now(),
		Type: EventAuthorizationGranted, Actor: eventstore.Actor{Kind: "user", AgentKind: service.Agent, NativeSessionID: prompt.Actor.NativeSessionID, ACRSessionID: prompt.Actor.ACRSessionID},
		Payload: payload, EvidenceRefs: []string{"event:" + prompt.EventID}}); err != nil {
		return "", err
	}
	return fmt.Sprintf("rule %s is lifted until the user's next message; retry the action now", input.ContractID), nil
}

// Grants answers whether the user lifted a contract for the current turn: a
// grant counts while the prompt it quotes is still the user's latest in the
// session the action belongs to.
type Grants struct {
	Events    *eventstore.Store
	ProjectID string
}

func (grants Grants) Authorized(action protocol.Action, contractID string) (bool, error) {
	ctx := context.Background()
	type grant struct{ quoteEventID, native, launch string }
	found := []grant{}
	err := grants.Events.Each(ctx, grants.ProjectID, 0, eventstore.Filter{Types: []string{EventAuthorizationGranted}}, func(event eventstore.Event) error {
		var payload struct {
			ContractID   string `json:"contract_id"`
			QuoteEventID string `json:"quote_event_id"`
		}
		if json.Unmarshal(event.Payload, &payload) == nil && payload.ContractID == contractID {
			found = append(found, grant{payload.QuoteEventID, event.Actor.NativeSessionID, event.Actor.ACRSessionID})
		}
		return nil
	})
	if err != nil || len(found) == 0 {
		return false, err
	}
	// A hook reports the agent's own session; the guarded server knows only
	// the launch.
	latestNative, latestLaunch := "", ""
	if action.Agent.NativeSessionID != "" {
		if latestNative, err = latestUserPrompt(ctx, grants.Events, grants.ProjectID, eventstore.Filter{NativeSessionID: action.Agent.NativeSessionID}); err != nil {
			return false, err
		}
	}
	if action.Agent.ACRSessionID != "" {
		if latestLaunch, err = latestUserPrompt(ctx, grants.Events, grants.ProjectID, eventstore.Filter{ACRSessionID: action.Agent.ACRSessionID}); err != nil {
			return false, err
		}
	}
	for _, item := range found {
		if item.native != "" && action.Agent.NativeSessionID == item.native && latestNative == item.quoteEventID {
			return true, nil
		}
		if item.launch != "" && action.Agent.ACRSessionID == item.launch && latestLaunch == item.quoteEventID {
			return true, nil
		}
	}
	return false, nil
}

// latestUserPrompt returns the ID of the last prompt the user sent in the
// session filter selects.
func latestUserPrompt(ctx context.Context, store *eventstore.Store, projectID string, filter eventstore.Filter) (string, error) {
	filter.Types = []string{eventUserIntent}
	latest := ""
	err := store.Each(ctx, projectID, 0, filter, func(event eventstore.Event) error {
		if event.Actor.Kind == "user" {
			latest = event.EventID
		}
		return nil
	})
	return latest, err
}

func promptOf(event eventstore.Event) string {
	var payload struct {
		Prompt string `json:"prompt"`
	}
	json.Unmarshal(event.Payload, &payload)
	return payload.Prompt
}
