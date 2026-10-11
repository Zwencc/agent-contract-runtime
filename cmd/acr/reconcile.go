package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/agent-contract-runtime/acr/internal/coverage"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/project"
)

func runReconcile(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "reconcile requires status or accept")
		return 2
	}
	switch args[0] {
	case "status":
		return runReconcileStatus(args[1:], stdout, stderr)
	case "accept":
		return runReconcileAccept(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unsupported reconcile command %q\n", args[0])
		return 2
	}
}

func runReconcileStatus(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("reconcile status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "reconcile status accepts no positional arguments")
		return 2
	}
	_, store, closeStore, err := openEventStore(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer closeStore()
	config, _, err := project.Load(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	gaps, err := unresolvedCoverageGaps(context.Background(), store, config.ProjectID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeJSON(gaps, stdout, stderr)
}

func runReconcileAccept(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("reconcile accept", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	eventID := flags.String("event-id", "", "coverage gap event id")
	reason := flags.String("reason", "", "human review reason")
	resolution := flags.String("resolution", "", "current_state_accepted, documented_external_change, or hook_repaired")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*eventID) == "" || strings.TrimSpace(*reason) == "" || !validResolution(*resolution) {
		fmt.Fprintln(stderr, "reconcile accept requires --event-id, --reason, and --resolution current_state_accepted|documented_external_change|hook_repaired")
		return 2
	}
	config, paths, err := project.Load(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := os.Stat(paths.EventDB); err != nil {
		fmt.Fprintf(stderr, "inspect event database %q: %v\n", paths.EventDB, err)
		return 1
	}
	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer store.Close()
	event, err := acceptCoverageGap(context.Background(), store, config, paths, *eventID, *resolution, *reason)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeJSON(event, stdout, stderr)
}

// acceptCoverageGap records a person's resolution of an open coverage gap,
// provided the workspace has not changed again since the gap was found.
func acceptCoverageGap(ctx context.Context, store *eventstore.Store, config project.Config, paths project.Paths, eventID, resolution, reason string) (eventstore.Event, error) {
	if strings.TrimSpace(reason) == "" || !validResolution(resolution) {
		return eventstore.Event{}, errors.New("a resolution of current_state_accepted, documented_external_change or hook_repaired and a reason are required")
	}
	gaps, err := unresolvedCoverageGaps(ctx, store, config.ProjectID)
	if err != nil {
		return eventstore.Event{}, err
	}
	var gap eventstore.Event
	for _, item := range gaps {
		if item.EventID == eventID {
			gap = item
			break
		}
	}
	if gap.EventID == "" {
		return eventstore.Event{}, fmt.Errorf("coverage gap %q is not open in project %q", eventID, config.ProjectID)
	}
	baseline, err := coverage.Read(paths.WorkspaceSnapshot)
	if err != nil {
		return eventstore.Event{}, err
	}
	current, err := coverage.Scan(paths.Root, baseline.EventSequence, coverage.Options{})
	if err != nil {
		return eventstore.Event{}, err
	}
	if changes := coverage.Compare(baseline, current); len(changes) != 0 {
		return eventstore.Event{}, fmt.Errorf("workspace changed again after the recorded gap; run a hooked SessionStart before reconciliation (%d changes)", len(changes))
	}
	payload, err := json.Marshal(map[string]any{
		"gap_event_id":        eventID,
		"resolution":          resolution,
		"reason":              strings.TrimSpace(reason),
		"reviewed_file_count": len(current.Files),
		"skipped_files":       current.SkippedFiles,
	})
	if err != nil {
		return eventstore.Event{}, fmt.Errorf("encode reconciliation: %w", err)
	}
	return store.Append(ctx, eventstore.AppendRequest{
		ProjectID:    config.ProjectID,
		WorkspaceID:  config.WorkspaceID,
		OccurredAt:   time.Now(),
		Type:         "coverage.gap_reconciled",
		Actor:        eventstore.Actor{Kind: "human"},
		Payload:      payload,
		EvidenceRefs: []string{gap.EventID},
	})
}

func unresolvedCoverageGaps(ctx context.Context, store *eventstore.Store, projectID string) ([]eventstore.Event, error) {
	open := map[string]eventstore.Event{}
	order := []string{}
	after := uint64(0)
	for {
		events, err := store.List(ctx, projectID, after, 500)
		if err != nil {
			return nil, err
		}
		if len(events) == 0 {
			break
		}
		for _, event := range events {
			switch event.Type {
			case "coverage.gap_detected":
				open[event.EventID] = event
				order = append(order, event.EventID)
			case "coverage.gap_reconciled":
				var payload struct {
					GapEventID string `json:"gap_event_id"`
				}
				if json.Unmarshal(event.Payload, &payload) == nil {
					delete(open, payload.GapEventID)
				}
			}
			after = event.Sequence
		}
	}
	result := make([]eventstore.Event, 0, len(open))
	for _, eventID := range order {
		if event, exists := open[eventID]; exists {
			result = append(result, event)
		}
	}
	return result, nil
}

func validResolution(value string) bool {
	switch value {
	case "current_state_accepted", "documented_external_change", "hook_repaired":
		return true
	default:
		return false
	}
}

func openEventStore(root string) (project.Paths, *eventstore.Store, func(), error) {
	_, paths, err := project.Load(root)
	if err != nil {
		return project.Paths{}, nil, func() {}, err
	}
	if _, err := os.Stat(paths.EventDB); err != nil {
		return project.Paths{}, nil, func() {}, fmt.Errorf("inspect event database %q: %w", paths.EventDB, err)
	}
	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		return project.Paths{}, nil, func() {}, err
	}
	return paths, store, func() { store.Close() }, nil
}
