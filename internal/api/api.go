package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// SpecVersion is the OpenAPI document version. It is fixed (not the build
// version) so the committed openapi.json is reproducible.
const SpecVersion = "0.1.0"

// Deps are the collaborators operations need.
type Deps struct {
	Store           *store.Store
	Tokens          *auth.TokenStore
	Cfg             config.Config
	Version, Commit string
	Started         time.Time
}

func sortOps(ops []OpInfo) {
	slices.SortFunc(ops, func(a, b OpInfo) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return strings.Compare(a.Method, b.Method)
	})
}

// New builds the Huma API on mux and registers every operation.
func New(mux *http.ServeMux, d Deps) huma.API {
	cfg := huma.DefaultConfig("metamusic", SpecVersion)
	cfg.OpenAPIPath = "/api/openapi"
	cfg.DocsPath = "/api/docs"
	cfg.SchemasPath = ""
	cfg.CreateHooks = nil
	a := humago.New(mux, cfg)
	registerSystem(a, d)
	notFound(mux, "/api/")
	notFound(mux, "/human/api/")
	return a
}

// notFound makes unknown paths and wrong methods under prefix return problem+json.
func notFound(mux *http.ServeMux, prefix string) {
	mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		var allow []string
		for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			probe := r.Clone(r.Context())
			probe.Method = m
			if _, pat := mux.Handler(probe); pat != "" && pat != prefix {
				allow = append(allow, m)
			}
		}
		if len(allow) > 0 {
			w.Header().Set("Allow", strings.Join(allow, ", "))
			problem.Write(w, 405, "METHOD_NOT_ALLOWED", "method not allowed", map[string]any{"allow": allow})
			return
		}
		problem.Write(w, 404, "NOT_FOUND", "no such resource", nil)
	})
}

// OpenAPIJSON renders the spec deterministically (sorted keys, 2-space indent, trailing newline).
func OpenAPIJSON(a huma.API) ([]byte, error) {
	raw, err := json.Marshal(a.OpenAPI())
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}
