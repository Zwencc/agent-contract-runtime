package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/objectstore"
	"github.com/agent-contract-runtime/acr/internal/project"
)

const (
	defaultReplayLimit = 200
	maximumReplayLimit = 2000
	replayTextValueMax = 2000
)

// replayEntry is one replayed event plus the content of the objects its
// payload references, when requested.
type replayEntry struct {
	Event   eventstore.Event  `json:"event"`
	Objects map[string]string `json:"objects,omitempty"`
}

// runReplay is a cognitive replay: it shows recorded actions, outputs and
// errors of a session, correlation or tool call and never re-executes them.
func runReplay(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("replay", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	session := flags.String("session", "", "native agent session id or acr launch id; events of the other side of that launch are included")
	correlation := flags.String("correlation", "", "correlation id")
	toolUse := flags.String("tool-use", "", "native tool use id")
	all := flags.Bool("all", false, "replay every event of the project")
	limit := flags.Int("limit", defaultReplayLimit, "maximum events to replay")
	withObjects := flags.Bool("with-objects", false, "resolve and verify referenced objects")
	format := flags.String("format", "text", "output format: text or json")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	filters := 0
	for _, value := range []string{*session, *correlation, *toolUse} {
		if value != "" {
			filters++
		}
	}
	if *all {
		filters++
	}
	if flags.NArg() != 0 || filters != 1 {
		fmt.Fprintln(stderr, "replay requires exactly one of --session, --correlation, --tool-use, or --all")
		return 2
	}
	if *limit <= 0 || *limit > maximumReplayLimit {
		fmt.Fprintf(stderr, "replay --limit must be within 1..%d\n", maximumReplayLimit)
		return 2
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintln(stderr, "replay --format must be text or json")
		return 2
	}

	config, paths, err := project.Load(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := os.Stat(paths.EventDB); err != nil {
		fmt.Fprintf(stderr, "replay: event database: %v\n", err)
		return 1
	}
	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer store.Close()
	ctx := context.Background()
	// Replayed evidence is only meaningful if the chain behind it is intact.
	if err := store.Verify(ctx, config.ProjectID); err != nil {
		fmt.Fprintf(stderr, "replay refused: %v\n", err)
		return 1
	}

	sessions := map[string]bool{}
	if *session != "" {
		if sessions, err = linkedSessions(ctx, store, config.ProjectID, *session); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	match := func(event eventstore.Event) bool {
		switch {
		case *session != "":
			// launch_started is the launch itself: its event ID is the launch ID.
			return sessions[event.Actor.NativeSessionID] || sessions[event.Actor.ACRSessionID] || sessions[event.EventID]
		case *correlation != "":
			return event.Causality.CorrelationID == *correlation
		case *toolUse != "":
			return event.Causality.NativeToolUseID == *toolUse
		}
		return true
	}
	entries := []replayEntry{}
	truncated := false
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
			if !match(event) {
				continue
			}
			if len(entries) == *limit {
				truncated = true
				break
			}
			entries = append(entries, replayEntry{Event: event})
		}
		if truncated {
			break
		}
	}

	missing := 0
	if *withObjects {
		objects, err := objectstore.Open(paths.Objects)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		for i := range entries {
			for field, ref := range payloadRefs(entries[i].Event.Payload) {
				if entries[i].Objects == nil {
					entries[i].Objects = map[string]string{}
				}
				content, err := objects.Get(ref)
				if err != nil {
					missing++
					entries[i].Objects[field] = "[object unavailable: " + err.Error() + "]"
					continue
				}
				entries[i].Objects[field] = string(content)
			}
		}
	}

	if *format == "json" {
		if code := writeJSON(entries, stdout, stderr); code != 0 {
			return code
		}
	} else {
		writeReplayText(stdout, entries)
	}
	if truncated {
		fmt.Fprintf(stderr, "replay stopped at --limit %d; more matching events exist\n", *limit)
	}
	if missing > 0 {
		fmt.Fprintf(stderr, "replay: %d referenced objects could not be verified\n", missing)
		return 1
	}
	return 0
}

// payloadRefs returns the top-level *_ref fields of a payload that hold a
// valid object reference.
func payloadRefs(payload json.RawMessage) map[string]objectstore.Ref {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil
	}
	refs := map[string]objectstore.Ref{}
	for name, raw := range fields {
		if !strings.HasSuffix(name, "_ref") {
			continue
		}
		var ref objectstore.Ref
		if err := json.Unmarshal(raw, &ref); err == nil && ref.Digest != "" && ref.MediaType != "" {
			refs[name] = ref
		}
	}
	return refs
}

// linkedSessions returns the session plus the sessions recorded together with
// it: the native sessions an acr launch started, or the launch that started a
// native session.
func linkedSessions(ctx context.Context, store *eventstore.Store, projectID, session string) (map[string]bool, error) {
	linked := map[string]bool{session: true}
	for after := uint64(0); ; {
		events, err := store.List(ctx, projectID, after, 500)
		if err != nil {
			return nil, err
		}
		if len(events) == 0 {
			return linked, nil
		}
		for _, event := range events {
			after = event.Sequence
			native, launch := event.Actor.NativeSessionID, event.Actor.ACRSessionID
			if native == "" || launch == "" {
				continue
			}
			if native == session {
				linked[launch] = true
			}
			if launch == session {
				linked[native] = true
			}
		}
	}
}

func writeReplayText(writer io.Writer, entries []replayEntry) {
	if len(entries) == 0 {
		fmt.Fprintln(writer, "no matching events")
		return
	}
	for _, entry := range entries {
		event := entry.Event
		header := fmt.Sprintf("#%d %s %s actor=%s", event.Sequence, event.OccurredAt.UTC().Format("2006-01-02T15:04:05Z"), event.Type, event.Actor.Kind)
		if event.Actor.AgentKind != "" {
			header += " agent=" + string(event.Actor.AgentKind)
		}
		if event.Actor.NativeSessionID != "" {
			header += " session=" + event.Actor.NativeSessionID
		}
		if event.Actor.ACRSessionID != "" && event.Actor.ACRSessionID != event.Actor.NativeSessionID {
			header += " launch=" + event.Actor.ACRSessionID
		}
		if event.Causality.NativeToolUseID != "" {
			header += " tool_use=" + event.Causality.NativeToolUseID
		}
		fmt.Fprintln(writer, header)

		var fields map[string]json.RawMessage
		if err := json.Unmarshal(event.Payload, &fields); err != nil {
			fmt.Fprintf(writer, "  payload: %s\n", clip(string(event.Payload)))
			continue
		}
		names := make([]string, 0, len(fields))
		for name := range fields {
			names = append(names, name)
		}
		sort.Strings(names)
		refs := payloadRefs(event.Payload)
		for _, name := range names {
			if ref, ok := refs[name]; ok {
				fmt.Fprintf(writer, "  %s: object %s (%d bytes, %s)\n", name, ref.Digest, ref.Size, ref.MediaType)
				if content, ok := entry.Objects[name]; ok {
					fmt.Fprintf(writer, "    %s\n", strings.ReplaceAll(clip(content), "\n", "\n    "))
				}
				continue
			}
			fmt.Fprintf(writer, "  %s: %s\n", name, clip(string(fields[name])))
		}
	}
}

func clip(value string) string {
	if len(value) <= replayTextValueMax {
		return value
	}
	cut := replayTextValueMax
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + fmt.Sprintf("... (%d more bytes; use --format json)", len(value)-cut)
}
