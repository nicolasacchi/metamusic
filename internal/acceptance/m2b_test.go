package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/duels/duelstest"
	"github.com/nicolasacchi/metamusic/internal/ears/earstest"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/ratelimit"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/synth"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// e2eRig is a rig over the imported e2e archive (nine takes, including the ears group).
func e2eRig(t *testing.T, o servertest.Options) *servertest.Rig {
	t.Helper()
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	tpl := archivetest.SyntheticArchive(t, "e2e")
	o.Archive = &tpl
	return servertest.New(t, o)
}

// trackByTitle finds a track id whose lower-cased title (dashes as spaces) contains want.
func trackByTitle(t *testing.T, c *servertest.Client, want string) string {
	t.Helper()
	items := c.OK(t, "GET", "/api/tracks?limit=100", nil).(map[string]any)["items"].([]any)
	for _, it := range items {
		m := it.(map[string]any)
		if strings.Contains(strings.ToLower(strings.ReplaceAll(m["title"].(string), "-", " ")), want) {
			return m["id"].(string)
		}
	}
	t.Fatalf("no track titled like %q", want)
	return ""
}

func nearly(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %v, want %v +- %v", what, got, want, tol)
	}
}

func flagCodes(dossier map[string]any) []string {
	var out []string
	for _, f := range dossier["flags"].([]any) {
		out = append(out, f.(map[string]any)["code"].(string))
	}
	return out
}

// TestAcceptance_M2b_TierAFlags: the e2e archive's ears group is analysed (ears.Backfill) and every
// defect is found where the generator put it (C55); no other take is flagged.
func TestAcceptance_M2b_TierAFlags(t *testing.T) {
	r := e2eRig(t, servertest.Options{M2b: servertest.M2bOptions{Media: true, Backfill: true}})
	h := r.Human(t)
	dossier := func(id string) map[string]any {
		return h.OK(t, "GET", "/api/tracks/"+id+"/analysis", nil).(map[string]any)
	}

	d := dossier(trackByTitle(t, h, "ears clip"))
	if d["tier"] != "A" || !slices.Contains(flagCodes(d), "clipping") {
		t.Errorf("clip take: tier %v flags %v", d["tier"], flagCodes(d))
	}
	clipping := d["clipping"].(map[string]any)
	nearly(t, "clipped_s", clipping["clipped_s"].(float64), 2.0, 0.2)
	spans := clipping["spans"].([]any)
	if len(spans) != 1 {
		t.Fatalf("clipping spans = %v, want one", spans)
	}
	nearly(t, "clipping span start", spans[0].(map[string]any)["start_s"].(float64), 2.0, 0.1)
	nearly(t, "clipping span end", spans[0].(map[string]any)["end_s"].(float64), 4.0, 0.1)

	d = dossier(trackByTitle(t, h, "ears gap"))
	if !slices.Contains(flagCodes(d), "internal_silence") {
		t.Errorf("gap take flags = %v", flagCodes(d))
	}
	found := false
	for _, s := range d["silence"].(map[string]any)["spans"].([]any) {
		m := s.(map[string]any)
		if math.Abs(m["start_s"].(float64)-4.0) <= 0.05 && math.Abs(m["end_s"].(float64)-6.5) <= 0.05 {
			found = true
		}
	}
	if !found {
		t.Errorf("no silence span [4.0, 6.5] in %v", d["silence"])
	}

	d = dossier(trackByTitle(t, h, "ears loop"))
	if !slices.Contains(flagCodes(d), "token_loop_suspected") {
		t.Errorf("loop take flags = %v", flagCodes(d))
	}
	streams := d["token_loops"].(map[string]any)["streams"].([]any)
	p := streams[0].(map[string]any)["periodic_run"].(map[string]any)
	if p["period_tokens"] != 25.0 {
		t.Errorf("period_tokens = %v, want 25", p["period_tokens"])
	}
	nearly(t, "period_s", p["period_s"].(float64), 0.5, 1e-9)
	if p["span_s"].(float64) < 6.0 || p["repeats"].(float64) < 12 {
		t.Errorf("periodic run %v: want span_s >= 6.0 and repeats >= 12", p)
	}
	nearly(t, "at_s", p["at_s"].(float64), 3.0, 0.1)
	st := d["structure"].(map[string]any)
	if st["timed"] != true {
		t.Errorf("structure = %v, want timed", st)
	}
	nearly(t, "expected_duration_s", st["expected_duration_s"].(float64), 12.0, 1e-9)
	nearly(t, "ratio", st["ratio"].(float64), 1.0, 0.01)

	// every other take has none of the three defect flags
	items := h.OK(t, "GET", "/api/tracks?limit=100", nil).(map[string]any)["items"].([]any)
	if len(items) != 9 {
		t.Fatalf("e2e library has %d tracks, want 9", len(items))
	}
	for _, it := range items {
		m := it.(map[string]any)
		if strings.Contains(strings.ToLower(m["title"].(string)), "ears") {
			continue
		}
		fl := flagCodes(dossier(m["id"].(string)))
		for _, bad := range []string{"clipping", "internal_silence", "token_loop_suspected"} {
			if slices.Contains(fl, bad) {
				t.Errorf("%v is flagged %s", m["title"], bad)
			}
		}
	}
}

// ---- spectrogram byte budget -------------------------------------------------------------------

// budgetSignal is one purpose-built take of the spectrogram budget test.
type budgetSignal struct {
	name    string
	seconds float64
	gen     func(t *testing.T, ffmpeg, dst string, seconds float64)
}

func ffmpegFLAC(t *testing.T, ffmpeg, dst string, args ...string) {
	t.Helper()
	full := append([]string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}, args...)
	full = append(full, "-c:a", "flac", dst)
	if out, err := exec.Command(ffmpeg, full...).CombinedOutput(); err != nil { // #nosec G204 -- test tool path
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

func noise(color string) func(*testing.T, string, string, float64) {
	return func(t *testing.T, ffmpeg, dst string, s float64) {
		ffmpegFLAC(t, ffmpeg, dst, "-f", "lavfi", "-i", fmt.Sprintf("anoisesrc=c=%s:r=44100:a=0.4:d=%v", color, s), "-ac", "1", "-sample_fmt", "s16")
	}
}

var budgetSignals = []budgetSignal{
	{"white 6s", 6, noise("white")},
	{"white 16s", 16, noise("white")},
	{"white 60s", 60, noise("white")},
	{"white 360s", 360, noise("white")},
	{"pink tone 240s", 240, func(t *testing.T, ffmpeg, dst string, s float64) {
		ffmpegFLAC(t, ffmpeg, dst, "-f", "lavfi", "-i", fmt.Sprintf("anoisesrc=c=pink:r=44100:a=0.3:d=%v", s),
			"-f", "lavfi", "-i", fmt.Sprintf("sine=f=997:r=44100:d=%v", s), "-filter_complex", "amix=inputs=2:duration=first:normalize=0", "-ac", "1", "-sample_fmt", "s16")
	}},
	{"sine 6s", 6, func(t *testing.T, ffmpeg, dst string, s float64) {
		ffmpegFLAC(t, ffmpeg, dst, "-f", "lavfi", "-i", fmt.Sprintf("sine=f=440:r=44100:d=%v", s), "-ac", "1", "-sample_fmt", "s16")
	}},
	{"silence 12s", 12, func(t *testing.T, ffmpeg, dst string, s float64) {
		ffmpegFLAC(t, ffmpeg, dst, "-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", fmt.Sprint(s), "-sample_fmt", "s16")
	}},
	{"clip bursts 8s", 8, func(t *testing.T, ffmpeg, dst string, s float64) {
		if err := synth.Special(context.Background(), testtools.Tools(), dst, synth.SpecialClipBursts, s); err != nil {
			t.Fatal(err)
		}
	}},
}

// budgetInbox writes the signals as an inbox: one recipe line and one take per signal.
func budgetInbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "music", "flac"), 0o750); err != nil {
		t.Fatal(err)
	}
	var lines []string
	var wg sync.WaitGroup
	for i, s := range budgetSignals {
		id := fmt.Sprintf("bud-%02d", i)
		lines = append(lines, fmt.Sprintf(`{"cot": "full", "id": "%s", "lyrics": "[Verse 1]\nla\n[Chorus]\nla", "seed": %d, "style": "pop"}`, id, 7000+i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.gen(t, testtools.Tools().FFmpeg, filepath.Join(dir, "music", "flac", fmt.Sprintf("bud_%02d.flac", i)), s.seconds)
		}()
	}
	wg.Wait()
	if err := os.WriteFile(filepath.Join(dir, "music", "songs.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

const (
	imageBudget  = 100_000
	resultBudget = 140_000
)

// TestAcceptance_M2b_SpectrogramBudget: whatever the signal, the stored picture and every face's
// copy of it stay inside the byte budget (C50.4, C52).
func TestAcceptance_M2b_SpectrogramBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	tpl := archivetest.ImportInbox(t, budgetInbox(t))
	r := servertest.New(t, servertest.Options{Archive: &tpl, M2b: servertest.M2bOptions{Media: true, Backfill: true}})
	h := r.Human(t)
	httpSess, stdioSess := r.MCP(t), r.Stdio(t)

	items := h.OK(t, "GET", "/api/tracks?limit=100", nil).(map[string]any)["items"].([]any)
	if len(items) != len(budgetSignals) {
		t.Fatalf("%d tracks imported, want %d", len(items), len(budgetSignals))
	}
	for _, it := range items {
		m := it.(map[string]any)
		id, title := m["id"].(string), m["title"].(string)

		// the stored rendition
		var rel, params string
		var size int64
		if err := r.Store.R.QueryRow(`SELECT path, bytes, params_json FROM renditions WHERE track_id = ? AND kind = 'spectrogram'`, id).Scan(&rel, &size, &params); err != nil {
			t.Errorf("%s: no spectrogram rendition: %v", title, err)
			continue
		}
		raw, err := os.ReadFile(filepath.Join(r.DataDir, rel))
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > imageBudget || int64(len(raw)) != size || !bytes.HasPrefix(raw, []byte{0xFF, 0xD8, 0xFF}) {
			t.Errorf("%s: rendition %d bytes (row %d), magic % x", title, len(raw), size, raw[:min(3, len(raw))])
		}
		var pp struct{ Width, Height int }
		_ = json.Unmarshal([]byte(strings.NewReplacer(`"width"`, `"Width"`, `"height"`, `"Height"`).Replace(params)), &pp)
		if cfg, err := jpeg.DecodeConfig(bytes.NewReader(raw)); err != nil || cfg.Width != pp.Width || cfg.Height != pp.Height {
			t.Errorf("%s: jpeg %v (%v) != params %dx%d", title, cfg, err, pp.Width, pp.Height)
		}

		// REST inline
		view := h.OK(t, "GET", "/api/tracks/"+id+"/spectrogram", nil).(map[string]any)
		b64, _ := view["image"].(map[string]any)["data_base64"].(string)
		dec, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(dec) == 0 || len(dec) > imageBudget || !bytes.Equal(dec, raw) {
			t.Errorf("%s: REST inline image %d bytes (%v), equal to the rendition: %v", title, len(dec), err, bytes.Equal(dec, raw))
		}

		// MCP (HTTP) and stdio
		for face, sess := range map[string]*mcp.ClientSession{"mcp": httpSess, "stdio": stdioSess} {
			res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_track_spectrogram", Arguments: map[string]any{"id": id}})
			if err != nil || res.IsError {
				t.Errorf("%s/%s: %v %+v", title, face, err, res)
				continue
			}
			var img *mcp.ImageContent
			for _, c := range res.Content {
				if ic, ok := c.(*mcp.ImageContent); ok {
					img = ic
				}
			}
			ser, _ := json.Marshal(res)
			if img == nil || len(img.Data) > imageBudget || len(ser) > resultBudget {
				t.Errorf("%s/%s: image %v, serialized result %d bytes (want <= %d)", title, face, img != nil, len(ser), resultBudget)
			}
			if img != nil && !bytes.Equal(img.Data, raw) {
				t.Errorf("%s/%s: ImageContent differs from the rendition", title, face)
			}
		}
	}
}

// ---- duel lifecycle ----------------------------------------------------------------------------

func clipFile(t *testing.T, r *servertest.Rig, url string) string {
	t.Helper()
	name, _, _ := strings.Cut(strings.TrimPrefix(url, "/media/"), "?")
	key := strings.TrimSuffix(name, ".flac")
	return filepath.Join(r.DataDir, "derived", "clips", key[:2], key+".flac")
}

func fetchClip(t *testing.T, r *servertest.Rig, url string) (total uint64, rangeStatus int) {
	t.Helper()
	req, _ := http.NewRequest("GET", r.URL+url, nil)
	req.Header.Set("Range", "bytes=0-1023")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	rangeStatus = resp.StatusCode
	resp, err = http.Get(r.URL + url) //nolint:gosec,noctx // loopback test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/flac" {
		t.Fatalf("clip %s: %d %q", url, resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	info, err := flacmeta.Read(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return info.StreamInfo.TotalSamples, rangeStatus
}

func slot(d map[string]any, side string) map[string]any { return obj(obj(d["slots"])[side]) }

func trackID(s map[string]any) string {
	if tr := obj(s["track"]); tr != nil {
		id, _ := tr["id"].(string)
		return id
	}
	return ""
}

// TestAcceptance_M2b_DuelLifecycle: a blind duel from creation to the ladder over the real clip
// service, as the operator, a paired device and an agent.
func TestAcceptance_M2b_DuelLifecycle(t *testing.T) {
	r := e2eRig(t, servertest.Options{M2b: servertest.M2bOptions{Media: true, ClipCacheMB: 1}})
	h := r.Human(t)
	s1, s2 := trackByTitle(t, h, "song 01 s1001"), trackByTitle(t, h, "song 01 s2001")
	idx := 0
	key := func() string { idx++; return fmt.Sprintf("lifecycle-%d", idx) }
	create := func(c *servertest.Client, a, b string, blind bool) map[string]any {
		t.Helper()
		return obj(c.OK(t, "POST", "/api/duels", map[string]any{"a_track_id": a, "b_track_id": b, "blind": blind}, "Idempotency-Key", key()))
	}

	// the operator creates a blind duel between siblings; next_duel returns it unrevealed
	created := create(h, s1, s2, true)
	duelID := created["id"].(string)
	next := obj(h.OK(t, "GET", "/api/duels/next", nil))
	d := obj(next["duel"])
	if d == nil || d["id"] != duelID || d["revealed"] != false || trackID(slot(d, "a")) != "" || trackID(slot(d, "b")) != "" {
		t.Fatalf("next_duel = %v, want the unrevealed duel %s", next, duelID)
	}
	ua, ub := slot(d, "a")["clip"].(map[string]any)["url"].(string), slot(d, "b")["clip"].(map[string]any)["url"].(string)
	la, ra := fetchClip(t, r, ua)
	lb, rb := fetchClip(t, r, ub)
	if ra != http.StatusPartialContent || rb != http.StatusPartialContent {
		t.Errorf("Range requests answered %d and %d, want 206", ra, rb)
	}
	if la == 0 || la != lb {
		t.Errorf("decoded clip lengths %d and %d, want equal and non-zero", la, lb)
	}
	decidedClips := []string{clipFile(t, r, ua), clipFile(t, r, ub)}

	// the verdict "b" reveals the duel and crowns the track in slot B
	sub := obj(h.OK(t, "POST", "/api/duels/"+duelID+"/verdicts", map[string]any{"verdict": "b", "latency_ms": 5400, "switches": 4}))
	rev := obj(sub["duel"])
	winner := obj(sub["verdict"])["winner_track_id"]
	if rev["revealed"] != true || trackID(slot(rev, "b")) == "" || winner != trackID(slot(rev, "b")) {
		t.Errorf("after the verdict: revealed %v, winner %v, slot B %q", rev["revealed"], winner, trackID(slot(rev, "b")))
	}
	ladder := obj(h.OK(t, "GET", "/api/ladder", nil))
	rows := ladder["items"].([]any)
	if len(rows) == 0 || obj(rows[0])["track_id"] != winner {
		t.Errorf("ladder head = %v, want the winner %v", rows, winner)
	}

	// the paired device sees the duel as decided
	dev := r.Device(t)
	if got := obj(dev.OK(t, "GET", "/api/duels/"+duelID, nil)); got["state"] != "decided" {
		t.Errorf("device sees state %v, want decided", got["state"])
	}

	// an agent: next_duel skips the decided duel; its own duel and verdict are stored as the critic's
	tok, _, err := r.Services.Tokens.Create(context.Background(), "life-agent", []auth.Scope{auth.ScopeRead, auth.ScopeWrite})
	if err != nil {
		t.Fatal(err)
	}
	sess := mcpWithToken(t, r, tok)
	if n := obj(callTool(t, sess, "next_duel", nil, false)); n["duel"] != nil {
		t.Errorf("agent next_duel = %v, want no duel (the only duel is decided)", n["duel"])
	}
	s3, s4 := trackByTitle(t, h, "song 02"), trackByTitle(t, h, "song 03")
	ad := obj(callTool(t, sess, "create_duel", map[string]any{"a_track_id": s3, "b_track_id": s4}, true))
	agentDuel := ad["id"].(string)
	if ad["blind"] != true || ad["revealed"] != false {
		t.Errorf("agent duel: blind %v revealed %v", ad["blind"], ad["revealed"])
	}
	res := obj(callTool(t, sess, "submit_duel", map[string]any{"id": agentDuel, "verdict": "a", "note": "scripted"}, false))
	if v := obj(res["verdict"]); v["source"] != "critic" || v["principal_kind"] != "agent" {
		t.Errorf("agent verdict = %v, want source critic", v)
	}
	var critics, humans int
	_ = r.Store.R.QueryRow(`SELECT count(*) FROM duel_verdicts WHERE duel_id = ? AND source = 'critic'`, agentDuel).Scan(&critics)
	_ = r.Store.R.QueryRow(`SELECT count(*) FROM duel_verdicts WHERE duel_id = ? AND source = 'human'`, agentDuel).Scan(&humans)
	if critics != 1 || humans != 0 {
		t.Errorf("agent duel verdicts: %d critic, %d human", critics, humans)
	}
	agentNext := obj(h.OK(t, "GET", "/api/duels/next", nil))
	if obj(agentNext["duel"]) == nil || obj(agentNext["duel"])["id"] != agentDuel {
		t.Errorf("the operator's queue head = %v, want the agent's duel (a critic verdict does not decide it)", agentNext)
	}

	// a non-blind duel reveals at creation
	s5, s6 := trackByTitle(t, h, "song 04"), trackByTitle(t, h, "song 05")
	open := create(h, s5, s6, false)
	if open["revealed"] != true || trackID(slot(open, "a")) != s5 || trackID(slot(open, "b")) != s6 {
		t.Errorf("non-blind duel: revealed %v, slots %q/%q", open["revealed"], trackID(slot(open, "a")), trackID(slot(open, "b")))
	}
	// With a 1 MB cap a single duel's clips exceed the cap, so a clip that was rendered but whose duel row
	// does not exist yet can go in the same pass; reading the duels back re-renders what is missing, and
	// from then on their keys are pinned.
	var openClips []string
	for _, id := range []string{open["id"].(string), agentDuel} {
		got := obj(h.OK(t, "GET", "/api/duels/"+id, nil))
		openClips = append(openClips, clipFile(t, r, slot(got, "a")["clip"].(map[string]any)["url"].(string)),
			clipFile(t, r, slot(got, "b")["clip"].(map[string]any)["url"].(string)))
	}
	create(h, trackByTitle(t, h, "ears clip"), trackByTitle(t, h, "ears gap"), false)
	for _, f := range openClips {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("an open duel's clip %s was evicted: %v", filepath.Base(f), err)
		}
	}
	gone := 0
	for _, f := range decidedClips {
		if _, err := os.Stat(f); os.IsNotExist(err) {
			gone++
		}
	}
	if gone != len(decidedClips) {
		t.Errorf("%d of %d clips of the decided duel were evicted, want all", gone, len(decidedClips))
	}
}

// ---- gate coverage -----------------------------------------------------------------------------

type auditRow struct {
	Kind, Op, Code, Idem string
	Status               int
	Created              []string
}

func auditRows(t *testing.T, db *sql.DB, op string) []auditRow {
	t.Helper()
	rows, err := db.Query(`SELECT principal_kind, op, status, COALESCE(error_code,''), idempotency, created_ids_json FROM audit_log WHERE op = ? ORDER BY id`, op)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []auditRow
	for rows.Next() {
		var a auditRow
		var created string
		if err := rows.Scan(&a.Kind, &a.Op, &a.Status, &a.Code, &a.Idem, &created); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal([]byte(created), &a.Created)
		out = append(out, a)
	}
	return out
}

// TestAcceptance_M2b_GateCoverage: the gate (rate limit, authorize, idempotency, audit) fronts every
// M2b operation: writes are audited for every principal, reads for agents only, refusals are recorded.
func TestAcceptance_M2b_GateCoverage(t *testing.T) {
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true, Limits: ratelimit.Default,
		M2b: servertest.M2bOptions{Fixture: true}})
	human, dev := r.Human(t), r.Device(t)
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite)
	a, b := duelstest.TrackAlpha, duelstest.TrackBeta
	type who struct {
		name string
		c    *servertest.Client
		kind string
	}
	principals := []who{{"human", human, "human"}, {"device", dev, "device"}, {"agent", agent, "agent"}}

	for _, p := range principals {
		body := map[string]any{"a_track_id": a, "b_track_id": b, "blind": true}
		resp := p.c.Do(t, "POST", "/api/duels", body, "Idempotency-Key", "gate-"+p.name)
		if resp.Status != 201 {
			t.Fatalf("%s create_duel: %d %s", p.name, resp.Status, resp.Body)
		}
		duelID := obj(resp.JSON(t))["id"].(string)
		replay := p.c.Do(t, "POST", "/api/duels", body, "Idempotency-Key", "gate-"+p.name)
		if replay.Status != 201 && replay.Status != 200 {
			t.Fatalf("%s create_duel replay: %d %s", p.name, replay.Status, replay.Body)
		}
		sub := p.c.Do(t, "POST", "/api/duels/"+duelID+"/verdicts", map[string]any{"verdict": "a"})
		if sub.Status != 201 {
			t.Fatalf("%s submit_duel: %d %s", p.name, sub.Status, sub.Body)
		}
		verdictID := obj(obj(sub.JSON(t))["verdict"])["id"].(string)
		again := p.c.Do(t, "POST", "/api/duels/"+duelID+"/verdicts", map[string]any{"verdict": "a"})
		if again.Status != 200 {
			t.Fatalf("%s submit_duel replay: %d %s", p.name, again.Status, again.Body)
		}

		var mine []auditRow
		for _, row := range auditRows(t, r.Store.R, "create_duel") {
			if row.Kind == p.kind {
				mine = append(mine, row)
			}
		}
		if len(mine) != 2 || !slices.Equal(mine[0].Created, []string{duelID}) || len(mine[1].Created) != 0 {
			t.Errorf("%s create_duel audit rows = %+v, want a 201 row with [%s] and a replay row without ids", p.name, mine, duelID)
		}
		mine = nil
		for _, row := range auditRows(t, r.Store.R, "submit_duel") {
			if row.Kind == p.kind {
				mine = append(mine, row)
			}
		}
		if len(mine) != 2 || !slices.Equal(mine[0].Created, []string{verdictID}) || len(mine[1].Created) != 0 || mine[1].Status != 200 {
			t.Errorf("%s submit_duel audit rows = %+v, want a 201 row with [%s] and a 200 replay row without ids", p.name, mine, verdictID)
		}
	}

	// reads add an audit row for the agent only
	reads := []struct{ op, path string }{
		{"get_ladder", "/api/ladder"}, {"get_duel", "/api/duels/" + duelstest.DuelOpen}, {"list_duels", "/api/duels?limit=5"},
		{"next_duel", "/api/duels/next"}, {"get_clip", "/api/tracks/" + lt.TrackLuna + "/clip"},
		{"compare_tracks", "/api/compare?a=" + a + "&b=" + b}, {"get_track_analysis", "/api/tracks/" + earstest.ClipTrack + "/analysis"},
		{"get_track_spectrogram", "/api/tracks/" + earstest.ClipTrack + "/spectrogram?inline=false"},
	}
	for _, rd := range reads {
		for _, p := range principals {
			if resp := p.c.Do(t, "GET", rd.path, nil); resp.Status != 200 {
				t.Errorf("%s as %s: %d %s", rd.op, p.name, resp.Status, resp.Body)
			}
		}
		rows := auditRows(t, r.Store.R, rd.op)
		if len(rows) != 1 || rows[0].Kind != "agent" || rows[0].Status != 200 {
			t.Errorf("%s audit rows = %+v, want exactly one, the agent's", rd.op, rows)
		}
	}

	// the heavy bucket: get_clip x7 as an agent => the last is refused and recorded
	heavy, _ := r.Agent(t, auth.ScopeRead)
	var last *servertest.Response
	for range 7 {
		last = heavy.Do(t, "GET", "/api/tracks/"+lt.TrackLuna+"/clip", nil)
	}
	if last.Status != http.StatusTooManyRequests {
		t.Fatalf("7th get_clip = %d, want 429", last.Status)
	}
	limited := 0
	for _, row := range auditRows(t, r.Store.R, "get_clip") {
		if row.Code == "RATE_LIMITED" && row.Status == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited != 1 {
		t.Errorf("RATE_LIMITED rows for get_clip = %d, want 1", limited)
	}

	// create_duel without a key over REST: 428, recorded
	for _, p := range principals {
		if resp := p.c.Do(t, "POST", "/api/duels", map[string]any{"a_track_id": a, "b_track_id": b}); resp.Status != http.StatusPreconditionRequired {
			t.Errorf("%s create_duel without a key: %d, want 428", p.name, resp.Status)
		}
	}
	missing := 0
	for _, row := range auditRows(t, r.Store.R, "create_duel") {
		if row.Code == "IDEMPOTENCY_KEY_REQUIRED" && row.Status == http.StatusPreconditionRequired {
			missing++
		}
	}
	if missing != len(principals) {
		t.Errorf("IDEMPOTENCY_KEY_REQUIRED rows = %d, want %d", missing, len(principals))
	}
}

// ---- GET never writes ----------------------------------------------------------------------------

// tableChecksums hashes every table (rows sorted), leaving out the named columns; skip drops whole tables.
func tableChecksums(t *testing.T, db *sql.DB, skip map[string]bool, dropCols map[string][]string) map[string]string {
	t.Helper()
	names, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for names.Next() {
		var n string
		_ = names.Scan(&n)
		tables = append(tables, n)
	}
	_ = names.Close()
	out := map[string]string{}
	for _, tb := range tables {
		if skip[tb] {
			continue
		}
		rows, err := db.Query("SELECT * FROM " + tb) //nolint:gosec // table names come from sqlite_master
		if err != nil {
			t.Fatalf("%s: %v", tb, err)
		}
		cols, _ := rows.Columns()
		var lines []string
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			var sb strings.Builder
			for i, v := range vals {
				if slices.Contains(dropCols[tb], cols[i]) {
					continue
				}
				if bs, ok := v.([]byte); ok {
					v = string(bs)
				}
				fmt.Fprintf(&sb, "%s=%v\t", cols[i], v)
			}
			lines = append(lines, sb.String())
		}
		_ = rows.Close()
		sort.Strings(lines)
		sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
		out[tb] = hex.EncodeToString(sum[:])
	}
	return out
}

// TestGETNeverWritesDB_M2b: every M2b GET leaves the database as it was (clip cache files may appear).
func TestGETNeverWritesDB_M2b(t *testing.T) {
	r := servertest.New(t, servertest.Options{Fixture: true, Files: true, M2b: servertest.M2bOptions{Fixture: true}})
	human, dev := r.Human(t), r.Device(t)
	agent, _ := r.Agent(t, auth.ScopeRead)
	conn, err := r.Store.R.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	dataVersion := func() int64 {
		var v int64
		if err := conn.QueryRowContext(context.Background(), `PRAGMA data_version`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	gets := []string{
		"/api/duels/next", "/api/duels/" + duelstest.DuelOpen, "/api/duels/" + duelstest.DuelDecided, "/api/duels/" + duelstest.DuelSighted,
		"/api/duels?limit=10", "/api/duels?state=open&mode=blind", "/api/ladder", "/api/ladder?scope=lineage&root_track_id=" + duelstest.TrackAlpha,
		"/api/tracks/" + earstest.ClipTrack + "/analysis", "/api/tracks/" + earstest.LoopTrack + "/analysis",
		"/api/tracks/" + earstest.ClipTrack + "/spectrogram", "/api/tracks/" + earstest.ClipTrack + "/spectrogram?inline=false",
		"/api/compare?a=" + duelstest.TrackAlpha + "&b=" + duelstest.TrackBeta,
		"/api/tracks/" + lt.TrackLuna + "/clip", "/api/tracks/" + lt.TrackLuna + "/clip?match_lufs=-20",
		"/api/tracks/" + lt.TrackLuna + "/context",
	}
	run := func(c *servertest.Client) {
		t.Helper()
		for _, p := range gets {
			if resp := c.Do(t, "GET", p, nil); resp.Status != 200 {
				t.Fatalf("GET %s: %d %s", p, resp.Status, resp.Body)
			}
		}
	}

	// the proxy operator: nothing at all changes, not even the data version
	beforeSums := tableChecksums(t, r.Store.R, nil, nil)
	beforeVersion := dataVersion()
	run(human)
	if got := dataVersion(); got != beforeVersion {
		t.Errorf("PRAGMA data_version changed (%d -> %d) during the operator's GETs", beforeVersion, got)
	}
	if diff := diffSums(beforeSums, tableChecksums(t, r.Store.R, nil, nil)); diff != "" {
		t.Errorf("the operator's GETs changed %s", diff)
	}

	// an agent and a paired device: only their own M2a side effects (audit rows, last-used stamps)
	skip := map[string]bool{"audit_log": true}
	drop := map[string][]string{"api_tokens": {"last_used_at"}, "devices": {"last_seen_at", "expires_at"}}
	before := tableChecksums(t, r.Store.R, skip, drop)
	run(agent)
	run(dev)
	if diff := diffSums(before, tableChecksums(t, r.Store.R, skip, drop)); diff != "" {
		t.Errorf("the agent's and the device's GETs changed %s", diff)
	}
}

func diffSums(a, b map[string]string) string {
	var out []string
	for k, v := range b {
		if a[k] != v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
