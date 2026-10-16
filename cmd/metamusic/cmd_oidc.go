package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/nicolasacchi/metamusic/internal/connectors"
	"github.com/nicolasacchi/metamusic/internal/oidc"
	"github.com/nicolasacchi/metamusic/internal/redact"
)

// cmdOIDC dispatches the operator's OIDC commands (C114, D-M4a-21).
func cmdOIDC(ctx context.Context, args []string, getenv func(string) string, s streams) error {
	if len(args) == 0 || args[0] != "probe" {
		return usageError("oidc: expected probe")
	}
	return cmdOIDCProbe(ctx, args[1:], getenv, s)
}

const probeUsage = `oidc probe: run the hosted-client OAuth flow against a live server as a probe client.

The probe prints an authorization URL. Open it in a browser, sign in, and paste the full URL the browser
was redirected to (it will not load; copy it from the address bar) followed by Enter. The probe then
verifies the access token locally and makes one round trip to <server>/mcp, which must answer 403
CONNECTOR_FORBIDDEN (the probe client is not allow-listed). Nothing secret is ever printed.`

const (
	probeMaxPaste = 16 << 10
	probeMaxBody  = 64 << 10
	probeTimeout  = 15 * time.Second
)

// cmdOIDCProbe implements `oidc probe` (C114, D-M4a-21).
func cmdOIDCProbe(ctx context.Context, args []string, _ func(string) string, s streams) error {
	fs := newFlags("oidc probe", s)
	fs.Usage = func() { fmt.Fprintln(s.err, probeUsage); fs.PrintDefaults() }
	server := fs.String("server", "", "the metamusic base URL (the public URL)")
	clientID := fs.String("client-id", "", "the probe's OAuth client id")
	secretFile := fs.String("client-secret-file", "", "file holding the client secret (read by the binary; confidential clients only)")
	redirect := fs.String("redirect", "http://127.0.0.1:8765/callback", "the probe client's registered redirect URL")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return usageError("oidc probe: unexpected argument")
	}
	if *server == "" {
		return usageError("oidc probe: --server is required")
	}
	if *clientID == "" {
		return usageError("oidc probe: --client-id is required")
	}
	base := strings.TrimRight(*server, "/")
	creds := &oauthex.ClientCredentials{ClientID: *clientID}
	if *secretFile != "" {
		raw, err := os.ReadFile(*secretFile)
		if err != nil {
			return errors.New("oidc probe: cannot read --client-secret-file")
		}
		creds.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: strings.TrimSpace(string(raw))}
	}
	httpc := &http.Client{Timeout: probeTimeout}

	prm, err := probePRM(ctx, httpc, base)
	if err != nil {
		return err
	}
	if len(prm.AuthorizationServers) == 0 {
		return usageError("oidc probe: the server advertises no authorization server")
	}

	var pkce, issOnRedirect bool
	in := bufio.NewReader(io.LimitReader(s.in, probeMaxPaste))
	h, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		PreregisteredClient: creds, RedirectURL: *redirect, RequestRefreshToken: true, Client: httpc,
		AuthorizationCodeFetcher: func(_ context.Context, a *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			if u, err := url.Parse(a.URL); err == nil {
				pkce = u.Query().Get("code_challenge_method") == "S256" && u.Query().Get("code_challenge") != ""
			}
			fmt.Fprintf(s.out, "Open this URL, sign in, then paste the URL you are redirected to:\n%s\n> ", a.URL)
			line, err := in.ReadString('\n')
			if err != nil && (!errors.Is(err, io.EOF) || strings.TrimSpace(line) == "") {
				return nil, errors.New("no redirect URL was pasted")
			}
			u, err := url.Parse(strings.TrimSpace(line))
			if err != nil || u.Query().Get("code") == "" && u.Query().Get("error") == "" {
				return nil, errors.New("the pasted text is not a redirect URL with a code")
			}
			q := u.Query()
			if e := q.Get("error"); e != "" {
				return nil, errors.New("authorization error " + e)
			}
			issOnRedirect = q.Get("iss") != ""
			return &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}, nil
		},
	})
	if err != nil {
		return fmt.Errorf("oidc probe: %s", redact.Error(err))
	}
	// A synthetic 401 carrying the server's real resource_metadata URL: the handler then discovers the protected
	// resource and the provider exactly like a hosted client.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/mcp", nil)
	chal := http.Header{"Www-Authenticate": {`Bearer resource_metadata="` + base + connectors.PRMPath + `"`}}
	if err := h.Authorize(ctx, req, &http.Response{StatusCode: http.StatusUnauthorized, Header: chal, Body: http.NoBody}); err != nil {
		return fmt.Errorf("oidc probe: authorization failed: %s", redact.Error(err))
	}
	ts, err := h.TokenSource(ctx)
	if err != nil {
		return fmt.Errorf("oidc probe: %s", redact.Error(err))
	}
	tok, err := ts.Token()
	if err != nil {
		return fmt.Errorf("oidc probe: token: %s", redact.Error(err))
	}

	issuer := prm.AuthorizationServers[0]
	hdr, pay := jwtParts(tok.AccessToken)
	v, err := oidc.NewVerifier(oidc.Config{Issuer: issuer, Resource: prm.Resource, Clients: map[string]string{*clientID: "probe"}}, httpc, nil)
	if err != nil {
		return fmt.Errorf("oidc probe: %s", redact.Error(err))
	}
	claims, verr := v.Verify(ctx, tok.AccessToken)
	var missing *oidc.MissingScopeError
	ok := verr == nil
	yes := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	okBad := func(b bool, name string) string {
		if b {
			return name + " ok"
		}
		return name + " FAILED"
	}
	typ, _ := hdr["typ"].(string)
	alg, _ := hdr["alg"].(string)
	kidKnown := verr == nil || errors.As(verr, &missing)
	hasUser := ok && claims.User != ""
	offline := slices.Contains(scopeList(pay), "offline_access")
	if !ok && !kidKnown {
		fmt.Fprintf(s.out, "\ntoken verify: failed (%s)\n", verifyCode(verr))
	}
	w := s.out
	fmt.Fprintln(w)
	fmt.Fprintln(w, okBad(pay["iss"] == issuer, "iss"))
	fmt.Fprintln(w, okBad(audHas(pay["aud"], prm.Resource), "aud"))
	fmt.Fprintf(w, "typ %s\n", strings.ToLower(typ))
	fmt.Fprintf(w, "alg %s\n", alg)
	fmt.Fprintln(w, map[bool]string{true: "kid known", false: "kid unknown"}[kidKnown])
	fmt.Fprintf(w, "scope metamusic: %s\n", yes(slices.Contains(scopeList(pay), oidc.RequiredScope)))
	fmt.Fprintf(w, "mm_user present: %s\n", yes(hasUser))
	fmt.Fprintf(w, "offline_access: %s\n", yes(offline))
	fmt.Fprintf(w, "refresh token: %s\n", yes(tok.RefreshToken != ""))
	fmt.Fprintf(w, "PKCE S256: %s\n", yes(pkce))
	fmt.Fprintf(w, "iss on redirect: %s\n", yes(issOnRedirect))

	sv, svOK := probeServerVerify(ctx, httpc, base, tok.AccessToken)
	fmt.Fprintf(w, "server verify: %s\n", sv)

	all := ok && hasUser && offline && tok.RefreshToken != "" && pkce && issOnRedirect && svOK &&
		pay["iss"] == issuer && audHas(pay["aud"], prm.Resource)
	if !all {
		return errors.New("oidc probe: one or more checks failed")
	}
	return nil
}

// probePRM fetches the server's protected-resource metadata.
func probePRM(ctx context.Context, c *http.Client, base string) (*oauthex.ProtectedResourceMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+connectors.PRMPath, nil)
	if err != nil {
		return nil, usageError("oidc probe: --server is not a valid URL")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return nil, errors.New("oidc probe: cannot reach the server's protected-resource metadata")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc probe: protected-resource metadata answered %d", resp.StatusCode)
	}
	var prm oauthex.ProtectedResourceMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, probeMaxBody)).Decode(&prm); err != nil {
		return nil, errors.New("oidc probe: protected-resource metadata is not valid JSON")
	}
	return &prm, nil
}

// probeServerVerify makes the one round trip and classifies the answer; ok only for 403 CONNECTOR_FORBIDDEN.
func probeServerVerify(ctx context.Context, c *http.Client, base, token string) (string, bool) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"metamusic-oidc-probe","version":"0"}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/mcp", strings.NewReader(body))
	if err != nil {
		return "unreachable", false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	nr := *c
	nr.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := nr.Do(req)
	if err != nil {
		return "unreachable", false
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, probeMaxBody))
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &p)
	switch {
	case resp.StatusCode == http.StatusForbidden && p.Code == "CONNECTOR_FORBIDDEN":
		return "CONNECTOR_FORBIDDEN (ok)", true
	case resp.StatusCode == http.StatusUnauthorized:
		return "invalid_token", false
	case resp.StatusCode == http.StatusServiceUnavailable:
		return "keys unavailable", false
	}
	return fmt.Sprintf("unexpected %d", resp.StatusCode), false
}

func verifyCode(err error) string {
	switch {
	case errors.Is(err, oidc.ErrUnavailable):
		return "keys unavailable"
	case errors.Is(err, oidc.ErrInvalid):
		return "invalid token"
	}
	return "error"
}

// jwtParts decodes header and payload without verifying (display only; the verifier decides).
func jwtParts(tok string) (hdr, pay map[string]any) {
	segs := strings.Split(tok, ".")
	if len(segs) != 3 {
		return map[string]any{}, map[string]any{}
	}
	dec := func(s string) map[string]any {
		raw, err := base64.RawURLEncoding.DecodeString(s)
		m := map[string]any{}
		if err == nil {
			d := json.NewDecoder(bytes.NewReader(raw))
			_ = d.Decode(&m)
		}
		return m
	}
	return dec(segs[0]), dec(segs[1])
}

func audHas(raw any, want string) bool {
	switch a := raw.(type) {
	case string:
		return a == want
	case []any:
		for _, x := range a {
			if s, ok := x.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

func scopeList(pay map[string]any) []string {
	var out []string
	if s, ok := pay["scope"].(string); ok {
		out = append(out, strings.Fields(s)...)
	}
	if l, ok := pay["scp"].([]any); ok {
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
