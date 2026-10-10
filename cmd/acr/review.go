package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/memory"
	"github.com/agent-contract-runtime/acr/internal/project"
	"github.com/agent-contract-runtime/acr/internal/registry"
)

// runMemoryReview lists recent memory and rule changes with the words that
// asked for them, so a person can check what agents recorded.
func runMemoryReview(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("memory review", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	since := flags.Duration("since", 7*24*time.Hour, "how far back to look")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *since <= 0 {
		fmt.Fprintln(stderr, "memory review accepts --root and a positive --since")
		return 2
	}
	config, paths, err := project.Load(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := context.Background()
	start := time.Now().Add(-*since)
	type line struct {
		at   time.Time
		text string
	}
	lines := []line{}

	memories, err := memory.Open(paths.MemoryDB)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer memories.Close()
	revisions, err := memories.RevisionsSince(ctx, config.ProjectID, start, memory.MaximumRetrieveLimit,
		[]memory.Action{memory.ActionCreate, memory.ActionActivate, memory.ActionSupersede, memory.ActionRetract, memory.ActionContest})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	labels := map[memory.Action]string{memory.ActionCreate: "candidate", memory.ActionActivate: "in effect", memory.ActionSupersede: "replaced",
		memory.ActionRetract: "retracted", memory.ActionContest: "contested"}
	for _, revision := range revisions {
		value := revision.Memory
		// A memory created and activated together is reported once, as in effect.
		if revision.Action == memory.ActionCreate && value.Source != "agent" {
			continue
		}
		text := fmt.Sprintf("[%s] %s %s %q (source: %s)", labels[revision.Action], value.MemoryID, value.Priority, value.Claim, value.Source)
		if revision.Action == memory.ActionCreate {
			text += " — the agent proposed this; it applies only after you agree"
		}
		if revision.RelatedMemoryID != "" {
			text += " → " + revision.RelatedMemoryID
		}
		if revision.Reason != "" {
			text += " — " + revision.Reason
		}
		lines = append(lines, line{revision.RecordedAt, text})
	}

	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer store.Close()
	for after := uint64(0); ; {
		events, err := store.List(ctx, config.ProjectID, after, 500)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if len(events) == 0 {
			break
		}
		for _, event := range events {
			after = event.Sequence
			if event.RecordedAt.Before(start) || (event.Type != registry.EventActivated && event.Type != registry.EventRetired) {
				continue
			}
			var payload struct {
				Contract   contract.Contract `json:"contract"`
				ContractID string            `json:"contract_id"`
				Reason     string            `json:"reason"`
				Source     registry.Source   `json:"source"`
			}
			if json.Unmarshal(event.Payload, &payload) != nil || payload.Source.Kind == registry.SourceLegacyImport {
				continue
			}
			text := fmt.Sprintf("[rule added] %s v%d %q", payload.Contract.Metadata.ID, payload.Contract.Metadata.Version, payload.Contract.Metadata.Title)
			if event.Type == registry.EventRetired {
				text = fmt.Sprintf("[rule removed] %s — %s", payload.ContractID, payload.Reason)
			}
			text += " (source: " + payload.Source.Kind + ")"
			if payload.Source.Quote != "" {
				text += fmt.Sprintf(" — user said: %q", payload.Source.Quote)
			}
			lines = append(lines, line{event.RecordedAt, text})
		}
	}

	if len(lines) == 0 {
		fmt.Fprintf(stdout, "no memory or rule changes since %s\n", start.Format(time.RFC3339))
		return 0
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].at.Before(lines[j].at) })
	for _, item := range lines {
		fmt.Fprintf(stdout, "%s  %s\n", item.at.Local().Format("2006-01-02 15:04"), item.text)
	}
	return 0
}
