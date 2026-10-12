// Package reqinfo carries request facts (transport, client label) in the context.
package reqinfo

import (
	"context"
	"strings"
)

// Transport names the face a request arrived through.
type Transport string

const (
	REST  Transport = "rest"
	SPA   Transport = "spa"
	MCP   Transport = "mcp"
	Stdio Transport = "stdio"
	CLI   Transport = "cli"
	Pair  Transport = "pair"
)

type (
	transportKey struct{}
	clientKey    struct{}
)

const maxClient = 120

func WithTransport(ctx context.Context, t Transport) context.Context {
	return context.WithValue(ctx, transportKey{}, t)
}

func TransportFrom(ctx context.Context) (Transport, bool) {
	t, ok := ctx.Value(transportKey{}).(Transport)
	return t, ok
}

// WithClient stores the sanitised label.
func WithClient(ctx context.Context, label string) context.Context {
	return context.WithValue(ctx, clientKey{}, SanitizeClient(label))
}

func Client(ctx context.Context) string {
	s, _ := ctx.Value(clientKey{}).(string)
	return s
}

// SanitizeClient keeps the first product token of a User-Agent-like string,
// printable ASCII only, at most 120 bytes; "" if none.
func SanitizeClient(s string) string {
	fields := strings.Fields(s)
	for _, f := range fields {
		var b strings.Builder
		for i := 0; i < len(f) && b.Len() < maxClient; i++ {
			if c := f[i]; c > 0x20 && c < 0x7f {
				b.WriteByte(c)
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	return ""
}
