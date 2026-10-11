package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/project"
	"github.com/agent-contract-runtime/acr/internal/repair"
)

func runRepair(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "status" || args[0] == "resolve") {
		return runRepairRecovery(args[0], args[1:], stdout, stderr)
	}
	if len(args) == 0 || args[0] != "run" {
		fmt.Fprintln(stderr, "repair requires run, status, or resolve")
		return 2
	}
	flags := flag.NewFlagSet("repair run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	planPath := flags.String("plan", "", "repair plan JSON file")
	apply := flags.Bool("apply", false, "execute the validated plan")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *planPath == "" {
		fmt.Fprintln(stderr, "repair run requires --plan and accepts no positional arguments")
		return 2
	}
	file, err := os.Open(*planPath)
	if err != nil {
		fmt.Fprintf(stderr, "open repair plan %q: %v\n", *planPath, err)
		return 1
	}
	plan, err := repair.Decode(file)
	file.Close()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	hash, err := plan.Hash()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !*apply {
		fmt.Fprintf(stdout, "validated repair plan %s (%d steps)\n", plan.ID, len(plan.Steps))
		fmt.Fprintf(stdout, "idempotency key: %s\n", plan.IdempotencyKey)
		fmt.Fprintf(stdout, "plan hash: %s\n", hash)
		fmt.Fprintln(stdout, "preview only; pass --apply to execute")
		return 0
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
	runner := repair.Runner{
		Store:       store,
		ProjectID:   config.ProjectID,
		WorkspaceID: config.WorkspaceID,
		Root:        paths.Root,
		LockDir:     filepath.Join(paths.State, "repair-locks"),
		Executor:    repair.CommandExecutor{},
	}
	result, err := runner.Run(context.Background(), plan)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeJSON(result, stdout, stderr)
}

// runRepairRecovery shows the state of a plan's latest run (status) or
// records a human judgement for a step whose outcome is unknown (resolve).
func runRepairRecovery(command string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("repair "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	planPath := flags.String("plan", "", "repair plan JSON file")
	stepID := flags.String("step", "", "step whose outcome is unknown (resolve)")
	outcome := flags.String("outcome", "", "succeeded or failed (resolve)")
	reason := flags.String("reason", "", "why the outcome is known (resolve)")
	apply := flags.Bool("apply", false, "record the resolution (resolve)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *planPath == "" {
		fmt.Fprintf(stderr, "repair %s requires --plan and accepts no positional arguments\n", command)
		return 2
	}
	if command == "resolve" && (*stepID == "" || (*outcome != "succeeded" && *outcome != "failed") || strings.TrimSpace(*reason) == "") {
		fmt.Fprintln(stderr, "repair resolve requires --step, --outcome succeeded|failed, and --reason")
		return 2
	}
	file, err := os.Open(*planPath)
	if err != nil {
		fmt.Fprintf(stderr, "open repair plan %q: %v\n", *planPath, err)
		return 1
	}
	plan, err := repair.Decode(file)
	file.Close()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
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
	runner := repair.Runner{
		Store:       store,
		ProjectID:   config.ProjectID,
		WorkspaceID: config.WorkspaceID,
		Root:        paths.Root,
		LockDir:     filepath.Join(paths.State, "repair-locks"),
		Executor:    repair.CommandExecutor{},
	}
	status, err := runner.Inspect(context.Background(), plan)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if command == "status" {
		return writeJSON(status, stdout, stderr)
	}
	if !*apply {
		fmt.Fprintf(stdout, "would record step %q of repair %q as %s: %s\n", *stepID, plan.ID, *outcome, *reason)
		if *outcome == "failed" {
			fmt.Fprintln(stdout, "the run would fail and declared compensations of completed steps would run")
		}
		fmt.Fprintln(stdout, "preview only; pass --apply to record")
		return 0
	}
	if err := runner.Resolve(context.Background(), plan, *stepID, *outcome == "succeeded", *reason); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	status, err = runner.Inspect(context.Background(), plan)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeJSON(status, stdout, stderr)
}
