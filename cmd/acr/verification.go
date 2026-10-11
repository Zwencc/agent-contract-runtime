package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/project"
	"github.com/agent-contract-runtime/acr/internal/verification"
)

func runVerificationList(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("verification list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "verification list accepts no positional arguments")
		return 2
	}
	config, paths, err := project.Load(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if _, err := os.Stat(paths.EventDB); os.IsNotExist(err) {
		return writeJSON([]verification.Status{}, stdout, stderr)
	}
	store, err := eventstore.Open(paths.EventDB)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer store.Close()
	statuses, err := verification.Statuses(context.Background(), store, config.ProjectID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeJSON(statuses, stdout, stderr)
}
