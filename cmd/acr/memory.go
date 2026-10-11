package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/agent-contract-runtime/acr/internal/memory"
	"github.com/agent-contract-runtime/acr/internal/project"
)

type repeatedFlag []string

func (values *repeatedFlag) String() string { return strings.Join(*values, ",") }
func (values *repeatedFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func runMemory(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "memory requires add, list, review, show, history, activate, confirm, stale, contest, contest-pair, correct, retract, or archive")
		return 2
	}
	switch args[0] {
	case "add":
		return runMemoryAdd(args[1:], stdout, stderr)
	case "list":
		return runMemoryList(args[1:], stdout, stderr)
	case "review":
		return runMemoryReview(args[1:], stdout, stderr)
	case "show", "history":
		return runMemoryRead(args[0], args[1:], stdout, stderr)
	case "correct":
		return runMemoryCorrect(args[1:], stdout, stderr)
	case "contest-pair":
		return runMemoryContestPair(args[1:], stdout, stderr)
	case "activate", "confirm", "stale", "contest", "retract", "archive":
		return runMemoryTransition(args[0], args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unsupported memory command %q\n", args[0])
		return 2
	}
}

func runMemoryAdd(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("memory add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	kind := flags.String("kind", "", "memory kind")
	priorityText := flags.String("priority", "", "priority P0 through P6")
	claim := flags.String("claim", "", "memory claim")
	scope := flags.String("scope", `{}`, "JSON object scope")
	source := flags.String("source", "human", "memory source")
	confidence := flags.Float64("confidence", 1, "confidence from 0 through 1")
	workspaceID := flags.String("workspace-id", "", "optional workspace scope")
	validUntil := flags.String("valid-until", "", "optional RFC3339 expiry")
	var evidence repeatedFlag
	flags.Var(&evidence, "evidence", "evidence reference; repeatable")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *kind == "" || *priorityText == "" || strings.TrimSpace(*claim) == "" {
		fmt.Fprintln(stderr, "memory add requires --kind, --priority, and --claim")
		return 2
	}
	priority, err := parsePriority(*priorityText)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	expires, err := parseOptionalTime(*validUntil)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	config, store, closeStore, err := openMemoryStore(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer closeStore()
	value, err := store.CreateCandidate(context.Background(), memory.CreateRequest{
		ProjectID: config.ProjectID, WorkspaceID: *workspaceID, Kind: *kind, Priority: priority,
		Claim: *claim, Scope: json.RawMessage(*scope), EvidenceRefs: evidence, Source: *source,
		Confidence: *confidence, ValidUntil: expires,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeJSON(value, stdout, stderr)
}

func runMemoryList(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("memory list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	workspaceID := flags.String("workspace-id", "", "optional workspace scope")
	limit := flags.Int("limit", 100, "maximum memories")
	includeContested := flags.Bool("include-contested", false, "include contested memories for audit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "memory list accepts no positional arguments")
		return 2
	}
	config, store, closeStore, err := openMemoryStore(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer closeStore()
	values, err := store.Retrieve(context.Background(), memory.Query{ProjectID: config.ProjectID, WorkspaceID: *workspaceID, Limit: *limit, IncludeContested: *includeContested})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeJSON(values, stdout, stderr)
}

func runMemoryRead(command string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("memory "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintf(stderr, "memory %s requires one memory id\n", command)
		return 2
	}
	config, store, closeStore, err := openMemoryStore(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer closeStore()
	var value any
	if command == "show" {
		value, err = store.Get(context.Background(), config.ProjectID, flags.Arg(0))
	} else {
		value, err = store.History(context.Background(), config.ProjectID, flags.Arg(0))
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeJSON(value, stdout, stderr)
}

func runMemoryTransition(command string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("memory "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	reason := flags.String("reason", "", "reason for lifecycle change")
	expectedRevision := flags.Uint64("expected-revision", 0, "expected current revision; defaults to a fresh read")
	var evidence repeatedFlag
	flags.Var(&evidence, "evidence", "evidence reference; repeatable")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintf(stderr, "memory %s requires one memory id\n", command)
		return 2
	}
	config, store, closeStore, err := openMemoryStore(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer closeStore()
	id := flags.Arg(0)
	revision, err := resolveExpectedRevision(context.Background(), store, config.ProjectID, id, *expectedRevision)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var value memory.Memory
	switch command {
	case "activate":
		value, err = store.ActivateAtRevision(context.Background(), config.ProjectID, id, revision, *reason)
	case "confirm":
		value, err = store.ConfirmAtRevision(context.Background(), config.ProjectID, id, revision, evidence)
	case "stale":
		value, err = store.MarkStaleAtRevision(context.Background(), config.ProjectID, id, revision, *reason)
	case "contest":
		value, err = store.ContestAtRevision(context.Background(), config.ProjectID, id, revision, *reason)
	case "retract":
		value, err = store.RetractAtRevision(context.Background(), config.ProjectID, id, revision, *reason)
	case "archive":
		value, err = store.ArchiveAtRevision(context.Background(), config.ProjectID, id, revision, *reason)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeJSON(value, stdout, stderr)
}

func runMemoryCorrect(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("memory correct", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	claim := flags.String("claim", "", "corrected claim")
	scope := flags.String("scope", "", "optional corrected JSON object scope")
	source := flags.String("source", "human_correction", "correction source")
	confidence := flags.Float64("confidence", 1, "confidence from 0 through 1")
	reason := flags.String("reason", "", "correction reason")
	expectedRevision := flags.Uint64("expected-revision", 0, "expected current revision; defaults to a fresh read")
	validUntil := flags.String("valid-until", "", "optional RFC3339 expiry")
	var evidence repeatedFlag
	flags.Var(&evidence, "evidence", "evidence reference; repeatable")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 || strings.TrimSpace(*claim) == "" || strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(stderr, "memory correct requires one memory id, --claim, and --reason")
		return 2
	}
	expires, err := parseOptionalTime(*validUntil)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	config, store, closeStore, err := openMemoryStore(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer closeStore()
	var rawScope json.RawMessage
	if *scope != "" {
		rawScope = json.RawMessage(*scope)
	}
	revision, err := resolveExpectedRevision(context.Background(), store, config.ProjectID, flags.Arg(0), *expectedRevision)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	value, err := store.CorrectAtRevision(context.Background(), config.ProjectID, flags.Arg(0), revision, memory.CorrectRequest{
		Claim: *claim, Scope: rawScope, EvidenceRefs: evidence, Source: *source,
		Confidence: *confidence, ValidUntil: expires, Reason: *reason,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeJSON(value, stdout, stderr)
}

func runMemoryContestPair(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("memory contest-pair", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	leftRevision := flags.Uint64("left-revision", 0, "expected left revision; defaults to a fresh read")
	rightRevision := flags.Uint64("right-revision", 0, "expected right revision; defaults to a fresh read")
	reason := flags.String("reason", "", "conflict reason")
	var evidence repeatedFlag
	flags.Var(&evidence, "evidence", "conflict evidence reference; repeatable")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 2 || strings.TrimSpace(*reason) == "" || len(evidence) == 0 {
		fmt.Fprintln(stderr, "memory contest-pair requires two memory ids, --reason, and at least one --evidence")
		return 2
	}
	config, store, closeStore, err := openMemoryStore(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer closeStore()
	left, err := resolveExpectedRevision(context.Background(), store, config.ProjectID, flags.Arg(0), *leftRevision)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	right, err := resolveExpectedRevision(context.Background(), store, config.ProjectID, flags.Arg(1), *rightRevision)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	leftValue, rightValue, err := store.ContestPair(context.Background(), config.ProjectID, flags.Arg(0), left, flags.Arg(1), right, *reason, evidence)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeJSON([]memory.Memory{leftValue, rightValue}, stdout, stderr)
}

func resolveExpectedRevision(ctx context.Context, store *memory.Store, projectID, memoryID string, expected uint64) (uint64, error) {
	if expected != 0 {
		return expected, nil
	}
	value, err := store.Get(ctx, projectID, memoryID)
	if err != nil {
		return 0, err
	}
	return value.Revision, nil
}

func openMemoryStore(root string) (project.Config, *memory.Store, func(), error) {
	config, paths, err := project.Load(root)
	if err != nil {
		return project.Config{}, nil, func() {}, err
	}
	store, err := memory.Open(paths.MemoryDB)
	if err != nil {
		return project.Config{}, nil, func() {}, err
	}
	return config, store, func() { store.Close() }, nil
}

func parsePriority(value string) (memory.Priority, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if len(value) != 2 || value[0] != 'P' {
		return 0, fmt.Errorf("priority %q must be P0 through P6", value)
	}
	number, err := strconv.Atoi(value[1:])
	if err != nil || number < 0 || number > 6 {
		return 0, fmt.Errorf("priority %q must be P0 through P6", value)
	}
	return memory.Priority(number), nil
}

func parseOptionalTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("time %q must use RFC3339: %w", value, err)
	}
	return parsed, nil
}

func writeJSON(value any, stdout, stderr io.Writer) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintf(stderr, "encode JSON: %v\n", err)
		return 1
	}
	return 0
}
