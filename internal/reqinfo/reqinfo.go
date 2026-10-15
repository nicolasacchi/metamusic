// Package reqinfo carries request facts (transport, client label) in the context.
package reqinfo

import (
	"context"
	"net"
	"net/http"
	"net/netip"
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
	// Runner marks audit rows written by the job runner and reconciler (M3 D-M3-22).
	Runner Transport = "runner"
	// Subsonic and OAuth mark the OpenSubsonic bridge and hosted-connector requests (M4a C102).
	Subsonic Transport = "subsonic"
	OAuth    Transport = "oauth"
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

// IsSPARequest reports whether r carries the SPA client label: the
// X-MM-Client: spa header or the (undeclared) mm_client=spa query flag used
// by plain links such as downloads. Callers decide whether the principal may
// claim it (an agent never can).
func IsSPARequest(r *http.Request) bool {
	return r.Header.Get("X-MM-Client") == "spa" || r.URL.Query().Get("mm_client") == "spa"
}

type shutdownKey struct{}

// WithShutdown attaches the server's shutdown signal (the Done channel of its base context) to a
// request context, so long-polling handlers return as soon as shutdown starts (M3 C75.8).
func WithShutdown(ctx context.Context, done <-chan struct{}) context.Context {
	return context.WithValue(ctx, shutdownKey{}, done)
}

// ShutdownDone returns the shutdown channel of ctx; nil (which never fires) when none is attached.
func ShutdownDone(ctx context.Context) <-chan struct{} {
	d, _ := ctx.Value(shutdownKey{}).(<-chan struct{})
	return d
}

// ClientIP returns RemoteAddr's address unless it lies inside a trusted prefix; then the right-most
// X-Forwarded-For entry that is not itself trusted (all trusted or unparsable ⇒ RemoteAddr's address).
// It never trusts X-Forwarded-For from an untrusted peer. An unparsable RemoteAddr yields the zero Addr.
func ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	peer := parseHostAddr(r.RemoteAddr)
	if !peer.IsValid() || !inPrefixes(peer, trusted) {
		return peer
	}
	var entries []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		entries = append(entries, strings.Split(line, ",")...)
	}
	for i := len(entries) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(entries[i]))
		if err != nil {
			return peer
		}
		a = a.Unmap()
		if !inPrefixes(a, trusted) {
			return a
		}
	}
	return peer
}

func parseHostAddr(hostport string) netip.Addr {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

func inPrefixes(a netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
