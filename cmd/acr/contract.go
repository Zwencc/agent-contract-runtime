package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/project"
	"github.com/agent-contract-runtime/acr/internal/registry"
)

// runContractRegistry handles contract activate, retire and list: the
// commands a person uses to change which contracts are enforced. Agents use
// the ACR tools instead, which record the user's words that asked for it.
func runContractRegistry(command string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("contract "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	reason := flags.String("reason", "", "why the contract is retired")
	apply := flags.Bool("apply", false, "record the change; without it the command only previews")
	target := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if target == "" && flags.NArg() == 1 {
		target = flags.Arg(0)
	} else if flags.NArg() != 0 {
		target = ""
	}
	if (command == "list") != (target == "") || command == "retire" && strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(stderr, "usage: acr contract activate <file> [--apply] | retire <id> --reason <text> [--apply] | list")
		return 2
	}
	config, paths, err := project.Load(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer store.Close()
	ctx := context.Background()
	source := registry.Source{Kind: registry.SourceCLI}

	switch command {
	case "activate":
		values, err := contract.LoadPath(target)
		if err != nil || len(values) != 1 {
			fmt.Fprintf(stderr, "contract activate needs exactly one contract file: %v\n", err)
			return 1
		}
		value := values[0]
		value.Metadata.Status = contract.StatusActive
		if err := value.Validate(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if !*apply {
			fmt.Fprintf(stdout, "would activate %s (%s)\npreview only; pass --apply to enforce it\n", value.Metadata.ID, value.Metadata.Title)
			return 0
		}
		entry, err := registry.Activate(ctx, store, config.ProjectID, config.WorkspaceID, paths.Contracts, value, source, time.Now())
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "activated %s version %d (event %s)\n", entry.Contract.Metadata.ID, entry.Contract.Metadata.Version, entry.EventID)
		return 0
	case "retire":
		if !*apply {
			fmt.Fprintf(stdout, "would retire %s\npreview only; pass --apply to stop enforcing it\n", target)
			return 0
		}
		if err := registry.Retire(ctx, store, config.ProjectID, config.WorkspaceID, paths.Contracts, target, *reason, source, time.Now()); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "retired %s\n", target)
		return 0
	}

	entries, managed, err := registry.Active(ctx, store, config.ProjectID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !managed {
		fmt.Fprintf(stdout, "contracts are read from %s; none was activated through ACR yet, so anyone who can write that directory changes the rules\n", paths.Contracts)
		return 0
	}
	for _, entry := range entries {
		line := fmt.Sprintf("%s v%d  %s  [%s]", entry.Contract.Metadata.ID, entry.Contract.Metadata.Version, entry.Contract.Metadata.Title, entry.Source.Kind)
		if entry.Source.Quote != "" {
			line += fmt.Sprintf("  user said: %q", clipRunes(entry.Source.Quote, 120))
		}
		fmt.Fprintln(stdout, line)
	}
	for _, name := range unmanagedContractFiles(paths.Contracts, entries) {
		fmt.Fprintf(stdout, "not enforced: %s differs from the active contracts; activate it with acr contract activate %s --apply\n", name, filepath.Join(paths.Contracts, name))
	}
	return 0
}

// unmanagedContractFiles lists contract files that are not an exact copy of
// an active contract, such as drafts or files changed outside ACR.
func unmanagedContractFiles(directory string, entries []registry.Entry) []string {
	digests := map[string]string{}
	for _, entry := range entries {
		digests[entry.Contract.Metadata.ID+".json"] = entry.Digest
	}
	names, err := filepath.Glob(filepath.Join(directory, "*.json"))
	if err != nil {
		return nil
	}
	result := []string{}
	for _, name := range names {
		values, err := contract.LoadPath(name)
		base := filepath.Base(name)
		if err == nil && len(values) == 1 {
			if digest, err := registry.Digest(values[0]); err == nil && digest == digests[base] {
				continue
			}
		}
		result = append(result, base)
	}
	return result
}

func clipRunes(value string, maximum int) string {
	if len([]rune(value)) <= maximum {
		return value
	}
	return string([]rune(value)[:maximum-1]) + "…"
}
