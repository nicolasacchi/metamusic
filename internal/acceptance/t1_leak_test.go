package acceptance_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// signedURL is the shape of a signed media URL or a bearer-looking query: none may appear in a transform answer, event, audit row
// or artifact.
var signedURL = regexp.MustCompile(`(?i)(/media/|[?&](sig|signature|token|exp|expires)=)`)

// TestT1_NoLeak: transforms leak no path, signed URL or secret. Everything a transform shows or stores is scanned: the answers of
// the seven operations over REST and MCP (human and agent), the audit table, the transform and recipe rows, the error text of a
// failed render, transform.json and request.json, the contents of the live work dir, and the server log.
func TestT1_NoLeak(t *testing.T) {
	logs := captureLogs(t)
	tr := newT1RigWith(t, func(o *servertest.Options) { o.Canary = true })
	a, b := tr.Info.Tx["A"], tr.Info.Tx["B"]
	root := filepath.Dir(tr.DataDir)
	l := newLeaks(t)
	l.add("secret_key", keyForms(canaryRigKey)...)
	l.add("edge_secret", canaryEdge)
	l.add("ntfy_token", canaryNtfy)
	l.add("rig_root", root)
	l.add("data_dir", tr.DataDir)
	l.add("child_dir", "mm-t1-child")
	l.add("work_dir", "incoming/transform-")
	l.add("masters_dir", "audio/masters")
	if d := filepath.Dir(tr.Tools.FFmpeg); filepath.IsAbs(d) {
		l.add("tool_dir", d)
	}
	scanText := func(loc, text string) {
		l.scan(loc, text)
		// a Take's own detail and context legitimately carry signed playback links; everything transform-specific must not
		if !strings.HasPrefix(loc, "GET /api/tracks/") && signedURL.MatchString(text) {
			t.Errorf("%s holds a signed media URL or a credential-looking query: %.200s", loc, signedURL.FindString(text))
		}
	}

	agent := tr.agentFor(auth.ScopeRead, auth.ScopeWrite)
	wr := tr.writer()
	// a render, a reuse, a queued-then-cancelled one, an estimate and the reads, by the human, over REST and over MCP
	done := tr.RunDone(t1Bodies(tr)["morph"])
	id := done["id"].(string)
	tr.CreateOK(t1Bodies(tr)["morph"])
	q := tr.CreateOK(masterBody(a, "bright"))
	tr.H.OK(t, "POST", "/api/transforms/"+q["id"].(string)+"/cancel", nil)
	tr.H.OK(t, "POST", "/api/transforms/estimate", masterBody(a, "warm"))
	tr.H.OK(t, "GET", "/api/transforms", nil)
	tr.H.OK(t, "GET", "/api/transforms/capabilities", nil)
	tr.H.OK(t, "GET", "/api/transforms/"+id+"/wait?timeout_s=0", nil)
	tr.H.OK(t, "GET", "/api/tracks/"+id, nil)
	tr.H.OK(t, "GET", "/api/tracks/"+id+"/context", nil)
	tr.H.OK(t, "GET", "/api/tracks/"+id+"/lineage", nil)
	wr.OK(t, "GET", "/api/transforms/"+id, nil)
	for _, op := range []struct {
		name string
		args map[string]any
	}{
		{"get_transform", map[string]any{"id": id}},
		{"list_transforms", map[string]any{}},
		{"get_transform_capabilities", map[string]any{}},
		{"estimate_transform", t1Bodies(tr)["widen"]},
	} {
		res := agent.raw(op.name, op.args)
		b, _ := json.Marshal(res)
		scanText("mcp "+op.name, string(b))
	}

	// a failed render: the parent's master is damaged, the decoder complains with the file's name on its stderr
	damaged := tr.MasterPath(b)
	_ = os.Chmod(damaged, 0o644)
	if err := os.WriteFile(damaged, []byte("not a flac file at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	fail := tr.CreateOK(map[string]any{"kind": "widen", "widen": map[string]any{"source_id": b}})
	tr.Drain()
	var errText, errCode string
	if err := tr.Store.R.QueryRow(`SELECT COALESCE(error_text, ''), COALESCE(error_code, '') FROM transforms WHERE id = ?`, fail["id"]).Scan(&errText, &errCode); err != nil {
		t.Fatal(err)
	}
	if errCode == "" || errText == "" {
		t.Fatalf("the damaged master did not fail the render with a message: %q %q", errCode, errText)
	}
	scanText("transforms.error_text", errText)
	view, _ := json.Marshal(tr.Get(fail["id"].(string)))
	scanText("failed transform view", string(view))

	// the live work dir of a running render holds no path or secret either
	live := tr.CreateOK(glacierBody(a, 20, 18))
	lid := live["id"].(string)
	finished := make(chan struct{})
	go func() { _, _ = tr.Services.Transform.RunOne(context.Background()); close(finished) }()
	deadline := time.Now().Add(60 * time.Second)
	for tr.Get(lid)["phase"] != "rendering" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	work := filepath.Join(tr.DataDir, "incoming", "transform-"+lid)
	var workFiles []string
	if ents, err := os.ReadDir(work); err != nil {
		t.Errorf("the live work dir cannot be read: %v", err)
	} else {
		for _, e := range ents {
			workFiles = append(workFiles, e.Name())
			if strings.HasSuffix(e.Name(), ".json") {
				raw, _ := os.ReadFile(filepath.Join(work, e.Name()))
				scanText("work dir "+e.Name(), string(raw))
			}
		}
	}
	if len(workFiles) == 0 {
		t.Error("no work dir files seen while the render ran: the test is vacuous")
	}
	tr.H.OK(t, "POST", "/api/transforms/"+lid+"/cancel", nil)
	<-finished

	// artifacts, rows and the audit table
	for _, f := range []string{"transform.json", "request.json"} {
		raw, err := os.ReadFile(filepath.Join(tr.DataDir, "takes", id, f))
		if err != nil {
			t.Fatal(err)
		}
		scanText("takes/<id>/"+f, string(raw))
	}
	for _, q := range []string{
		`SELECT COALESCE(group_concat(op || '|' || COALESCE(client,'') || '|' || created_ids_json || '|' || COALESCE(args_sha256,'') || '|' || COALESCE(error_code,''), char(10)), '') FROM audit_log`,
		`SELECT COALESCE(group_concat(request_json, char(10)), '') FROM recipes WHERE engine = 'dsp'`,
		`SELECT COALESCE(group_concat(COALESCE(params_json,'') || COALESCE(rationale,''), char(10)), '') FROM mutations WHERE transform_id IS NOT NULL`,
		`SELECT COALESCE(group_concat(title || '|' || COALESCE(error_text,'') || '|' || COALESCE(error_code,''), char(10)), '') FROM transforms`,
	} {
		var text string
		if err := tr.Store.R.QueryRow(q).Scan(&text); err != nil {
			t.Fatalf("%v: %s", err, q)
		}
		scanText(q[:40], text)
	}
	audit, _ := json.Marshal(tr.H.OK(t, "GET", "/api/audit?limit=100", nil))
	scanText("get_audit", string(audit))

	for _, c := range []*servertest.Client{tr.H, wr} {
		for _, r := range c.Responses() {
			scanText(fmt.Sprintf("%s %s", r.Method, r.Path), string(r.Body))
		}
	}
	scanText("server log", logs.String())
	l.flush()
}
