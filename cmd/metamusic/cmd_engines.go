package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/engineadopt"
)

// adoptable are the engines the operator can adopt or reject here: every engine except yue2, which the migration seeds as
// adopted (D-E2-15). Engine names are never taken from free text, so a typo cannot write a log row.
var adoptable = []string{acestep.Name}

// cliOperator is the changed_by of every row this command writes: the CLI, like `runpod arm` (the audit row's principal).
const cliOperator = "system:cli"

// cmdEngines is `metamusic engines list|adopt|reject`. Adopting is the only way an engine other than yue2 receives money
// (D-E2-15/33): it is a human, CLI-only act. There is no REST operation and no MCP tool for it, and list_engines shows its
// hint as text. Each adopt or reject appends one row to engine_adoption_log and one audit row in the same transaction.
func cmdEngines(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	if len(args) == 0 {
		return usageError("engines: expected list|adopt|reject")
	}
	switch args[0] {
	case "list":
		return cmdEnginesList(ctx, args[1:], getenv, s)
	case "adopt":
		return cmdEnginesChange(ctx, "adopt", args[1:], getenv, s)
	case "reject":
		return cmdEnginesChange(ctx, "reject", args[1:], getenv, s)
	case "help", "-h", "--help":
		fmt.Fprint(s.out, enginesUsage)
		return nil
	}
	return usageError("engines: unknown subcommand " + args[0])
}

const enginesUsage = `usage: metamusic engines list
       metamusic engines adopt ENGINE --reason "..."
       metamusic engines reject ENGINE --reason "..."

An engine other than yue2 receives money only after it is adopted. adopt and reject append one row to the append-only adoption
log (history is never rewritten) and one audit row. ENGINE: ` + acestep.Name + `. --reason is required (1 to 500 characters).
`

func cmdEnginesList(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	fs := newFlags("engines list", s)
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return usageError("engines list: unexpected argument " + fs.Arg(0))
	}
	cfg, err := config.LoadOffline(getenv)
	if err != nil {
		return fatal(err)
	}
	st, err := openExistingDB(ctx, cfg)
	if err != nil {
		return fatal(err)
	}
	defer func() { _ = st.Close() }()
	svc := engineadopt.New(engineadopt.Options{Store: st})
	for _, name := range append([]string{engineadopt.YuE2}, adoptable...) {
		a, err := svc.State(ctx, st.R, name)
		if err != nil {
			return fatal(err)
		}
		line := fmt.Sprintf("%s\t%s", name, a.State)
		if a.State != engineadopt.StateNone {
			line += fmt.Sprintf("\tvia %s\tby %s\tat %s", a.Via, a.ChangedBy, a.ChangedAt.UTC().Format(time.RFC3339))
			if a.Reason != nil {
				line += "\t" + *a.Reason
			}
		}
		fmt.Fprintln(s.out, line)
	}
	return nil
}

func cmdEnginesChange(ctx context.Context, verb string, args []string, getenv func(string) string, s streams) error {
	// flags may follow the engine name: `engines adopt acestep15 --reason "..."`
	var engine string
	var flagArgs []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flagArgs = append(flagArgs, args[i])
			if !strings.Contains(args[i], "=") && i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
			continue
		}
		if engine != "" {
			return usageError("engines " + verb + ": unexpected argument " + args[i])
		}
		engine = args[i]
	}
	fs := newFlags("engines "+verb, s)
	reason := fs.String("reason", "", "why (1 to 500 characters)")
	if err := fs.Parse(flagArgs); err != nil {
		return usageError(err.Error())
	}
	if engine == "" {
		return usageError("engines " + verb + ": ENGINE is required (" + strings.Join(adoptable, ", ") + ")")
	}
	if engine == engineadopt.YuE2 {
		return usageError("engines " + verb + ": yue2 is adopted by the seed and cannot be changed")
	}
	if !slices.Contains(adoptable, engine) {
		return usageError("engines " + verb + ": unknown engine " + engine + " (" + strings.Join(adoptable, ", ") + ")")
	}
	if err := checkReason(*reason); err != nil {
		return err
	}
	cfg, err := config.LoadOffline(getenv)
	if err != nil {
		return fatal(err)
	}
	st, err := openExistingDB(ctx, cfg)
	if err != nil {
		return fatal(err)
	}
	defer func() { _ = st.Close() }()
	svc := engineadopt.New(engineadopt.Options{Store: st})
	var res engineadopt.State
	if verb == "adopt" {
		res, err = svc.Adopt(ctx, cliOperator, engine, *reason)
	} else {
		res, err = svc.Reject(ctx, cliOperator, engine, *reason)
	}
	switch {
	case errors.Is(err, engineadopt.ErrReasonRequired):
		return usageError("--reason is required (1 to 500 characters)")
	case err != nil:
		return fatal(err)
	}
	fmt.Fprintf(s.out, "%s: %s\n", res.Engine, res.State)
	return nil
}
