package connectors

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// PRMPath is the only path the protected-resource metadata is served at (RFC 9728 §3.1, the /mcp resource).
const PRMPath = "/.well-known/oauth-protected-resource/mcp"

// PRMHandler serves the RFC 9728 protected-resource metadata of <PublicURL>/mcp (C106). The document is
// marshalled once. Without MM_OIDC_ISSUER it omits authorization_servers and scopes_supported. The handler
// does its own method dispatch: GET/HEAD 200, OPTIONS 204 with CORS, anything else 405 problem.
func PRMHandler(cfg config.Config) http.Handler {
	md := oauthex.ProtectedResourceMetadata{
		Resource:               strings.TrimRight(cfg.PublicURL, "/") + "/mcp",
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "metamusic",
	}
	if cfg.OIDCIssuer != "" {
		md.AuthorizationServers = []string{cfg.OIDCIssuer}
		md.ScopesSupported = append([]string(nil), cfg.OIDCScopes...)
	}
	doc, err := json.Marshal(md)
	if err != nil {
		panic(err) // a struct of strings always marshals
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			h.Set("Content-Type", "application/json")
			h.Set("Cache-Control", "public, max-age=300")
			h.Set("Access-Control-Allow-Origin", "*")
			w.WriteHeader(http.StatusOK)
			if r.Method == http.MethodGet {
				_, _ = w.Write(doc)
			}
		case http.MethodOptions:
			h.Set("Access-Control-Allow-Origin", "*")
			h.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusNoContent)
		default:
			h.Set("Allow", "GET, HEAD, OPTIONS")
			problem.Write(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", nil)
		}
	})
}
