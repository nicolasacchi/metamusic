package api_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
)

type encBody struct {
	Title  string            `json:"title"`
	Amount int64             `json:"amount_micro_usd"`
	Notes  []string          `json:"notes"`
	Opt    *string           `json:"opt,omitempty"`
	Nested map[string]any    `json:"nested"`
	Tags   map[string]string `json:"tags,omitempty"`
}

type encOut struct{ Body encBody }

func TestEncodeResponse_MatchesHumaBody(t *testing.T) {
	t.Parallel()
	val := encBody{
		Title: `Café <b>&</b> "quoted" \ unicode ☃  `, Amount: 326217, Notes: nil,
		Nested: map[string]any{"z": 1.5, "a": []int{1, 2}, "html": "<script>&</script>"},
		Tags:   map[string]string{"b": "2", "a": "1"},
	}
	mux := http.NewServeMux()
	a := humago.New(mux, api.HumaConfig())
	api.Register(a, huma.Operation{OperationID: "enc_probe", Method: "GET", Path: "/api/enc", Summary: "s", Description: "d"},
		api.Meta{Scope: auth.ScopeRead, Cost: "free", MCP: "tool", UI: "none:x"},
		func(context.Context, *struct{}) (*encOut, error) { return &encOut{Body: val}, nil })
	r := httptest.NewRequest("GET", "/api/enc", nil).WithContext(auth.WithPrincipal(context.Background(),
		auth.Principal{Kind: auth.KindAgent, Scopes: []auth.Scope{auth.ScopeRead}}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	got, err := api.EncodeResponse(val)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, w.Body.Bytes()) {
		t.Fatalf("EncodeResponse differs from Huma's body:\n got %q\nwant %q", got, w.Body.Bytes())
	}
	if _, err := api.EncodeResponse(make(chan int)); err == nil {
		t.Error("unencodable value accepted")
	}
}

func TestRefFor_SystemKeys(t *testing.T) {
	t.Parallel()
	human := auth.Principal{Kind: auth.KindHuman, ID: "h", Name: "h"}
	agent := auth.Principal{Kind: auth.KindAgent, ID: "a", Name: "a"}
	device := auth.Principal{Kind: auth.KindDevice, ID: "d", Name: "d"}
	for _, viewer := range []auth.Principal{human, agent, device} {
		for _, name := range []string{"runner", "reconciler", "billing", "cli", "job"} {
			got := api.RefFor(viewer, "system:"+name, "system", name)
			want := api.PrincipalRef{Kind: "system", ID: "system:" + name, Name: name}
			if got != want {
				t.Errorf("viewer %s, %s: %+v, want %+v", viewer.Kind, name, got, want)
			}
		}
	}
	// the name argument is not trusted for system keys
	if got := api.RefFor(agent, "system:runner", "system", "whatever"); got.Name != "runner" {
		t.Errorf("name = %q", got.Name)
	}
	// non-system behaviour is unchanged
	if got := api.RefFor(agent, "human:h", "human", "h"); got.ID != "human" {
		t.Errorf("human for agent: %+v", got)
	}
	if got := api.KeyFor(agent, "system:runner"); got != "system:runner" {
		t.Errorf("KeyFor system = %q", got)
	}
}
