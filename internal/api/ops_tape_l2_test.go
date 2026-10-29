package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/tape"
	"github.com/nicolasacchi/metamusic/internal/tape/tapetest"
)

// tapeAPI is the two tape operations over a store with one published Record and a snapshot source the test controls; the
// service is the real one, the pipeline is tapetest.FakePipeline. (The rig-based test below goes through the whole app.)
type tapeAPI struct {
	r    *rig
	svc  *tape.Service
	src  *tapetest.FakeSource
	data *os.Root
}

func newTapeAPI(t *testing.T) *tapeAPI {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.W.Exec(`INSERT INTO records(id, title, cover_seed, status, revision, created_by, created_at, updated_by, updated_at, published_by, published_at, first_published_at)
		VALUES ('rec-1', 'Synthetic Record', 1, 'published', 2, 'human:op', '2026-01-01T00:00:00.000Z', 'human:op', '2026-01-01T00:00:00.000Z', 'human:op',
		'2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	src := tapetest.NewFakeSource()
	snap := tape.Snapshot{RecordID: "rec-1", Revision: 2, Status: "published", Title: "Synthetic Record", Tags: []flacmeta.Comment{{Key: "TITLE", Value: "Synthetic Record"}}}
	for i := 1; i <= 2; i++ {
		snap.Items = append(snap.Items, tape.Item{SlotID: fmt.Sprintf("slot-%d", i), Position: i, TakeID: fmt.Sprintf("take-%d", i), Title: fmt.Sprintf("Synthetic Take %d", i),
			MasterSHA256: strings.Repeat(fmt.Sprintf("%02x", i), 32), SampleRate: 48000, Bits: 24, Channels: 2, DurationS: 100,
			Measure: tape.Measure{LUFS: -15 + float64(i), TruePeakDBTP: -3, OK: true}})
	}
	src.Set(snap)
	svc := tape.New(tape.Options{Store: st, Source: src, Data: root, Pipeline: tapetest.NewFakePipeline(t.TempDir()), Signer: serve.NewSigner([]byte("k"), nil),
		FlacVersion: "flac 1.5.0", BuildVersion: "v-test", NewID: seqIDs("tape")})
	d := l2Deps()
	d.Tapes = svc
	d.Records = records.New(st, nil, nil)
	return &tapeAPI{r: l2Rig(t, d), svc: svc, src: src, data: root}
}

func seqIDs(prefix string) func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("%s-%03d", prefix, n) }
}

func (a *tapeAPI) call(p auth.Principal, method, path, body string) (int, map[string]any) {
	hdr := []string(nil)
	if method == "POST" {
		hdr = []string{"Idempotency-Key", "k-1"}
	}
	rec := a.r.callWith(p, method, path, body, hdr...)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

// TestTapeOps_ExportAndGet: 201 queued, RunOne makes it ready, the link exists only when ready, errors keep their codes.
func TestTapeOps_ExportAndGet(t *testing.T) {
	t.Parallel()
	a := newTapeAPI(t)
	rw := l2Agent(auth.ScopeRead, auth.ScopeWrite)
	code, tp := a.call(rw, "POST", "/api/records/rec-1/tapes", `{"target_lufs":-16.04,"revision":2}`)
	if code != 201 || tp["state"] != "queued" || tp["reused"] != false || tp["download"] != nil || tp["target_lufs"] != -16.0 || tp["record_revision"] != float64(2) {
		t.Fatalf("export: %d %v", code, tp)
	}
	items := tp["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["status"] != "matched" || items[0].(map[string]any)["gain_db"] != -2.0 {
		t.Errorf("items: %v", items)
	}
	id := tp["id"].(string)
	if code, got := a.call(rw, "GET", "/api/tapes/"+id, ""); code != 200 || got["state"] != "queued" || got["chapters"] != nil || got["progress"] != nil || got["error"] != nil {
		t.Fatalf("get queued: %d %v", code, got)
	}
	// the same snapshot again: reused
	if code, again := a.call(rw, "POST", "/api/records/rec-1/tapes", `{"target_lufs":-16.0}`); code != 201 || again["id"] != id || again["reused"] != true {
		t.Errorf("reuse: %d %v", code, again)
	}
	if ran, err := a.svc.RunOne(context.Background()); err != nil || !ran {
		t.Fatalf("RunOne: %v %v", ran, err)
	}
	code, got := a.call(rw, "GET", "/api/tapes/"+id, "")
	dl, _ := got["download"].(map[string]any)
	if code != 200 || got["state"] != "ready" || dl == nil || !strings.HasPrefix(dl["url"].(string), "/media/") || len(got["chapters"].([]any)) != 2 || got["total_ms"] != float64(200000) {
		t.Fatalf("get ready: %d %v", code, got)
	}
	// errors
	if code, p := a.call(rw, "POST", "/api/records/nope/tapes", `{}`); code != 404 || p["code"] != "NOT_FOUND" {
		t.Errorf("unknown record: %d %v", code, p)
	}
	if code, p := a.call(rw, "POST", "/api/records/rec-1/tapes", `{"revision":1}`); code != 409 || p["code"] != "STALE_REVISION" {
		t.Errorf("stale: %d %v", code, p)
	}
	if code, p := a.call(rw, "POST", "/api/records/rec-1/tapes", `{"target_lufs":-5}`); code != 422 {
		t.Errorf("target: %d %v", code, p)
	}
	if code, p := a.call(rw, "POST", "/api/records/rec-1/tapes", `{"accept_warnings":true}`); code != 403 || p["code"] != "HUMAN_ONLY" {
		t.Errorf("agent accept: %d %v", code, p)
	}
	if code, p := a.call(rw, "GET", "/api/tapes/nope", ""); code != 404 || p["code"] != "NOT_FOUND" {
		t.Errorf("unknown tape: %d %v", code, p)
	}
	// the review gate through REST: 409 with the slots, a human accepts
	snap, _ := a.src.Snapshot(context.Background(), "rec-1", tape.SnapOptions{})
	snap.Warnings = []tape.SlotWarning{{SlotID: "slot-2", Code: "undecided_pick"}}
	a.src.Set(snap)
	code, p := a.call(rw, "POST", "/api/records/rec-1/tapes", `{"target_lufs":-18}`)
	d, _ := p["details"].(map[string]any)
	if code != 409 || p["code"] != "TAPE_NEEDS_REVIEW" || len(d["slots"].([]any)) != 1 {
		t.Errorf("review: %d %v", code, p)
	}
	if code, p := a.call(human, "POST", "/api/records/rec-1/tapes", `{"target_lufs":-18,"accept_warnings":true}`); code != 201 || p["accepted_warnings"] != true || len(p["warnings"].([]any)) != 1 {
		t.Errorf("human accept: %d %v", code, p)
	}
	// the quota through REST
	for _, tl := range []string{"-19", "-20"} {
		a.call(human, "POST", "/api/records/rec-1/tapes", `{"target_lufs":`+tl+`,"accept_warnings":true}`)
	}
	code, p = a.call(human, "POST", "/api/records/rec-1/tapes", `{"target_lufs":-21,"accept_warnings":true}`)
	if d, _ := p["details"].(map[string]any); code != 409 || p["code"] != "TAPE_QUOTA" || d["scope"] != "record" {
		t.Errorf("quota: %d %v", code, p)
	}
}

// TestTapeOps_NoLinkWhenNotReady: expired tapes and failed tapes carry no link and the failure shows code and message.
func TestTapeOps_NoLinkWhenNotReady(t *testing.T) {
	t.Parallel()
	a := newTapeAPI(t)
	rw := l2Agent(auth.ScopeRead, auth.ScopeWrite)
	_, tp := a.call(rw, "POST", "/api/records/rec-1/tapes", `{}`)
	id := tp["id"].(string)
	a.src.Set(tape.Snapshot{RecordID: "rec-1", Revision: 2, Status: "draft"}) // the Record was unpublished before the run
	if _, err := a.svc.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	code, got := a.call(rw, "GET", "/api/tapes/"+id, "")
	e, _ := got["error"].(map[string]any)
	if code != 200 || got["state"] != "failed" || e["code"] != "TAPE_SOURCE_CHANGED" || got["download"] != nil {
		t.Errorf("failed: %d %v", code, got)
	}
}

// TestTapeOps_ThroughTheApp drives both operations over HTTP on the whole app: the idempotent replay, the audit row without
// titles, the heavy path to a ready tape and the signed link. It needs the Record view's snapshot (lane V2): until that is
// merged the rig's snapshot source answers 501 and the test skips.
func TestTapeOps_ThroughTheApp(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, L1: true, L2: true})
	r.Settle(t)
	h := r.Human(t)
	var rec string
	if err := r.Store.R.QueryRow(`SELECT id FROM records WHERE status = 'published' LIMIT 1`).Scan(&rec); err != nil {
		t.Fatal(err)
	}
	first := h.Do(t, "POST", "/api/records/"+rec+"/tapes", map[string]any{"accept_warnings": true}, "Idempotency-Key", "tape-1")
	if first.Status == http.StatusNotImplemented {
		t.Skip("recordview.Snapshot lands with L2-V2")
	}
	if first.Status != 201 {
		t.Fatalf("export: %d %s", first.Status, first.Body)
	}
	body := m(first.JSON(t))
	id := str(body["id"])
	replay := h.Do(t, "POST", "/api/records/"+rec+"/tapes", map[string]any{"accept_warnings": true}, "Idempotency-Key", "tape-1")
	if replay.Status != 201 || str(m(replay.JSON(t))["id"]) != id {
		t.Errorf("replay: %d %s", replay.Status, replay.Body)
	}
	if n := dbStr(t, r, `SELECT count(*) FROM tapes`); n != "1" {
		t.Errorf("tapes = %s after a replay", n)
	}
	if !r.Tapes.RunOne(t) {
		t.Fatal("no queued tape")
	}
	got := m(h.OK(t, "GET", "/api/tapes/"+id, nil))
	dl, _ := got["download"].(map[string]any)
	if got["state"] != "ready" || dl == nil {
		t.Fatalf("get: %v", got)
	}
	if resp := h.Do(t, "GET", str(dl["url"]), nil); resp.Status != 200 || resp.Header.Get("Content-Type") != "audio/flac" ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), str(dl["filename"])) {
		t.Errorf("download: %d %v", resp.Status, resp.Header)
	}
	rows, err := r.Store.R.Query(`SELECT op, created_ids_json, error_code FROM audit_log WHERE op IN ('export_record_tape','get_tape')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var op, ids string
		var ec *string
		if err := rows.Scan(&op, &ids, &ec); err != nil {
			t.Fatal(err)
		}
		seen++
		if strings.Contains(ids, "Synthetic Record") || strings.Contains(ids, "Synthetic Song") {
			t.Errorf("audit row holds a title: %s", ids)
		}
	}
	if seen == 0 {
		t.Error("no audit row for the tape operations")
	}
}
