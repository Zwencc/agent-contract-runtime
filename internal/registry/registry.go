// Package registry keeps the set of enforced contracts in the event chain.
//
// Once a project activates a contract through ACR, the contracts it enforces
// are exactly those recorded by contract.activated and not later retired,
// whatever the contract directory holds. The directory then only mirrors the
// set for people to read, so a file written there by other means changes
// nothing. A project that never activated through ACR keeps loading its
// contract directory as before.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
)

const (
	EventActivated = "contract.activated"
	EventRetired   = "contract.retired"
)

// Source says who changed the set: a person at the CLI, the user through an
// agent (with the words that asked for it), or the import of contracts that
// were in force before the project managed them.
type Source struct {
	Kind         string `json:"kind"`
	QuoteEventID string `json:"quote_event_id,omitempty"`
	Quote        string `json:"quote,omitempty"`
}

const (
	SourceCLI             = "cli"
	SourceConsole         = "console"
	SourceUserInstruction = "user_instruction"
	SourceLegacyImport    = "legacy_import"
)

// Entry is one enforced contract and the event that activated it.
type Entry struct {
	Contract contract.Contract
	Digest   string
	EventID  string
	Source   Source
}

type activatedPayload struct {
	Contract contract.Contract `json:"contract"`
	Digest   string            `json:"digest"`
	Source   Source            `json:"source"`
}

type retiredPayload struct {
	ContractID string `json:"contract_id"`
	Version    int    `json:"version"`
	Reason     string `json:"reason"`
	Source     Source `json:"source"`
}

// Active folds the registry events. managed is false when the project has
// never activated a contract through ACR.
func Active(ctx context.Context, store *eventstore.Store, projectID string) ([]Entry, bool, error) {
	byID := map[string]Entry{}
	managed := false
	err := store.Each(ctx, projectID, 0, eventstore.Filter{Types: []string{EventActivated, EventRetired}}, func(event eventstore.Event) error {
		managed = true
		if event.Type == EventRetired {
			var payload retiredPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return fmt.Errorf("decode %s %s: %w", event.Type, event.EventID, err)
			}
			delete(byID, payload.ContractID)
			return nil
		}
		var payload activatedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode %s %s: %w", event.Type, event.EventID, err)
		}
		if digest, err := Digest(payload.Contract); err != nil || digest != payload.Digest {
			return fmt.Errorf("%s %s: contract digest does not match", event.Type, event.EventID)
		}
		byID[payload.Contract.Metadata.ID] = Entry{Contract: payload.Contract, Digest: payload.Digest, EventID: event.EventID, Source: payload.Source}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	entries := make([]Entry, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, byID[id])
	}
	return entries, managed, nil
}

// Load returns the contracts to enforce: the built-in ones plus the activated
// set of a managed project, otherwise plus every contract in directory.
func Load(ctx context.Context, store *eventstore.Store, projectID, directory string) ([]contract.Contract, error) {
	entries, managed, err := Active(ctx, store, projectID)
	if err != nil {
		return nil, err
	}
	values := Builtin()
	if !managed {
		loaded, err := contract.LoadPath(directory)
		return append(values, loaded...), err
	}
	for _, entry := range entries {
		values = append(values, entry.Contract)
	}
	return values, nil
}

// BuiltinProtectState is the ID of the built-in contract that keeps agents
// from writing ACR's own state with their file tools.
const BuiltinProtectState = "acr-builtin-protect-state"

// Builtin returns the contracts ACR always enforces. Contracts, plans and
// memories change only through ACR, which records who asked for the change;
// an agent that writes .acr/ directly could otherwise rewrite its own rules
// or the plans guarded mode runs, and one that runs the person's contract
// commands would be recorded as that person.
func Builtin() []contract.Contract {
	cli := func(command string) contract.Contract {
		return contract.Contract{
			APIVersion: contract.APIVersion,
			Kind:       "Contract",
			Metadata:   contract.Metadata{ID: "acr-builtin-no-agent-" + strings.ReplaceAll(command, " ", "-"), Version: 1, Title: "Agents do not run acr " + command, Status: contract.StatusActive, Severity: "critical", Priority: 1000},
			Scope:      contract.Scope{OperationKinds: []string{"process.exec"}},
			Applicability: contract.Applicability{All: []contract.Condition{{
				Field: "input.command", Operator: contract.OperatorContains, Value: json.RawMessage(`"` + command + `"`),
			}}},
			Behavior: contract.Behavior{
				Instruction: "acr " + command + " is for the person at the terminal. To change a rule the user asked for, use mcp__acr__add_rule or mcp__acr__retire_rule with the user's words.",
				Enforcement: contract.EnforcementHard, NonOverridable: true,
			},
			Correction:  contract.Correction{Mode: contract.CorrectionDeny},
			FailureMode: contract.FailureClosed,
		}
	}
	return []contract.Contract{cli("contract activate"), cli("contract retire"), {
		APIVersion: contract.APIVersion,
		Kind:       "Contract",
		Metadata:   contract.Metadata{ID: BuiltinProtectState, Version: 1, Title: "ACR state changes only through ACR", Status: contract.StatusActive, Severity: "critical", Priority: 1000},
		Scope:      contract.Scope{OperationKinds: []string{"filesystem.write"}},
		Applicability: contract.Applicability{All: []contract.Condition{{
			Field: contract.FieldPaths, Operator: contract.OperatorGlob, Value: json.RawMessage(`".acr/**"`),
		}}},
		Behavior: contract.Behavior{
			Instruction: "Do not write .acr/ directly. To remember something, change a rule or record a plan the user asked for, use the ACR tools (mcp__acr__remember, mcp__acr__add_rule and related), quoting the user's words.",
			Enforcement: contract.EnforcementHard, NonOverridable: true,
		},
		Correction:  contract.Correction{Mode: contract.CorrectionDeny},
		FailureMode: contract.FailureClosed,
	}}
}

// Digest identifies a contract's content.
func Digest(value contract.Contract) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Activate puts value in force, replacing an active contract with the same
// ID under the next version, and mirrors it to directory. The first
// activation of an unmanaged project first imports the active contracts of
// directory, so nothing in force before stops applying.
func Activate(ctx context.Context, store *eventstore.Store, projectID, workspaceID, directory string, value contract.Contract, source Source, now time.Time) (Entry, error) {
	value.Metadata.Status = contract.StatusActive
	if value.Metadata.Version < 1 {
		value.Metadata.Version = 1
	}
	if err := value.Validate(); err != nil {
		return Entry{}, err
	}
	entries, managed, err := Active(ctx, store, projectID)
	if err != nil {
		return Entry{}, err
	}
	if !managed {
		existing, err := contract.LoadPath(directory)
		if err != nil {
			return Entry{}, fmt.Errorf("import contracts in force: %w", err)
		}
		for _, item := range existing {
			if item.Metadata.Status != contract.StatusActive || item.Metadata.ID == value.Metadata.ID {
				continue
			}
			imported, err := record(ctx, store, projectID, workspaceID, item, Source{Kind: SourceLegacyImport}, now)
			if err != nil {
				return Entry{}, err
			}
			entries = append(entries, imported)
		}
	}
	for _, entry := range entries {
		if entry.Contract.Metadata.ID == value.Metadata.ID && value.Metadata.Version <= entry.Contract.Metadata.Version {
			value.Metadata.Version = entry.Contract.Metadata.Version + 1
		}
	}
	if err := value.Validate(); err != nil {
		return Entry{}, err
	}
	entry, err := record(ctx, store, projectID, workspaceID, value, source, now)
	if err != nil {
		return Entry{}, err
	}
	encoded, _ := json.MarshalIndent(value, "", "  ")
	if err := writeMirror(filepath.Join(directory, value.Metadata.ID+".json"), append(encoded, '\n')); err != nil {
		return entry, fmt.Errorf("contract %s is active, but its copy in %s was not written: %w", value.Metadata.ID, directory, err)
	}
	return entry, nil
}

// Retire takes the contract with id out of force and removes its copy.
func Retire(ctx context.Context, store *eventstore.Store, projectID, workspaceID, directory, id, reason string, source Source, now time.Time) error {
	entries, managed, err := Active(ctx, store, projectID)
	if err != nil {
		return err
	}
	if !managed {
		return errors.New("this project does not manage its contracts through ACR yet; activate one first")
	}
	for _, entry := range entries {
		if entry.Contract.Metadata.ID != id {
			continue
		}
		payload, _ := json.Marshal(retiredPayload{ContractID: id, Version: entry.Contract.Metadata.Version, Reason: reason, Source: source})
		if _, err := store.Append(ctx, eventstore.AppendRequest{ProjectID: projectID, WorkspaceID: workspaceID, OccurredAt: now, Type: EventRetired,
			Actor: actorFor(source), Payload: payload, EvidenceRefs: evidenceFor(source)}); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(directory, id+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("contract %s is retired, but its copy was not removed: %w", id, err)
		}
		return nil
	}
	return fmt.Errorf("contract %s is not active", id)
}

func record(ctx context.Context, store *eventstore.Store, projectID, workspaceID string, value contract.Contract, source Source, now time.Time) (Entry, error) {
	digest, err := Digest(value)
	if err != nil {
		return Entry{}, err
	}
	payload, err := json.Marshal(activatedPayload{Contract: value, Digest: digest, Source: source})
	if err != nil {
		return Entry{}, err
	}
	event, err := store.Append(ctx, eventstore.AppendRequest{ProjectID: projectID, WorkspaceID: workspaceID, OccurredAt: now, Type: EventActivated,
		Actor: actorFor(source), Payload: payload, EvidenceRefs: evidenceFor(source)})
	if err != nil {
		return Entry{}, fmt.Errorf("record activation of %s: %w", value.Metadata.ID, err)
	}
	return Entry{Contract: value, Digest: digest, EventID: event.EventID, Source: source}, nil
}

func actorFor(source Source) eventstore.Actor {
	if source.Kind == SourceUserInstruction {
		return eventstore.Actor{Kind: "user"}
	}
	if source.Kind == SourceCLI || source.Kind == SourceConsole {
		return eventstore.Actor{Kind: "human"}
	}
	return eventstore.Actor{Kind: "runtime"}
}

func evidenceFor(source Source) []string {
	if source.QuoteEventID == "" {
		return nil
	}
	return []string{"event:" + source.QuoteEventID}
}

func writeMirror(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
