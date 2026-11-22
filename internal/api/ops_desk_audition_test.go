package api_test

import (
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/desk"
	"github.com/nicolasacchi/metamusic/internal/desk/desktest"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

func newAuditionRig(t *testing.T) (*deskRig, *desktest.Renderer) {
	t.Helper()
	var f *desktest.Renderer
	r := newDeskRig(t, func(o *desk.Options) {
		f = desktest.New(t, o.DataDir)
		o.Transform = f
		o.Encode = desktest.Encode
	})
	return r, f
}

// TestOps_Desk_AuditionREST: the audition operations, their status codes (201 new, 200 cached), the signed link and each error.
func TestOps_Desk_AuditionREST(t *testing.T) {
	t.Parallel()
	r, f := newAuditionRig(t)
	id := r.want(t, human, "POST", "/api/tracks/"+lt.TrackLuna+"/plan", nil, 201, "")["id"].(string)
	first := r.want(t, deskEditor, "POST", "/api/plans/"+id+"/auditions", map[string]any{}, 201, "")
	if first["plan_id"] != id || first["soundfont"] != "gm" || first["chords"] != "block" || first["synth_version"] != "2.5.4" || first["seconds"] != 12.5 {
		t.Errorf("audition = %v", first)
	}
	// the same settings spelled out are a cache hit: 200 and the same row
	hit := r.want(t, human, "POST", "/api/plans/"+id+"/auditions", map[string]any{"soundfont": "gm", "chords": "block"}, 200, "")
	if hit["id"] != first["id"] {
		t.Errorf("hit = %v", hit)
	}
	got := r.want(t, deskAgent, "GET", "/api/plans/"+id+"/auditions/"+first["id"].(string), nil, 200, "")
	if got["id"] != first["id"] || !strings.Contains(got["url"].(string), ".mp3") {
		t.Errorf("get = %v", got)
	}
	r.want(t, human, "GET", "/api/plans/"+id+"/auditions/nope", nil, 404, "NOT_FOUND")
	r.want(t, human, "POST", "/api/plans/nope/auditions", map[string]any{}, 404, "NOT_FOUND")
	r.want(t, human, "POST", "/api/plans/"+id+"/auditions", map[string]any{"soundfont": "zz"}, 422, "SOUNDFONT_UNKNOWN")
	r.want(t, human, "POST", "/api/plans/"+id+"/auditions", map[string]any{"chords": "wild"}, 422, "")
	r.want(t, readOnly, "POST", "/api/plans/"+id+"/auditions", map[string]any{}, 403, "FORBIDDEN_SCOPE")
	r.want(t, readOnly, "GET", "/api/plans/"+id+"/auditions/"+first["id"].(string), nil, 200, "")
	// the plan lists its audition
	if pl := r.want(t, human, "GET", "/api/plans/"+id, nil, 200, ""); len(pl["auditions"].([]any)) != 1 {
		t.Errorf("auditions = %v", pl["auditions"])
	}
	f.Busy = true
	m := r.want(t, human, "POST", "/api/plans/"+id+"/auditions", map[string]any{"chords": "none"}, 409, "AUDITION_BUSY")
	if obj(t, m["details"])["retry_after_s"] == nil {
		t.Errorf("busy details = %v", m["details"])
	}
}

// TestOps_Desk_AuditionLinkSigned (C359.5): the link of an audition is signed for the caller who asked and names the MP3's hash.
func TestOps_Desk_AuditionLinkSigned(t *testing.T) {
	t.Parallel()
	r, _ := newAuditionRig(t)
	id := r.want(t, human, "POST", "/api/tracks/"+lt.TrackLuna+"/plan", nil, 201, "")["id"].(string)
	a := r.want(t, human, "POST", "/api/plans/"+id+"/auditions", map[string]any{}, 201, "")
	sha := r.scalar(`SELECT mp3_sha256 FROM desk_auditions`)
	agent := r.want(t, deskAgent, "GET", "/api/plans/"+id+"/auditions/"+a["id"].(string), nil, 200, "")
	for who, m := range map[string]map[string]any{"owner": a, "agent": agent} {
		u, _ := m["url"].(string)
		if !strings.Contains(u, sha) || !strings.Contains(u, ".mp3") || !strings.Contains(u, "sig") && !strings.Contains(u, "?") {
			t.Errorf("%s url = %q", who, u)
		}
	}
	if a["url"] == agent["url"] {
		t.Error("an agent's link has the same lifetime as a person's")
	}
}

// TestOps_Desk_AuditionUnavailable: no pipeline, no audition: 503 naming the missing part, a Listener is refused first.
func TestOps_Desk_AuditionUnavailable(t *testing.T) {
	t.Parallel()
	r := newDeskRig(t)
	id := r.want(t, human, "POST", "/api/tracks/"+lt.TrackLuna+"/plan", nil, 201, "")["id"].(string)
	m := r.want(t, human, "POST", "/api/plans/"+id+"/auditions", map[string]any{}, 503, "UNAVAILABLE")
	if obj(t, m["details"])["component"] != "transform" {
		t.Errorf("details = %v", m["details"])
	}
}
