package mcpgen

import (
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/reqinfo"
)

func TestResources_SpecsValid(t *testing.T) {
	t.Parallel()
	good := ResourceSpec{URITemplate: "metamusic://items/{id}/x", Name: "ok", Op: "get_item", MIMEType: "application/json"}
	for _, tc := range []struct {
		name string
		spec ResourceSpec
		want string // substring of the error; "" = valid
	}{
		{"valid", good, ""},
		{"valid generic op", ResourceSpec{URITemplate: "metamusic://g/{id}", Name: "g", Op: "generic_op"}, ""},
		{"unknown op", ResourceSpec{URITemplate: good.URITemplate, Name: "u", Op: "nope"}, "does not exist"},
		{"excluded op", ResourceSpec{URITemplate: good.URITemplate, Name: "e", Op: "hidden_op"}, "does not exist or is excluded"},
		{"not GET", ResourceSpec{URITemplate: good.URITemplate, Name: "p", Op: "add_note"}, "not GET"},
		{"no id path parameter", ResourceSpec{URITemplate: good.URITemplate, Name: "n", Op: "list_tags"}, "path parameter id"},
		{"template without id", ResourceSpec{URITemplate: "metamusic://items/{name}", Name: "t", Op: "get_item"}, "{id}"},
		{"template with two variables", ResourceSpec{URITemplate: "metamusic://{id}/{x}", Name: "t2", Op: "get_item"}, "{id}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, mux := testAPI(t)
			_, err := newServerWith(a, mux, "v0", reqinfo.MCP, nil, []ResourceSpec{tc.spec})
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("error %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestIDFromURI(t *testing.T) {
	t.Parallel()
	const id = "0190000a-0000-7000-8000-00000000010b"
	const tpl = "metamusic://tracks/{id}/context"
	for uri, want := range map[string]string{
		"metamusic://tracks/" + id + "/context":                  id,
		"metamusic://tracks/" + id + "/spectrogram":              "",
		"metamusic://tracks/" + strings.ToUpper(id) + "/context": "",
		"metamusic://tracks/" + id + "x/context":                 "",
		"metamusic://tracks/../" + id + "/context":               "",
		"metamusic://tracks//context":                            "",
		"metamusic://tracks/" + id + "/context?x=1":              "",
		"http://tracks/" + id + "/context":                       "",
		"":                                                       "",
	} {
		if got := idFromURI(tpl, uri); got != want {
			t.Errorf("idFromURI(%q) = %q, want %q", uri, got, want)
		}
	}
}

func TestProblemError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{403, `{"code":"FORBIDDEN_SCOPE","message":"secret detail"}`, "FORBIDDEN_SCOPE"},
		{500, `not json`, "INTERNAL"},
		{404, `{"code":"NOT_FOUND"}`, "NOT_FOUND"},
	} {
		err := problemError(tc.status, []byte(tc.body))
		if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret detail") {
			t.Errorf("problemError(%d, %s) = %v", tc.status, tc.body, err)
		}
	}
}
