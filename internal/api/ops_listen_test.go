package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/nicolasacchi/metamusic/internal/httpserver"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

func listenEvent(n int, track, kind string) map[string]any {
	return map[string]any{
		"id": fmt.Sprintf("01900000-0000-7000-8000-%012x", 0xb000+n), "track_id": track, "session_id": "session-123",
		"kind": kind, "client_at": "2026-10-06T11:59:00Z",
	}
}

func TestListen_ValidationAndDedup(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	good, dupInBatch, unknown := listenEvent(1, lt.TrackLuna, "start"), listenEvent(2, lt.TrackLuna, "complete"), listenEvent(3, "nope", "start")
	badRange := listenEvent(4, lt.TrackLuna, "segment")
	badRange["from_ms"], badRange["to_ms"] = 9000, 1000
	body := map[string]any{"events": []any{good, unknown, badRange, dupInBatch}}
	resp := human.Do(t, "POST", "/api/listen-events", body)
	if resp.Status != 200 {
		t.Fatalf("%d %s", resp.Status, resp.Body)
	}
	got := obj(t, resp.JSON(t))
	rej := got["rejected"].([]any)
	if got["accepted"] != 2.0 || got["duplicates"] != 0.0 || len(rej) != 2 ||
		obj(t, rej[0])["code"] != "unknown_track" || obj(t, rej[1])["code"] != "bad_range" {
		t.Fatalf("first post: %s", resp.Body)
	}
	got = obj(t, human.Do(t, "POST", "/api/listen-events", body).JSON(t))
	if got["accepted"] != 0.0 || got["duplicates"] != 2.0 || len(got["rejected"].([]any)) != 2 {
		t.Fatalf("re-post: %v", got)
	}
	// a malformed timestamp fails the schema (format date-time)
	bad := listenEvent(5, lt.TrackLuna, "start")
	bad["client_at"] = "yesterday"
	if resp := human.Do(t, "POST", "/api/listen-events", map[string]any{"events": []any{bad}}); resp.Status != 422 {
		t.Fatalf("bad client_at: %d %s", resp.Status, resp.Body)
	}
	// a timestamp in the future is a per-event rejection
	future := listenEvent(7, lt.TrackLuna, "start")
	future["client_at"] = "2027-01-01T00:00:00Z"
	fr := obj(t, human.Do(t, "POST", "/api/listen-events", map[string]any{"events": []any{future}}).JSON(t))
	if rj := fr["rejected"].([]any); len(rj) != 1 || obj(t, rj[0])["code"] != "bad_client_at" {
		t.Fatalf("future client_at: %v", fr)
	}

	// 201 events: schema maxItems
	many := make([]any, 201)
	for i := range many {
		many[i] = listenEvent(100+i, lt.TrackLuna, "start")
	}
	resp = human.Do(t, "POST", "/api/listen-events", map[string]any{"events": many})
	if resp.Status != 422 || obj(t, resp.JSON(t))["code"] != "VALIDATION_FAILED" {
		t.Errorf("201 events: %d %s", resp.Status, resp.Body)
	}
	// a 300 KiB body: 413
	big := listenEvent(6, lt.TrackLuna, "start")
	big["device_id"] = strings.Repeat("d", 300<<10)
	resp = human.Do(t, "POST", "/api/listen-events", map[string]any{"events": []any{big}})
	if resp.Status != 413 || obj(t, resp.JSON(t))["code"] != "PAYLOAD_TOO_LARGE" {
		t.Errorf("300 KiB: %d %.200s", resp.Status, resp.Body)
	}
	// read-only tokens cannot log
	ro, _ := r.Agent(t)
	if resp := ro.Do(t, "POST", "/api/listen-events", body); resp.Status != 403 {
		t.Errorf("read-only: %d", resp.Status)
	}
}

func TestListen_StatsReachTrackDetail(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	agent, _ := r.Agent(t, rw...)
	for i, c := range []*servertest.Client{human, agent} {
		resp := c.Do(t, "POST", "/api/listen-events", map[string]any{"events": []any{listenEvent(10+i, lt.TrackCitta, "start")}})
		if resp.Status != 200 {
			t.Fatalf("%d %s", resp.Status, resp.Body)
		}
	}
	tr := obj(t, obj(t, human.Do(t, "GET", "/api/tracks/"+lt.TrackCitta, nil).JSON(t))["listening"])
	if tr["plays"] != 1.0 {
		t.Errorf("only the human event counts: %v", tr)
	}
}

// TestListen_BeaconQueryFlag: the sendBeacon fallback marks itself with ?mm_client=spa, an undeclared
// query parameter. The request is accepted, audited as the SPA transport and counted by the
// served-operation recorder; the flag appears in neither the OpenAPI document nor an MCP schema.
func TestListen_BeaconQueryFlag(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	resp := human.Do(t, "POST", "/api/listen-events?mm_client=spa", map[string]any{"events": []any{listenEvent(1, lt.TrackLuna, "start")}})
	if resp.Status != 200 || obj(t, resp.JSON(t))["accepted"] != 1.0 {
		t.Fatalf("beacon: %d %s", resp.Status, resp.Body)
	}
	var transport string
	if err := r.Store.R.QueryRow(`SELECT transport FROM audit_log WHERE op = 'log_listen_events'`).Scan(&transport); err != nil || transport != "spa" {
		t.Errorf("audit transport %q %v", transport, err)
	}

	// the served-operation recorder (E2E mode) counts it
	opts := r.Services.Server
	opts.Cfg.E2E = true
	opts.SPA = fstest.MapFS{"index.html": {Data: []byte("x")}}
	h, err := httpserver.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	do := func(method, path, body string) []byte {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Remote-User", "alice")
		req.Header.Set("X-Mm-Edge", "edge")
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		b, _ := io.ReadAll(res.Body)
		return b
	}
	ev, _ := json.Marshal(map[string]any{"events": []any{listenEvent(2, lt.TrackLuna, "start")}})
	if out := do("POST", "/api/listen-events?mm_client=spa", string(ev)); !strings.Contains(string(out), `"accepted":1`) {
		t.Fatalf("beacon via e2e server: %s", out)
	}
	if served := string(do("GET", "/__e2e/served", "")); !strings.Contains(served, "log_listen_events") {
		t.Errorf("served operations %s", served)
	}

	spec, err := os.ReadFile("../../openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(spec), "mm_client") {
		t.Error("openapi.json mentions mm_client")
	}
	sess := r.MCP(t, rw...)
	tools, err := sess.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		b, _ := json.Marshal(tl)
		if strings.Contains(string(b), "mm_client") {
			t.Errorf("tool %s mentions mm_client", tl.Name)
		}
	}
}

func TestListen_DiagnosticKinds(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	adv := listenEvent(40, lt.TrackLuna, "advance")
	adv["detail"] = "reason=watchdog"
	blocked := listenEvent(41, lt.TrackLuna, "autoplay_blocked")
	blocked["detail"] = "cause=advance"
	resp := human.Do(t, "POST", "/api/listen-events", map[string]any{"events": []any{adv, blocked}})
	if got := obj(t, resp.JSON(t)); resp.Status != 200 || got["accepted"] != 2.0 {
		t.Fatalf("diagnostics: %d %s", resp.Status, resp.Body)
	}
	// the schema bounds the detail token
	bad := listenEvent(42, lt.TrackLuna, "error")
	bad["detail"] = "Has Spaces"
	if resp := human.Do(t, "POST", "/api/listen-events", map[string]any{"events": []any{bad}}); resp.Status != 422 {
		t.Fatalf("bad detail: %d %s", resp.Status, resp.Body)
	}
	// a detail on a listening kind is a per-event rejection
	start := listenEvent(43, lt.TrackLuna, "start")
	start["detail"] = "x=1"
	got := obj(t, human.Do(t, "POST", "/api/listen-events", map[string]any{"events": []any{start}}).JSON(t))
	if rj := got["rejected"].([]any); len(rj) != 1 || obj(t, rj[0])["code"] != "bad_detail" {
		t.Fatalf("detail on start: %v", got)
	}
}
