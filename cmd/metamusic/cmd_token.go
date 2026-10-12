package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

func newFlags(name string, s streams) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(s.err)
	return fs
}

func cmdToken(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	if len(args) == 0 {
		return usageError("token: expected create|list|revoke")
	}
	switch args[0] {
	case "create":
		fs := newFlags("token create", s)
		name := fs.String("name", "", "token name")
		scopes := fs.String("scopes", "read", "comma-separated scopes (read,write,generate,admin)")
		if err := fs.Parse(args[1:]); err != nil {
			return usageError(err.Error())
		}
		if strings.TrimSpace(*name) == "" {
			return usageError("token create: --name is required")
		}
		sc, _ := parseScopes(*scopes)
		b, err := boot(ctx, getenv, false)
		if err != nil {
			return err
		}
		defer b.close()
		plain, id, err := b.tokens.Create(ctx, *name, sc)
		if err != nil {
			if errors.Is(err, auth.ErrInvalidScope) {
				return fmt.Errorf("invalid name or scopes %q", *scopes)
			}
			return err
		}
		fmt.Fprintln(s.out, plain)
		fmt.Fprintf(s.err, "token id: %s (the token above is shown only once)\n", id)
	case "list":
		b, err := boot(ctx, getenv, false)
		if err != nil {
			return err
		}
		defer b.close()
		list, err := b.tokens.List(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(s.out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tPREFIX\tSCOPES\tCREATED\tLAST USED\tREVOKED")
		for _, t := range list {
			var sc []string
			for _, x := range t.Scopes {
				sc = append(sc, string(x))
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.Name, t.Prefix, strings.Join(sc, ","),
				fmtTime(t.CreatedAt), fmtTime(t.LastUsedAt), fmtTime(t.RevokedAt))
		}
		return tw.Flush()
	case "revoke":
		if len(args) != 2 {
			return usageError("token revoke: expected exactly one ID")
		}
		b, err := boot(ctx, getenv, false)
		if err != nil {
			return err
		}
		defer b.close()
		if err := b.tokens.Revoke(ctx, args[1]); err != nil {
			if errors.Is(err, auth.ErrNotFound) {
				return fmt.Errorf("no such token %q", args[1])
			}
			return err
		}
		fmt.Fprintln(s.out, "revoked")
	default:
		return usageError(fmt.Sprintf("token: unknown subcommand %q", args[0]))
	}
	return nil
}

const maxPasswordBytes = 4096

func cmdUser(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	if len(args) == 0 || args[0] != "set-password" {
		return usageError("user: expected set-password")
	}
	fs := newFlags("user set-password", s)
	username := fs.String("username", "", "username")
	if err := fs.Parse(args[1:]); err != nil {
		return usageError(err.Error())
	}
	if strings.TrimSpace(*username) == "" {
		return usageError("user set-password: --username is required")
	}
	b, err := boot(ctx, getenv, false)
	if err != nil {
		return err
	}
	defer b.close()
	if b.cfg.AuthMode != "local" {
		return errors.New("user set-password only applies to MM_AUTH=local")
	}
	raw, err := io.ReadAll(io.LimitReader(s.in, maxPasswordBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxPasswordBytes {
		return fmt.Errorf("password exceeds %d bytes", maxPasswordBytes)
	}
	pw := strings.TrimRight(string(raw), "\r\n")
	if err := b.local.SetPassword(ctx, *username, pw); err != nil {
		if errors.Is(err, auth.ErrInvalidPassword) {
			return errors.New("username and password must be non-blank")
		}
		return err
	}
	fmt.Fprintln(s.out, "password set; existing sessions invalidated")
	return nil
}

func fmtTime(v any) string {
	switch t := v.(type) {
	case time.Time:
		if t.IsZero() {
			return "-"
		}
		return t.UTC().Format(time.RFC3339)
	case *time.Time:
		if t == nil {
			return "-"
		}
		return fmtTime(*t)
	case string:
		if t == "" {
			return "-"
		}
		return t
	case *string:
		if t == nil {
			return "-"
		}
		return fmtTime(*t)
	}
	return fmt.Sprint(v)
}
