package acceptance_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/provider/fake"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// allOnFeatures is a verified-features file with every E2a and E2b capability on (what the canary cases X4a/X4b will have
// proved): the acceptance tests swap it into the rig's registry so the E2b modes are reachable. The committed file offers
// text2music only until the canary ran.
const allOnFeatures = `{"schema":"mm.engine-features/2","engine":"acestep15","verified_at":"2026-11-06","canary_commit":"abc1234",
 "tasks":["text2music","repaint","extend","complete","extract"],"reference":true,"reference_reproducible":false,"seed_reproducible":true,
 "lrc":true,"lrc_fixed_s":60,"models_mode":"bundle_cached_base_baked","max_s_default":480,
 "pools":{"AMPERE_24":{"max_s":480},"ADA_24":{"max_s":480}},"repaint":{"min_s":3,"max_s":90},"extend":{"min_s":3,"max_s":90},"reference_max_s":30}`

type aceOpts struct {
	adopt    bool // an adopted row for acestep15 before the services start (tests of the adoption gate leave it off)
	allOn    bool // swap the all-capabilities features file into the registry
	audioMax float64
	yueS     float64 // seconds of audio a yue2 take renders (default 3, as genRig)
	o        servertest.Options
}

// aceRig is a generation rig over an imported archive with BOTH fake backends: `fake` (yue2) and `fake-acestep`, each with a
// simulated worker uploading through the rig's own handlers and the production ingest bridge (real ffmpeg). The runner is idle:
// tests drive it with pump. ACE takes synthesise at most audioMax seconds of real audio while accounting the requested length
// (a scaled take stores a shorter FLAC than its audio_seconds; nothing here requires them to match).
func aceRig(t *testing.T, tpl archivetest.Template, a aceOpts) *servertest.Rig {
	t.Helper()
	o := a.o
	o.ACEStep = true
	if a.adopt {
		o.AdoptEngines = append(o.AdoptEngines, acestep.Name)
	}
	if a.audioMax <= 0 {
		a.audioMax = 4
	}
	o.FakeACE = &fake.Options{}
	o.FakeACEWorker = &fake.SimWorkerOptions{Tools: testtools.Tools(), AudioScale: a.audioMax}
	r := genRigWith(t, tpl, o, a.yueS)
	if a.allOn {
		allOn(t, r)
	}
	return r
}

// allOn swaps the all-capabilities features file into the rig's registry; call it before any traffic (the map is shared by every
// service).
func allOn(t *testing.T, r *servertest.Rig) {
	t.Helper()
	f, err := acestep.ParseFeatures([]byte(allOnFeatures))
	if err != nil {
		t.Fatal(err)
	}
	r.Services.Engines[acestep.Name] = acestep.Engine{Verified: f}
}

// aceBackend is the fake-acestep backend of the rig.
func aceBackend(t *testing.T, r *servertest.Rig) *fake.Backend {
	t.Helper()
	b, ok := r.Services.Backends.Get(provider.BackendFakeACEStep)
	if !ok {
		t.Fatal("no fake-acestep backend in the set")
	}
	fb, ok := b.(*fake.Backend)
	if !ok {
		t.Fatalf("fake-acestep is %T", b)
	}
	return fb
}

// problemCode reads the code and details of a problem+json body.
func problemCode(t *testing.T, resp *servertest.Response) (string, map[string]any) {
	t.Helper()
	var p struct {
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal(resp.Body, &p); err != nil {
		t.Fatalf("not a problem body (%d): %s", resp.Status, resp.Body)
	}
	return p.Code, p.Details
}

// aceRun estimates body, creates it as the human (the normal UI path) and pumps the runner until every job is terminal. It
// returns the estimate, the created batch and its job ids.
func aceRun(t *testing.T, r *servertest.Rig, body map[string]any, key string) (est, batch map[string]any, jobIDs []string) {
	t.Helper()
	est, batch = aceCreate(t, r, body, key)
	for _, j := range arr(batch["jobs"]) {
		jobIDs = append(jobIDs, obj(j)["id"].(string))
	}
	pump(t, r, func() bool {
		for _, id := range jobIDs {
			if s, _ := jobState(t, r, id); !terminalJob(s) {
				return false
			}
		}
		return true
	})
	return est, batch, jobIDs
}

// aceCreate estimates and creates (human path) without running anything.
func aceCreate(t *testing.T, r *servertest.Rig, body map[string]any, key string) (est, batch map[string]any) {
	t.Helper()
	h := r.Human(t)
	est = obj(h.OK(t, "POST", "/api/generations/estimate", body))
	resp := h.Do(t, "POST", "/human/api/generations", map[string]any{"confirmation_token": est["confirmation_token"], "rationale": "e2 acceptance"}, "Idempotency-Key", key)
	if resp.Status != 201 {
		t.Fatalf("create: %d %s", resp.Status, resp.Body)
	}
	return est, obj(resp.JSON(t))
}

// jobTrack is the track a completed job produced.
func jobTrack(t *testing.T, r *servertest.Rig, job string) string {
	t.Helper()
	var id *string
	if err := r.Store.R.QueryRow(`SELECT track_id FROM jobs WHERE id = ?`, job).Scan(&id); err != nil || id == nil {
		t.Fatalf("job %s has no track (%v)", job, err)
	}
	return *id
}

// rowsDump renders every row of q as one text block (a compare-before/after of a table).
func rowsDump(t *testing.T, r *servertest.Rig, q string) string {
	t.Helper()
	rows, err := r.Store.R.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	cols, _ := rows.Columns()
	var b strings.Builder
	for rows.Next() {
		vals := make([]any, len(cols))
		ptr := make([]any, len(cols))
		for i := range vals {
			ptr[i] = &vals[i]
		}
		if err := rows.Scan(ptr...); err != nil {
			t.Fatal(err)
		}
		for _, v := range vals {
			if bs, ok := v.([]byte); ok {
				v = string(bs)
			}
			fmt.Fprintf(&b, "%v|", v)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// genRigWith is genRig with the seconds a yue2 take renders.
func genRigWith(t *testing.T, tpl archivetest.Template, o servertest.Options, yueS float64) *servertest.Rig {
	t.Helper()
	if yueS <= 0 {
		return genRig(t, tpl, o)
	}
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	testtools.Require(t, "ffmpeg")
	late := &lateUploader{}
	o.Archive, o.Backend, o.M2b.Media = &tpl, "fake", true
	fo := fake.Options{}
	if o.Fake != nil {
		fo = *o.Fake
	}
	fo.Worker = fake.NewSimWorker(fake.SimWorkerOptions{Uploader: late, Tools: testtools.Tools(), AudioSeconds: yueS})
	o.Fake = &fo
	r := servertest.New(t, o)
	late.set(fake.HandlerUploader{UploadHandler: r.Services.Uploads.Handler(), StartHandler: r.Services.Uploads.StartHandler()})
	return r
}
