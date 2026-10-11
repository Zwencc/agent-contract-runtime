package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agent-contract-runtime/acr/internal/contract"
	"github.com/agent-contract-runtime/acr/internal/distill"
	"github.com/agent-contract-runtime/acr/internal/eventstore"
	"github.com/agent-contract-runtime/acr/internal/project"
	"github.com/agent-contract-runtime/acr/internal/projector"
)

// runCandidate manages contract candidates distilled from repeated failures:
// propose, list, reject and accept. Accepting writes a draft contract that
// stays inactive until a human edits its status.
func runCandidate(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "candidate requires propose, list, reject, or accept")
		return 2
	}
	command := args[0]
	if command != "propose" && command != "list" && command != "reject" && command != "accept" {
		fmt.Fprintln(stderr, "candidate requires propose, list, reject, or accept")
		return 2
	}
	flags := flag.NewFlagSet("candidate "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "project root")
	minimum := flags.Int("min-occurrences", distill.DefaultMinimumOccurrences, "failures needed before proposing (propose)")
	all := flags.Bool("all", false, "include rejected and accepted candidates (list)")
	reason := flags.String("reason", "", "why the candidate is rejected (reject)")
	apply := flags.Bool("apply", false, "write the draft contract (accept)")
	needsID := command == "reject" || command == "accept"
	rest := args[1:]
	candidateID := ""
	// Accept the id before or after the flags.
	if needsID && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		candidateID, rest = rest[0], rest[1:]
	}
	if err := flags.Parse(rest); err != nil {
		return 2
	}
	if needsID && candidateID == "" && flags.NArg() == 1 {
		candidateID = flags.Arg(0)
	} else if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "candidate %s got unexpected arguments %v\n", command, flags.Args())
		return 2
	}
	if needsID && candidateID == "" {
		fmt.Fprintf(stderr, "candidate %s requires a candidate id\n", command)
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

	switch command {
	case "propose":
		projections, err := projector.Open(paths.ProjectionDB)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer projections.Close()
		proposed, err := distill.Propose(ctx, store, projections, config.ProjectID, paths.Root, *minimum, time.Now())
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeJSON(proposed, stdout, stderr)
	case "list":
		records, err := distill.List(ctx, store, config.ProjectID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		shown := []distill.Record{}
		for _, record := range records {
			if *all || record.State == distill.StateProposed {
				shown = append(shown, record)
			}
		}
		return writeJSON(shown, stdout, stderr)
	case "reject":
		if strings.TrimSpace(*reason) == "" {
			fmt.Fprintln(stderr, "candidate reject requires --reason")
			return 2
		}
		if _, err := distill.Decide(ctx, store, config.ProjectID, candidateID, false, *reason, time.Now()); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "rejected %s; it will not be proposed again on the same evidence\n", candidateID)
		return 0
	case "accept":
		records, err := distill.List(ctx, store, config.ProjectID)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		var candidate *distill.Candidate
		for i := range records {
			if records[i].Candidate.CandidateID == candidateID && records[i].State == distill.StateProposed {
				candidate = &records[i].Candidate
			}
		}
		if candidate == nil {
			fmt.Fprintf(stderr, "no proposed candidate %s\n", candidateID)
			return 1
		}
		results, err := distill.Simulate(candidate.Contract, candidate.Cases)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		contractPath := filepath.Join(paths.Contracts, candidate.Contract.Metadata.ID+".json")
		casesPath := filepath.Join(paths.ACRDirectory, "candidates", candidate.Contract.Metadata.ID+".cases.json")
		if !*apply {
			return writeJSON(map[string]any{
				"would_write":          []string{contractPath, casesPath},
				"contract":             candidate.Contract,
				"assumptions":          candidate.Assumptions,
				"ambiguities":          candidate.Ambiguities,
				"false_positive_risks": candidate.FalsePositiveRisks,
				"simulation":           results,
				"note":                 "preview only; pass --apply to write the draft contract. It stays draft until a human activates it.",
			}, stdout, stderr)
		}
		for _, result := range results {
			if !result.Passed {
				fmt.Fprintf(stderr, "candidate %s fails its own simulation case %q; not accepted\n", candidate.CandidateID, result.Name)
				return 1
			}
		}
		encodedContract, _ := json.MarshalIndent(candidate.Contract, "", "  ")
		encodedCases, _ := json.MarshalIndent(candidate.Cases, "", "  ")
		if err := writeNewFile(contractPath, encodedContract); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := writeNewFile(casesPath, encodedCases); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if _, err := distill.Decide(ctx, store, config.ProjectID, candidate.CandidateID, true, "", time.Now()); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "wrote draft contract %s and simulation cases %s\n", contractPath, casesPath)
		return 0
	}
	fmt.Fprintln(stderr, "candidate requires propose, list, reject, or accept")
	return 2
}

// writeNewFile refuses to overwrite, so accepting never replaces a contract.
func writeNewFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	return file.Close()
}

// runContractSimulate checks one contract against example cases as if it
// were active, and fails when any case does not match its expectation.
func runContractSimulate(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("contract simulate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	casesPath := flags.String("cases", "", "JSON array of {name, action, expect_match}")
	contractPath := ""
	// Accept the contract file before or after the flags.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		contractPath, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if contractPath == "" && flags.NArg() == 1 {
		contractPath = flags.Arg(0)
	} else if flags.NArg() != 0 {
		contractPath = ""
	}
	if contractPath == "" || *casesPath == "" {
		fmt.Fprintln(stderr, "contract simulate requires one contract file and --cases")
		return 2
	}
	values, err := contract.LoadPath(contractPath)
	if err != nil || len(values) != 1 {
		fmt.Fprintf(stderr, "contract simulate needs exactly one contract: %v\n", err)
		return 1
	}
	data, err := os.ReadFile(*casesPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var cases []distill.Case
	if err := json.Unmarshal(data, &cases); err != nil || len(cases) == 0 {
		fmt.Fprintf(stderr, "decode simulation cases: %v\n", err)
		return 1
	}
	results, err := distill.Simulate(values[0], cases)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if code := writeJSON(results, stdout, stderr); code != 0 {
		return code
	}
	for _, result := range results {
		if !result.Passed {
			return 1
		}
	}
	return 0
}
