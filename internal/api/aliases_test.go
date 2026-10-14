package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
)

type aliasIn struct {
	Body struct {
		Name string `json:"name"`
	}
}
type aliasInSame struct { // another Go type of the same shape
	Body struct {
		Name string `json:"name"`
	}
}
type aliasInDiff struct {
	Body struct {
		Name string `json:"name"`
		Plus int    `json:"plus"`
	}
}
type aliasOut struct {
	Body struct {
		OK bool `json:"ok"`
	}
}
type aliasOutDiff struct {
	Body struct {
		OK   bool `json:"ok"`
		More int  `json:"more"`
	}
}

func spendOp(id, path string) huma.Operation {
	return huma.Operation{OperationID: id, Method: "POST", Path: path, Summary: "s", Description: "d"}
}

func aliasMeta(of string) api.Meta {
	return api.Meta{Scope: auth.ScopeGenerate, Cost: "spend", MCP: "exclude:human-only", UI: "none:x", HumanOnly: true, Idempotency: "create", AliasOf: of}
}

func regTarget(a huma.API) {
	api.Register(a, spendOp("create_thing", "/api/thing"), api.Meta{Scope: auth.ScopeGenerate, Cost: "spend", MCP: "tool", UI: "none:x", Idempotency: "create"},
		func(context.Context, *aliasIn) (*aliasOut, error) { return &aliasOut{}, nil })
}

func TestAliases_SchemasEqual(t *testing.T) {
	t.Parallel()
	// identical shapes of distinct Go types pass
	a := humago.New(http.NewServeMux(), huma.DefaultConfig("t", "1"))
	regTarget(a)
	api.Register(a, spendOp("create_thing_human", "/human/api/thing"), aliasMeta("create_thing"),
		func(context.Context, *aliasInSame) (*aliasOut, error) { return &aliasOut{}, nil })
	api.ValidateAliases(a) // must not panic

	for name, reg := range map[string]func(huma.API){
		"request differs": func(a huma.API) {
			api.Register(a, spendOp("create_thing_human", "/human/api/thing"), aliasMeta("create_thing"),
				func(context.Context, *aliasInDiff) (*aliasOut, error) { return &aliasOut{}, nil })
		},
		"response differs": func(a huma.API) {
			api.Register(a, spendOp("create_thing_human", "/human/api/thing"), aliasMeta("create_thing"),
				func(context.Context, *aliasIn) (*aliasOutDiff, error) { return &aliasOutDiff{}, nil })
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := humago.New(http.NewServeMux(), huma.DefaultConfig("t", "1"))
			regTarget(a)
			reg(a)
			defer func() {
				r := recover()
				if r == nil || !strings.Contains(r.(string), "differs") {
					t.Fatalf("recover = %v", r)
				}
			}()
			api.ValidateAliases(a)
		})
	}
}

func TestValidateAliases_Dangling(t *testing.T) {
	t.Parallel()
	a := humago.New(http.NewServeMux(), huma.DefaultConfig("t", "1"))
	regTarget(a)
	api.Register(a, spendOp("create_other_human", "/human/api/other"), aliasMeta("create_missing"),
		func(context.Context, *aliasIn) (*aliasOut, error) { return &aliasOut{}, nil })
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(r.(string), "does not exist") {
			t.Fatalf("recover = %v", r)
		}
	}()
	api.ValidateAliases(a)
}

func TestValidateAliases_NoAliasesIsFine(t *testing.T) {
	t.Parallel()
	api.ValidateAliases(newAPI())
}
