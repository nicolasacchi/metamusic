// Command metamusic is the single-binary entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// Set via -ldflags "-X main.version=… -X main.commit=…".
var (
	version = "dev"
	commit  = "none"
)

const usage = `usage: metamusic <command>

commands:
  serve                          run the server (requires MM_AUTH=proxy|local)
  dev                            serve in local-auth mode with data in ./data/dev
  openapi                        print the OpenAPI document
  healthcheck                    GET /healthz on MM_ADDR; exit 0 when healthy
  version                        print version
  token create --name N --scopes read,write
  token list
  token revoke ID
  user set-password --username U (password read from stdin)
  mcp-stdio                      serve MCP on stdin/stdout as the agent owning MM_TOKEN (no MM_AUTH needed)
  export (--source PATH | --tracks | --drafts) [--for-batch] [--out DIR]
                                 write songs.jsonl for a selection (exit 1 when the validator reports issues)
  validate-batch FILE            check a songs.jsonl like yue2 batch (exit 0 ok, 1 issues, 2 usage)
  backup [--to DIR] | backup list
                                 take a snapshot now, or list snapshots
  restore --from SNAPSHOT --into DIR
                                 rebuild a data directory from a snapshot
  import [--dry-run] [--plan REPORT] [--yes] [--inbox DIR] [--mapping FILE] [--report FILE]
         [--mapping-template FILE] [--min-age 2m] [--jobs 2] [--json]
                                 inventory the inbox (--dry-run), then apply the reviewed plan (--plan)
  seed-synthetic [--profile dev|e2e] [--seed 1] [--force]
                                 fill a dev or e2e data directory with a generated library
dev flags: --data-dir DIR  --seed-synthetic

import exit codes: 0 ok, 1 per-file errors, 2 usage or fatal, 3 PLAN_CHANGED, 4 IMPORT_IN_PROGRESS
`

func main() {
	// The process-wide logger is configured exactly once, here, never inside
	// run(): run() is re-entered concurrently by tests, and a per-invocation
	// slog.SetDefault would route other invocations' (and the MCP SDK's
	// background goroutines') log lines into this invocation's writer.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

type streams struct {
	in  io.Reader
	out io.Writer
	err io.Writer
}

// run executes one CLI invocation and returns the process exit code.
func run(ctx context.Context, args []string, getenv func(string) string, in io.Reader, out, errw io.Writer) int {
	s := streams{in, out, errw}
	if len(args) == 0 {
		fmt.Fprint(errw, usage)
		return 2
	}
	rest := args[1:]
	var err error
	switch args[0] {
	case "version":
		fmt.Fprintf(out, "metamusic %s (%s)\n", version, commit)
	case "openapi":
		err = cmdOpenAPI(s)
	case "healthcheck":
		err = cmdHealthcheck(ctx, getenv, s)
	case "serve":
		err = cmdServe(ctx, getenv, s)
	case "dev":
		err = cmdDev(ctx, rest, getenv, s)
	case "token":
		err = cmdToken(ctx, rest, getenv, s)
	case "user":
		err = cmdUser(ctx, rest, getenv, s)
	case "mcp-stdio":
		err = cmdMCPStdio(ctx, rest, getenv, s)
	case "export":
		err = cmdExport(ctx, rest, getenv, s)
	case "validate-batch":
		err = cmdValidateBatch(ctx, rest, s)
	case "backup":
		err = cmdBackup(ctx, rest, getenv, s)
	case "restore":
		err = cmdRestore(ctx, rest, getenv, s)
	case "import":
		err = cmdImport(ctx, rest, getenv, s)
	case "seed-synthetic":
		err = cmdSeedSynthetic(ctx, rest, getenv, s)
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
	default:
		fmt.Fprintf(errw, "metamusic: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	if err != nil {
		var x exitError
		if errors.As(err, &x) {
			if x.err != nil {
				fmt.Fprintf(errw, "metamusic: %v\n", x.err)
			}
			return x.code
		}
		var e usageError
		if errors.As(err, &e) {
			fmt.Fprintf(errw, "metamusic: %s\n", e)
			return 2
		}
		fmt.Fprintf(errw, "metamusic: %v\n", err)
		return 1
	}
	return 0
}

type usageError string

func (e usageError) Error() string { return string(e) }

// exitError carries a specific process exit code (import: 1 per-file errors,
// 2 fatal, 3 PLAN_CHANGED, 4 IMPORT_IN_PROGRESS). A nil err prints nothing:
// the command has already reported on stdout.
type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit %d", e.code)
	}
	return e.err.Error()
}

func (e exitError) Unwrap() error { return e.err }

// fatal wraps err as a fatal (exit 2) error.
func fatal(err error) error { return exitError{2, err} }
