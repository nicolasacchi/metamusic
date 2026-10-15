package servertest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	mmauth "github.com/nicolasacchi/metamusic/internal/auth"
)

// ConnectorOpts select the hosted-connector client of Rig.Connector (C117.1).
type ConnectorOpts struct {
	Client string         // default "test-claude"
	Scopes []mmauth.Scope // set through set_connector_grant after the first call
}

// refreshSkew mirrors x/oauth2's expiryDelta: a token is refreshed 10 s before it expires.
const refreshSkew = 10 * time.Second

// rigTokenSource is the rig-clock token source (C117.1). go-sdk re-authorizes (a full authorization-code flow) on a
// 401/403 and never refreshes by itself, and x/oauth2 refreshes only by the wall clock. This source keeps the
// current token and, on Token(), refreshes through the refresh_token grant when the rig clock has reached exp - 10 s,
// where exp is the access token's own claim (decoded without verification: test-only).
type rigTokenSource struct {
	ctx context.Context
	cfg *oauth2.Config
	now func() time.Time

	mu  sync.Mutex
	tok *oauth2.Token
}

func (s *rigTokenSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if exp, ok := jwtExp(s.tok.AccessToken); ok && s.tok.RefreshToken != "" && !s.now().Before(exp.Add(-refreshSkew)) {
		next, err := s.cfg.TokenSource(s.ctx, &oauth2.Token{RefreshToken: s.tok.RefreshToken}).Token()
		if err != nil {
			return nil, err
		}
		if next.RefreshToken == "" {
			next.RefreshToken = s.tok.RefreshToken
		}
		s.tok = next
	}
	cp := *s.tok
	return &cp, nil
}

func jwtExp(tok string) (time.Time, bool) {
	segs := strings.Split(tok, ".")
	if len(segs) != 3 {
		return time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(segs[1])
	if err != nil {
		return time.Time{}, false
	}
	var c struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &c) != nil || c.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(c.Exp, 0), true
}

// Connector connects a go-sdk MCP client over <PublicURL>/mcp with the OAuth authorization-code handler against the
// rig's stub provider and returns the session and the grant id (C117.1). It needs Options.OIDC. The client is
// pre-registered (test-claude public, test-chatgpt confidential client_secret_post), requests a refresh token, uses the
// rig's plain http.Client and the rig-clock token source. With opts.Scopes the grant's scopes are then set through
// set_connector_grant as the human.
func (r *Rig) Connector(tb testing.TB, o ConnectorOpts) (*mcp.ClientSession, string) {
	tb.Helper()
	if r.OIDC == nil {
		tb.Fatal("servertest: Connector needs Options.OIDC")
	}
	client := o.Client
	if client == "" {
		client = OIDCClaudeClient
	}
	creds := &oauthex.ClientCredentials{ClientID: client}
	redirect := claudeRedirect
	switch client {
	case OIDCChatGPTClient:
		creds.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: chatgptSecret()}
		redirect = chatgptRedirect
	case OIDCProbeClient:
		redirect = probeRedirect
	}
	httpc := &http.Client{}
	h, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		PreregisteredClient: creds, RedirectURL: redirect, AuthorizationCodeFetcher: r.OIDC.Fetcher(tb),
		RequestRefreshToken: true, Client: httpc,
		NewTokenSource: func(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			return &rigTokenSource{ctx: ctx, cfg: cfg, now: r.Clock.Now, tok: tok}, nil
		},
	})
	if err != nil {
		tb.Fatalf("servertest: oauth handler: %v", err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "servertest-" + client, Version: "0"}, nil)
	sess, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: r.URL + "/mcp", HTTPClient: httpc, MaxRetries: -1, OAuthHandler: h, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		tb.Fatalf("servertest: connect %s: %v", client, err)
	}
	tb.Cleanup(func() { _ = sess.Close() })
	id := ""
	grants, err := r.Services.Connectors.List(context.Background())
	if err != nil {
		tb.Fatal(err)
	}
	for _, g := range grants { // the newest grant of this client and the e2e subject
		if g.ClientID == client && g.Subject == OIDCSubject {
			id = g.ID
			break
		}
	}
	if id == "" {
		tb.Fatalf("servertest: no connector grant for %s after connecting", client)
	}
	if o.Scopes != nil {
		scopes := make([]string, len(o.Scopes))
		for i, sc := range o.Scopes {
			scopes[i] = string(sc)
		}
		if resp := r.Human(tb).Do(tb, "PUT", "/api/connectors/"+id, map[string]any{"scopes": scopes}); resp.Status != http.StatusOK {
			tb.Fatalf("servertest: set_connector_grant: %d %s", resp.Status, resp.Body)
		}
	}
	return sess, id
}
