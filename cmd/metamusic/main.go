// Command metamusic is the single-binary entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
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
`

func main() {
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
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
	default:
		fmt.Fprintf(errw, "metamusic: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	if err != nil {
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
