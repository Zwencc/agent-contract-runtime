// Package distill proposes reviewable contract candidates from deterministic
// repeated-failure patterns. Candidates are drafts: they cite every source
// event, state their assumptions and risks, and never become active without a
// separate human approval.
package distill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/projector"
	"github.com/agent-contract-runtime/acr/internal/protocol"
)

const (
	EventProposed = "candidate.proposed"
	EventRejected = "candidate.rejected"
	EventAccepted = "candidate.accepted"

	DefaultMinimumOccurrences = 2
)

var ErrNotFound = errors.New("candidate not found")

// Case is one simulation example: whether the candidate contract should
// match the action.
type Case struct {
	Name        string          `json:"name"`
	Action      protocol.Action `json:"action"`
	ExpectMatch bool            `json:"expect_match"`
}

type Candidate struct {
	CandidateID        string            `json:"candidate_id"`
	FailureNodeID      string            `json:"failure_node_id"`
	EvidenceKey        string            `json:"evidence_key"`
	SourceEventIDs     []string          `json:"source_event_ids"`
	Occurrences        int               `json:"occurrences"`
	Contract           contract.Contract `json:"contract"`
	Assumptions        []string          `json:"assumptions"`
	Ambiguities        []string          `json:"ambiguities"`
	FalsePositiveRisks []string          `json:"false_positive_risks"`
	Cases              []Case            `json:"cases"`
}

type State string

const (
	StateProposed State = "proposed"
	StateRejected State = "rejected"
	StateAccepted State = "accepted"
)

type Record struct {
	Candidate Candidate `json:"candidate"`
	State     State     `json:"state"`
	Reason    string    `json:"reason,omitempty"`
}

// List folds candidate events into the current record of every candidate,
// ordered by candidate id.
func List(ctx context.Context, store *eventstore.Store, projectID string) ([]Record, error) {
	records := map[string]*Record{}
	for after := uint64(0); ; {
		events, err := store.List(ctx, projectID, after, 500)
		if err != nil {
			return nil, fmt.Errorf("read candidate events: %w", err)
		}
		if len(events) == 0 {
			break
		}
		for _, event := range events {
			after = event.Sequence
			switch event.Type {
			case EventProposed:
				var candidate Candidate
				if err := json.Unmarshal(event.Payload, &candidate); err != nil {
					return nil, fmt.Errorf("decode %s %s: %w", event.Type, event.EventID, err)
				}
				records[candidate.CandidateID] = &Record{Candidate: candidate, State: StateProposed}
			case EventRejected, EventAccepted:
				var decision struct {
					CandidateID string `json:"candidate_id"`
					Reason      string `json:"reason"`
				}
				if err := json.Unmarshal(event.Payload, &decision); err != nil {
					return nil, fmt.Errorf("decode %s %s: %w", event.Type, event.EventID, err)
				}
				if record, ok := records[decision.CandidateID]; ok {
					record.State, record.Reason = StateAccepted, decision.Reason
					if event.Type == EventRejected {
						record.State = StateRejected
					}
				}
			}
		}
	}
	result := make([]Record, 0, len(records))
	for _, record := range records {
		result = append(result, *record)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Candidate.CandidateID < result[j].Candidate.CandidateID })
	return result, nil
}

// Propose records a candidate for every agent-tool failure pattern seen at
// least minimum times, unless the same evidence was already proposed or
// rejected, or the pattern already produced an accepted candidate.
func Propose(ctx context.Context, store *eventstore.Store, projections *projector.Store, projectID, root string, minimum int, now time.Time) ([]Candidate, error) {
	if minimum < 2 {
		return nil, errors.New("candidates need at least two occurrences of a failure")
	}
	if _, err := projections.Sync(ctx, store, projectID, root); err != nil {
		return nil, err
	}
	nodes, err := projections.Nodes(ctx, projectID)
	if err != nil {
		return nil, err
	}
	existing, err := List(ctx, store, projectID)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	accepted := map[string]bool{}
	for _, record := range existing {
		known[record.Candidate.CandidateID] = true
		if record.State == StateAccepted {
			accepted[record.Candidate.FailureNodeID] = true
		}
	}

	proposed := []Candidate{}
	for _, node := range nodes {
		if node.Type != projector.NodeFailure || accepted[node.ID] {
			continue
		}
		var attributes projector.FailureAttributes
		if err := json.Unmarshal(node.Attributes, &attributes); err != nil {
			return nil, fmt.Errorf("decode failure %s: %w", node.ID, err)
		}
		if attributes.Occurrences < minimum || attributes.Tool == "" || strings.HasPrefix(attributes.Tool, "repair:") {
			continue
		}
		evidence := append([]string{}, node.SourceEventIDs...)
		sort.Strings(evidence)
		evidenceSum := sha256.Sum256([]byte(node.ID + "\n" + strings.Join(evidence, "\n")))
		evidenceKey := hex.EncodeToString(evidenceSum[:])
		candidateID := "cand_" + evidenceKey[:16]
		if known[candidateID] {
			continue
		}
		sample, err := firstToolInput(ctx, store, projectID, node.SourceEventIDs[0])
		if err != nil {
			return nil, err
		}
		candidate := build(candidateID, node, attributes, evidence, evidenceKey, sample)
		if err := candidate.Contract.Validate(); err != nil {
			return nil, fmt.Errorf("candidate %s produced an invalid contract: %w", candidateID, err)
		}
		payload, err := json.Marshal(candidate)
		if err != nil {
			return nil, fmt.Errorf("encode candidate %s: %w", candidateID, err)
		}
		if _, err := store.Append(ctx, eventstore.AppendRequest{
			ProjectID: projectID, OccurredAt: now, Type: EventProposed,
			Actor:     eventstore.Actor{Kind: "runtime"},
			Causality: eventstore.Causality{CausationEventID: attributes.LastEventID},
			Payload:   payload,
		}); err != nil {
			return nil, fmt.Errorf("record candidate %s: %w", candidateID, err)
		}
		proposed = append(proposed, candidate)
	}
	return proposed, nil
}

// Decide records a rejection (with its reason) or an acceptance of a
// proposed candidate and returns the candidate.
func Decide(ctx context.Context, store *eventstore.Store, projectID, candidateID string, accept bool, reason string, now time.Time) (Candidate, error) {
	records, err := List(ctx, store, projectID)
	if err != nil {
		return Candidate{}, err
	}
	for _, record := range records {
		if record.Candidate.CandidateID != candidateID {
			continue
		}
		if record.State != StateProposed {
			return Candidate{}, fmt.Errorf("candidate %s is already %s", candidateID, record.State)
		}
		eventType := EventRejected
		if accept {
			eventType = EventAccepted
		} else if strings.TrimSpace(reason) == "" {
			return Candidate{}, errors.New("rejecting a candidate requires a reason")
		}
		payload, _ := json.Marshal(map[string]string{"candidate_id": candidateID, "reason": reason})
		if _, err := store.Append(ctx, eventstore.AppendRequest{
			ProjectID: projectID, OccurredAt: now, Type: eventType, Actor: eventstore.Actor{Kind: "user"}, Payload: payload,
		}); err != nil {
			return Candidate{}, fmt.Errorf("record %s: %w", eventType, err)
		}
		return record.Candidate, nil
	}
	return Candidate{}, fmt.Errorf("candidate %s: %w", candidateID, ErrNotFound)
}

type toolSample struct {
	field string
	input json.RawMessage
}

// firstToolInput reads the tool input of a failure's first source event to
// learn which input field carries the path.
func firstToolInput(ctx context.Context, store *eventstore.Store, projectID, eventID string) (toolSample, error) {
	for after := uint64(0); ; {
		events, err := store.List(ctx, projectID, after, 500)
		if err != nil {
			return toolSample{}, err
		}
		if len(events) == 0 {
			return toolSample{}, fmt.Errorf("source event %s not found", eventID)
		}
		for _, event := range events {
			after = event.Sequence
			if event.EventID != eventID {
				continue
			}
			var payload struct {
				ToolInput json.RawMessage `json:"tool_input"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return toolSample{}, fmt.Errorf("decode source event %s: %w", eventID, err)
			}
			var input map[string]any
			json.Unmarshal(payload.ToolInput, &input)
			sample := toolSample{input: payload.ToolInput}
			for _, field := range []string{"file_path", "path"} {
				if _, ok := input[field].(string); ok {
					sample.field = field
					break
				}
			}
			return sample, nil
		}
	}
}

func commonDirectory(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	common := path.Dir(paths[0])
	for _, value := range paths[1:] {
		for common != "." && !strings.HasPrefix(value, common+"/") {
			common = path.Dir(common)
		}
	}
	return common
}

func build(candidateID string, node projector.Node, attributes projector.FailureAttributes, evidence []string, evidenceKey string, sample toolSample) Candidate {
	conditions := []contract.Condition{{Field: "tool.name", Operator: contract.OperatorEquals, Value: mustJSON(attributes.Tool)}}
	assumptions := []string{
		"The failure depends on the tool and the files it touched, not on other inputs.",
		"Errors whose first line matches after normalizing digits and whitespace share one root cause.",
	}
	ambiguities := []string{}
	risks := []string{"Advisory only: matching calls still run and the agent receives the failure evidence as context."}

	target := ""
	switch {
	case len(attributes.Paths) == 0 || sample.field == "":
		ambiguities = append(ambiguities, "No file path was recorded, so the condition matches every "+attributes.Tool+" call.")
		risks = append(risks, "High: unrelated "+attributes.Tool+" calls will match; narrow the applicability before approving.")
	case len(attributes.Paths) == 1:
		target = attributes.Paths[0]
	default:
		target = commonDirectory(attributes.Paths)
		if target == "." {
			target = ""
			ambiguities = append(ambiguities, fmt.Sprintf("The failures touched %d paths with no common directory, so the condition matches every %s call.", len(attributes.Paths), attributes.Tool))
			risks = append(risks, "High: unrelated "+attributes.Tool+" calls will match; narrow the applicability before approving.")
		} else {
			target += "/"
			ambiguities = append(ambiguities, fmt.Sprintf("The failures touched %s; the condition covers their common directory %s.", strings.Join(attributes.Paths, ", "), target))
			risks = append(risks, "Medium: other files in "+target+" will also match.")
		}
	}
	if target != "" {
		conditions = append(conditions, contract.Condition{Field: "input." + sample.field, Operator: contract.OperatorContains, Value: mustJSON(target)})
		risks = append(risks, "Low: the condition matches any path containing "+target+", including the same path in other checkouts.")
	}

	title := attributes.Signature
	if len(title) > 80 {
		title = title[:80]
	}
	value := contract.Contract{
		APIVersion: contract.APIVersion,
		Kind:       "Contract",
		Metadata: contract.Metadata{
			ID: "candidate-" + strings.TrimPrefix(candidateID, "cand_"), Version: 1, Title: "Repeated failure: " + title,
			Status: contract.StatusDraft, Severity: "low", Priority: 100, Tags: []string{"candidate", "failure-pattern"},
		},
		Scope:         contract.Scope{ToolNames: []string{attributes.Tool}},
		Applicability: contract.Applicability{All: conditions},
		Behavior: contract.Behavior{
			Instruction: fmt.Sprintf("%s failed %d times here with %q; review the recorded evidence (events %s) before repeating the same approach.",
				attributes.Tool, attributes.Occurrences, attributes.Signature, strings.Join(evidence, ", ")),
			Enforcement: contract.EnforcementAdvisory,
		},
		Correction:  contract.Correction{Mode: contract.CorrectionNone},
		FailureMode: contract.FailureOpen,
	}

	matching := protocol.NewAction(protocol.AgentContext{Kind: protocol.AgentCodex, NativeSessionID: "simulation"},
		protocol.WorkspaceContext{CWD: "/workspace", ProjectID: "simulation"}, protocol.ToolCall{Name: attributes.Tool, Input: sample.input})
	if len(matching.Tool.Input) == 0 {
		matching.Tool.Input = json.RawMessage(`{}`)
	}
	otherTool := matching
	otherTool.Tool.Name = attributes.Tool + "_other"
	cases := []Case{
		{Name: "recorded failing call", Action: matching, ExpectMatch: true},
		{Name: "different tool", Action: otherTool, ExpectMatch: false},
	}
	if target != "" {
		unrelated := matching
		unrelated.Tool.Input = mustJSON(map[string]string{sample.field: "acr-simulation/unrelated.txt"})
		cases = append(cases, Case{Name: "same tool on an unrelated path", Action: unrelated, ExpectMatch: false})
	}

	return Candidate{
		CandidateID: candidateID, FailureNodeID: node.ID, EvidenceKey: evidenceKey, SourceEventIDs: evidence,
		Occurrences: attributes.Occurrences, Contract: value, Assumptions: assumptions, Ambiguities: ambiguities,
		FalsePositiveRisks: risks, Cases: cases,
	}
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
