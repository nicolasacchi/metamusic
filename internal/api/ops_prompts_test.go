package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// promptRig serves the registry over the rig's real services with the real prompt sources (app.Build wires
// them in M4a-B3; until then the rig carries a placeholder).
func promptRig(t *testing.T) *rig {
	t.Helper()
	sr := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, M4a: true, Files: true})
	d := sr.Services.Deps
	d.Prompts = api.NewPromptSources(d.Library, d.Context, d.Listening)
	mux := http.NewServeMux()
	api.New(mux, d)
	return &rig{mux: mux}
}

var (
	readAgent = auth.Principal{Kind: auth.KindAgent, ID: "tok-r", Name: "r", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}, Via: "token"}
	genAgent  = auth.Principal{Kind: auth.KindAgent, ID: "tok-g", Name: "g", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate}, Via: "token"}
)

type promptBody struct {
	Name, Title, Version string
	Arguments            []struct{ Name string }
	Messages             []struct {
		Role    string
		Content struct {
			Type, Text string
			Resource   *struct {
				URI      string `json:"uri"`
				MIMEType string `json:"mime_type"`
				Text     string `json:"text"`
			} `json:"resource"`
		}
	}
}

func getPrompt(t *testing.T, r *rig, p auth.Principal, path string) (int, promptBody, []byte) {
	t.Helper()
	rec := r.callWith(p, "GET", path, "")
	var b promptBody
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, b, rec.Body.Bytes()
}

// TestPrompts_CritiqueEmbedsContext: the embedded resource is the REST body of get_track_context, byte for byte.
func TestPrompts_CritiqueEmbedsContext(t *testing.T) {
	t.Parallel()
	r := promptRig(t)
	code, b, raw := getPrompt(t, r, readAgent, "/api/prompts/critique_track?track_id="+lt.TrackLuna)
	if code != 200 || len(b.Messages) != 2 || b.Name != "critique_track" || b.Version != "1" || len(b.Arguments) != 1 {
		t.Fatalf("%d %s", code, raw)
	}
	want := r.callWith(readAgent, "GET", "/api/tracks/"+lt.TrackLuna+"/context", "")
	res := b.Messages[1].Content
	if want.Code != 200 || res.Type != "resource" || res.Resource == nil || res.Resource.Text != want.Body.String() ||
		res.Resource.URI != "metamusic://tracks/"+lt.TrackLuna+"/context" || res.Resource.MIMEType != "application/json" {
		t.Errorf("resource differs from get_track_context: %+v", res)
	}
	if !strings.Contains(b.Messages[0].Content.Text, "Luna Park") {
		t.Errorf("text: %s", b.Messages[0].Content.Text)
	}
}

func TestPrompts_SessionReview(t *testing.T) {
	t.Parallel()
	r := promptRig(t)
	since := lt.FixedNow.Add(-time.Hour).Format(time.RFC3339)
	code, b, raw := getPrompt(t, r, readAgent, "/api/prompts/session_review?since="+since)
	if code != 200 || len(b.Messages) != 1 {
		t.Fatalf("%d %s", code, raw)
	}
	txt := b.Messages[0].Content.Text
	// the fixture human played Luna twice and completed it once
	for _, want := range []string{`"track_id": "` + lt.TrackLuna + `"`, `"plays": 2`, `"completed": 1`, `"stars": 5`} {
		if !strings.Contains(txt, want) {
			t.Errorf("session text lacks %s:\n%s", want, txt)
		}
	}
	// no since: the default window (12 h) includes the same events
	if code, b, raw := getPrompt(t, r, readAgent, "/api/prompts/session_review"); code != 200 || !strings.Contains(b.Messages[0].Content.Text, lt.TrackLuna) {
		t.Errorf("default window: %d %s", code, raw)
	}
}

func TestPrompts_UnknownTrack404(t *testing.T) {
	t.Parallel()
	r := promptRig(t)
	for _, n := range []string{"critique_track", "write_variant"} {
		rec := r.callWith(readAgent, "GET", "/api/prompts/"+n+"?track_id=nope", "")
		if p := decodeProblem(t, rec.Body.Bytes()); rec.Code != 404 || p["code"] != "NOT_FOUND" {
			t.Errorf("%s: %d %s", n, rec.Code, rec.Body)
		}
	}
}

func TestPrompts_WriteVariantRefusesReferenceTrack(t *testing.T) {
	t.Parallel()
	r := promptRig(t)
	for _, id := range []string{lt.TrackRefFlac, lt.TrackLossy} {
		rec := r.callWith(genAgent, "GET", "/api/prompts/write_variant?track_id="+id, "")
		p := decodeProblem(t, rec.Body.Bytes())
		if rec.Code != 422 || p["code"] != "RECIPE_UNKNOWN" {
			t.Errorf("%s: %d %s", id, rec.Code, rec.Body)
		}
	}
	// the critique of a reference track is allowed
	if code, _, raw := getPrompt(t, r, genAgent, "/api/prompts/critique_track?track_id="+lt.TrackRefFlac); code != 200 {
		t.Errorf("critique of a reference track: %d %s", code, raw)
	}
}

// TestPrompts_WriteVariantDependsOnScope: the ending follows the caller's generate scope.
func TestPrompts_WriteVariantDependsOnScope(t *testing.T) {
	t.Parallel()
	r := promptRig(t)
	_, with, _ := getPrompt(t, r, genAgent, "/api/prompts/write_variant?track_id="+lt.TrackLuna+"&goal=calmer")
	_, without, _ := getPrompt(t, r, readAgent, "/api/prompts/write_variant?track_id="+lt.TrackLuna)
	if len(with.Messages) != 2 || len(without.Messages) != 2 {
		t.Fatalf("messages: %d %d", len(with.Messages), len(without.Messages))
	}
	if !strings.Contains(with.Messages[0].Content.Text, "estimate_variant") || !strings.Contains(with.Messages[0].Content.Text, "calmer") {
		t.Errorf("with generate: %s", with.Messages[0].Content.Text)
	}
	if strings.Contains(without.Messages[0].Content.Text, "estimate_variant") {
		t.Errorf("without generate: %s", without.Messages[0].Content.Text)
	}
}

func TestGetPrompt_Validation(t *testing.T) {
	t.Parallel()
	r := promptRig(t)
	old := lt.FixedNow.Add(-31 * 24 * time.Hour).Format(time.RFC3339)
	for _, c := range []struct {
		path string
		code string
	}{
		{"/api/prompts/critique_track", "VALIDATION_FAILED"},
		{"/api/prompts/write_variant", "VALIDATION_FAILED"},
		{"/api/prompts/session_review?since=" + old, "VALIDATION_FAILED"},
		{"/api/prompts/session_review?since=yesterday", "VALIDATION_FAILED"},
		{"/api/prompts/nope?track_id=" + lt.TrackLuna, ""},
		{"/api/prompts/write_variant?track_id=" + lt.TrackLuna + "&goal=" + strings.Repeat("g", 501), ""},
	} {
		rec := r.callWith(readAgent, "GET", c.path, "")
		p := decodeProblem(t, rec.Body.Bytes())
		if rec.Code != 422 || (c.code != "" && p["code"] != c.code) {
			t.Errorf("%s: %d %s, want 422 %s", c.path, rec.Code, rec.Body, c.code)
		}
	}
}

// TestGetPrompt_ReadScope: a read-only caller may render prompts and read the offline set; a missing source is 503.
func TestGetPrompt_ReadScope(t *testing.T) {
	t.Parallel()
	r := promptRig(t)
	ro := auth.Principal{Kind: auth.KindAgent, ID: "tok-ro", Name: "ro", Scopes: []auth.Scope{auth.ScopeRead}, Via: "token"}
	if code, _, raw := getPrompt(t, r, ro, "/api/prompts/critique_track?track_id="+lt.TrackLuna); code != 200 {
		t.Errorf("prompt: %d %s", code, raw)
	}
	if rec := r.callWith(ro, "GET", "/api/offline-set", ""); rec.Code != 200 {
		t.Errorf("offline set: %d %s", rec.Code, rec.Body)
	}
	if rec := r.callWith(auth.Principal{}, "GET", "/api/prompts/critique_track?track_id=x", ""); rec.Code != 401 {
		t.Errorf("anonymous: %d", rec.Code)
	}
}
