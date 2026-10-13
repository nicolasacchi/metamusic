package exports_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/export"
	"github.com/nicolasacchi/metamusic/internal/exports"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/store"
)

type env struct {
	st    *store.Store
	dir   string
	root  *os.Root
	lib   *library.Service
	pls   *playlists.Service
	judg  *judgments.Service
	svc   *exports.Service
	now   time.Time
	ids   atomic.Int64
	clock func() time.Time
}

var (
	human = auth.Principal{Kind: auth.KindHuman, ID: "fixture", Name: "fixture", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
	agent = auth.Principal{Kind: auth.KindAgent, ID: "ag1", Name: "ag", Scopes: []auth.Scope{auth.ScopeRead, auth.ScopeWrite}}
)

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &env{st: st, dir: t.TempDir(), now: lt.FixedNow.Add(time.Hour)}
	e.ids.Store(0x7000)
	lt.Fixture(t, st, lt.FixedNow)
	lt.WriteFiles(t, e.dir)
	e.root, err = os.OpenRoot(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.root.Close() })
	e.clock = func() time.Time { return e.now }
	newID := func() string { return fmt.Sprintf("01900000-0000-7000-8000-%012x", e.ids.Add(1)) }
	e.lib = library.New(st, e.root, e.clock)
	e.pls = playlists.New(st, e.lib, e.clock, newID)
	e.judg = judgments.New(st, e.lib, e.clock, newID)
	e.svc = exports.New(exports.Options{Store: st, Data: e.root, Lib: e.lib, Playlists: e.pls, Ratings: e.judg,
		Signer: serve.NewSigner([]byte("test-key-test-key-test-key-12345"), e.clock), Now: e.clock, NewID: newID})
	t.Cleanup(func() { _ = os.Chmod(e.dir, 0o755) })
	return e
}

func (e *env) file(rel string) []byte {
	b, err := os.ReadFile(filepath.Join(e.dir, rel))
	if err != nil {
		panic(err)
	}
	return b
}

func (e *env) exportFile(t *testing.T, x exports.Export) []byte {
	t.Helper()
	obj, err := e.svc.Resolve(context.Background(), x.SHA256, strings.TrimPrefix(filepath.Ext(x.Filename), "."))
	if err != nil {
		t.Fatal(err)
	}
	return e.file(obj.RelPath)
}

func problem(t *testing.T, err error, status int, code string) *exports.Error {
	t.Helper()
	var pe *exports.Error
	if !errors.As(err, &pe) || pe.Status != status || pe.Code != code {
		t.Fatalf("error %v, want %d %s", err, status, code)
	}
	return pe
}

var songs = export.Selection{Kind: "source", SourcePath: "music/songs.jsonl"}

func TestDBSource_Contract(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	src := exports.NewDBSource(e.st, e.lib, e.pls)

	rows, err := src.JSONLSourceRows(ctx, "music/songs.jsonl")
	if err != nil || len(rows) != 5 {
		t.Fatalf("rows %d %v", len(rows), err)
	}
	for i, r := range rows {
		if r.LineNo != i+1 || r.ImportRunID != lt.RunID || r.RecipeID == "" || len(r.Raw) == 0 || r.ABC != "" {
			t.Errorf("row %d: %+v", i, r)
		}
	}
	if _, err := src.JSONLSourceRows(ctx, "nope.jsonl"); !errors.Is(err, export.ErrUnknownSource) {
		t.Errorf("unknown source: %v", err)
	}

	// an abc_path row carries the recipe's inlined abc
	abc := "X:1\nT:t\nM:4/4\nL:1/8\nK:C\nCDEF|]\n"
	raw := []byte(`{"id":"fx-abc","style":"pop","lyrics":"[verse]\nla","cot":"melody","seed":9,"abc_path":"abc/x.abc"}`)
	p, perr := parseWithABC(raw, abc)
	if perr != nil {
		t.Fatal(perr)
	}
	tx, _ := e.st.W.BeginTx(ctx, nil)
	rid, _, err := e.lib.InsertRecipe(ctx, tx, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.lib.InsertRecipeSource(ctx, tx, rid, library.SourceRef{Path: "music/abc.jsonl", Line: 1, Raw: raw, RawKind: "jsonl_line"}, lt.RunID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err = src.JSONLSourceRows(ctx, "music/abc.jsonl")
	if err != nil || len(rows) != 1 || rows[0].ABC != abc || !bytes.Equal(rows[0].Raw, raw) {
		t.Fatalf("abc row %+v %v", rows, err)
	}

	recs, err := src.SelectRecipes(ctx, export.TrackFilter{TrackIDs: []string{lt.TrackLuna, lt.TrackCitta}})
	if err != nil || len(recs) != 2 {
		t.Fatalf("recipes %d %v", len(recs), err)
	}
	var luna *export.RecipeRow
	for i := range recs {
		if recs[i].RequestID == "fx-luna" {
			luna = &recs[i]
		}
	}
	if luna == nil || luna.Request.Seed != 101 || luna.Hash == "" || luna.FirstRecordedAt == "" ||
		len(luna.JSONLSources) != 1 || luna.JSONLSources[0].Path != "music/songs.jsonl" || luna.JSONLSources[0].LineNo != 1 {
		t.Fatalf("luna %+v", luna)
	}

	count := func(f export.TrackFilter) int {
		r, err := src.SelectRecipes(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(r)
	}
	two := 2
	four := 4
	for name, tc := range map[string]struct {
		f    export.TrackFilter
		want int
	}{
		"default (non-discarded)":            {export.TrackFilter{}, 5},
		"tag ballad (AND, inherited counts)": {export.TrackFilter{Tags: []string{"ballad"}}, 2},
		"min stars 4":                        {export.TrackFilter{MinStars: &four}, 1},
		"min stars 2":                        {export.TrackFilter{MinStars: &two}, 2},
		"playlist":                           {export.TrackFilter{PlaylistID: lt.PlaylistMix}, 3},
		"playlist and ids intersect":         {export.TrackFilter{PlaylistID: lt.PlaylistMix, TrackIDs: []string{lt.TrackLuna, lt.TrackTwinB}}, 1},
		"status none match":                  {export.TrackFilter{Status: []string{"discarded"}}, 0},
		"ids and tag":                        {export.TrackFilter{TrackIDs: []string{lt.TrackLuna, lt.TrackTwinA}, Tags: []string{"night"}}, 1},
	} {
		if got := count(tc.f); got != tc.want {
			t.Errorf("%s: %d recipes, want %d", name, got, tc.want)
		}
	}
	if _, err := src.SelectRecipes(ctx, export.TrackFilter{PlaylistID: "nope"}); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown playlist: %v", err)
	}

	dcount := func(f export.DraftFilter) int {
		d, err := src.SelectDrafts(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(d)
	}
	for name, tc := range map[string]struct {
		f    export.DraftFilter
		want int
	}{
		"default backlog":  {export.DraftFilter{}, 9},
		"collection":       {export.DraftFilter{Collection: "pilot-x"}, 3},
		"origin upload":    {export.DraftFilter{Origin: "jsonl_upload"}, 2},
		"group key":        {export.DraftFilter{GroupKey: "fxb_001"}, 3},
		"ids":              {export.DraftFilter{DraftIDs: []string{lt.Draft1, lt.DraftUpload1, "x"}}, 2},
		"fulfilled (none)": {export.DraftFilter{Status: []string{"fulfilled"}}, 0},
	} {
		if got := dcount(tc.f); got != tc.want {
			t.Errorf("drafts %s: %d, want %d", name, got, tc.want)
		}
	}
	ds, _ := src.SelectDrafts(ctx, export.DraftFilter{Origin: "jsonl_upload"})
	if ds[0].Request.ID != "fxu_001" || ds[0].RequestID != "fxu_001" || ds[0].GroupKey != "" {
		t.Errorf("draft row %+v", ds[0])
	}

	samples, err := src.StyleSamples(ctx)
	if err != nil || len(samples) != 5+6+1+1 {
		t.Errorf("style samples %d %v", len(samples), err)
	}
}

func TestExports_ContentAddressed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, err := e.svc.JSONL(ctx, human, songs, "")
	if err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Minute)
	b, err := e.svc.JSONL(ctx, human, songs, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA256 != b.SHA256 || a.ID == b.ID || a.Kind != "songs_jsonl" || a.Filename != "songs.jsonl" || a.JSONL.Rows != 5 || a.JSONL.RawRows != 5 {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
	var n int
	if err := e.st.R.QueryRow(`SELECT COUNT(*) FROM exports WHERE sha256 = ?`, a.SHA256).Scan(&n); err != nil || n != 2 {
		t.Errorf("rows for the sha: %d %v", n, err)
	}
	rel := "derived/exports/" + a.SHA256[:2] + "/" + a.SHA256 + ".jsonl"
	st, err := os.Stat(filepath.Join(e.dir, rel))
	if err != nil || st.Mode().Perm() != 0o444 || st.Size() != a.Bytes {
		t.Fatalf("file %v %v", st, err)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dir, "incoming")); len(ents) != 0 {
		t.Errorf("work dirs left: %v", ents)
	}
	if !strings.HasPrefix(a.Download.URL, "/media/"+a.SHA256+".jsonl?exp=") {
		t.Errorf("link %s", a.Download.URL)
	}
	got, err := e.svc.Get(ctx, human, a.ID)
	if err != nil || got.SHA256 != a.SHA256 || got.JSONL == nil || got.JSONL.Rows != 5 || got.Download.URL == "" {
		t.Errorf("get %+v %v", got, err)
	}
	problem(t, func() error { _, err := e.svc.Get(ctx, human, "nope"); return err }(), 404, "NOT_FOUND")
	problem(t, func() error {
		_, err := e.svc.JSONL(ctx, human, export.Selection{Kind: "source", SourcePath: "x"}, "")
		return err
	}(), 422, "UNKNOWN_SOURCE")
	problem(t, func() error {
		_, err := e.svc.JSONL(ctx, human, export.Selection{Kind: "tracks", Tracks: export.TrackFilter{Status: []string{"discarded"}}}, "")
		return err
	}(), 422, "EXPORT_EMPTY")

	srcs, err := e.svc.Sources(ctx)
	if err != nil || len(srcs) != 3 || srcs[1].Path != "music/songs.jsonl" || srcs[1].Lines != 5 || srcs[1].Recipes != 5 {
		t.Errorf("sources %+v %v", srcs, err)
	}
	obj, err := e.svc.Resolve(ctx, a.SHA256, "jsonl")
	if err != nil || obj.ContentType != "application/x-ndjson" || obj.DownloadName != "songs.jsonl" || obj.RelPath != rel {
		t.Errorf("resolve %+v %v", obj, err)
	}
	if _, err := e.svc.Resolve(ctx, a.SHA256, "zip"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("wrong ext: %v", err)
	}
	if _, err := e.svc.Resolve(ctx, a.SHA256, "flac"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("flac: %v", err)
	}
}

func TestExports_InlineContentThreshold(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	small, err := e.svc.JSONL(ctx, human, songs, "my songs")
	if err != nil {
		t.Fatal(err)
	}
	if small.JSONL.Content == nil || *small.JSONL.Content != string(e.exportFile(t, small)) || small.Filename != "my-songs.jsonl" {
		t.Fatalf("small %+v", small)
	}
	// grow the export beyond 60 000 bytes with drafts
	lyrics := strings.Repeat("la la la la la\\n", 40)
	for i := range 100 {
		id := fmt.Sprintf("big_%03d", i)
		if _, err := e.st.W.Exec(`INSERT INTO recipe_drafts(id, origin, group_key, label, request_id, request_json, status, created_by, updated_by, created_at, updated_at)
			VALUES (?,'jsonl_upload',NULL,'x',?,?,'backlog','human:x','human:x','2026-10-06T12:00:00Z','2026-10-06T12:00:00Z')`,
			fmt.Sprintf("01900000-0000-7000-8000-%012x", 0x9000+i), id,
			fmt.Sprintf(`{"id":%q,"style":"pop","lyrics":"[verse]\n%s","cot":"full","seed":%d}`, id, lyrics, i)); err != nil {
			t.Fatal(err)
		}
	}
	big, err := e.svc.JSONL(ctx, human, export.Selection{Kind: "drafts", Drafts: export.DraftFilter{Origin: "jsonl_upload"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if big.Bytes <= 60000 || big.JSONL.Content != nil || !big.JSONL.Validation.OK || big.JSONL.Rows != 102 {
		t.Fatalf("big: bytes %d content %v validation %+v", big.Bytes, big.JSONL.Content != nil, big.JSONL.Validation)
	}
}

func TestExports_LinkTTLByPrincipal(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, err := e.svc.JSONL(ctx, agent, songs, "")
	if err != nil {
		t.Fatal(err)
	}
	h, err := e.svc.Get(ctx, human, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	exp := func(l exports.Link) time.Duration {
		u, _ := url.Parse(l.URL)
		sec, _ := strconv.ParseInt(u.Query().Get("exp"), 10, 64)
		ts, err := time.Parse(time.RFC3339, l.ExpiresAt)
		if err != nil || ts.Unix() != sec {
			t.Errorf("expires_at %q vs exp %d: %v", l.ExpiresAt, sec, err)
		}
		return time.Unix(sec, 0).Sub(e.now)
	}
	if d := exp(a.Download); d < 15*time.Minute || d > 16*time.Minute+time.Second {
		t.Errorf("agent link lives %v", d)
	}
	if d := exp(h.Download); d < 12*time.Hour || d > 13*time.Hour+time.Second {
		t.Errorf("human link lives %v", d)
	}
	dev := auth.Principal{Kind: auth.KindDevice, ID: "d1"}
	d, _ := e.svc.Get(ctx, dev, a.ID)
	if got := exp(d.Download); got < 12*time.Hour {
		t.Errorf("device link lives %v", got)
	}
}

func TestExports_Sweep(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	old, err := e.svc.JSONL(ctx, human, songs, "")
	if err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(2 * 24 * time.Hour)
	other, err := e.svc.JSONL(ctx, human, export.Selection{Kind: "tracks", Tracks: export.TrackFilter{TrackIDs: []string{lt.TrackLuna}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(6 * 24 * time.Hour) // old: 8 days, other: 6 days
	n, err := e.svc.Sweep(ctx, 7*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("swept %d %v", n, err)
	}
	problem(t, func() error { _, err := e.svc.Get(ctx, human, old.ID); return err }(), 410, "EXPORT_EXPIRED")
	if _, err := e.svc.Resolve(ctx, old.SHA256, "jsonl"); !errors.Is(err, serve.ErrNotFound) {
		t.Errorf("swept resolve: %v", err)
	}
	if _, err := e.svc.Get(ctx, human, other.ID); err != nil {
		t.Errorf("fresh export: %v", err)
	}
	var rows int
	_ = e.st.R.QueryRow(`SELECT COUNT(*) FROM exports`).Scan(&rows)
	if rows != 2 {
		t.Errorf("rows must stay: %d", rows)
	}
	if n, _ := e.svc.Sweep(ctx, 7*24*time.Hour); n != 0 {
		t.Errorf("second sweep removed %d", n)
	}
}

func TestExports_Bundle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	abc := "X:1\nT:t\nM:4/4\nL:1/8\nK:C\nCDEF|]\n"
	raw := []byte(`{"id":"fx-abc","style":"pop","lyrics":"[verse]\nla","cot":"melody","seed":9,"abc_path":"abc/x.abc"}`)
	p, err := parseWithABC(raw, abc)
	if err != nil {
		t.Fatal(err)
	}
	tx, _ := e.st.W.BeginTx(ctx, nil)
	rid, _, _ := e.lib.InsertRecipe(ctx, tx, p)
	_, _ = e.lib.InsertRecipeSource(ctx, tx, rid, library.SourceRef{Path: "music/abc.jsonl", Line: 1, Raw: raw, RawKind: "jsonl_line"}, lt.RunID)
	_ = tx.Commit()
	x, err := e.svc.JSONL(ctx, human, export.Selection{Kind: "source", SourcePath: "music/abc.jsonl"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if x.Kind != "songs_bundle" || x.Filename != "songs-bundle.zip" || len(x.JSONL.ABCFiles) != 1 || x.JSONL.ABCFiles[0].Path != "abc/x.abc" || !x.JSONL.Validation.OK {
		t.Fatalf("%+v", x)
	}
	zr, err := zip.NewReader(bytes.NewReader(e.exportFile(t, x)), x.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		r, _ := f.Open()
		b, _ := io.ReadAll(r)
		got[f.Name] = string(b)
	}
	if got["songs.jsonl"] != string(raw)+"\n" || got["abc/x.abc"] != abc || len(got) != 2 {
		t.Errorf("zip %q", got)
	}
}

// ---- playlist ZIP ----

// frames returns the ID3v2.4 frames at the start of an MP3 entry and the audio bytes after the tag.
func frames(t *testing.T, b []byte) (map[string][][]byte, []byte) {
	t.Helper()
	if string(b[:3]) != "ID3" {
		t.Fatalf("no ID3 header: %q", b[:10])
	}
	size := int(b[6])<<21 | int(b[7])<<14 | int(b[8])<<7 | int(b[9])
	body, audio := b[10:10+size], b[10+size:]
	out := map[string][][]byte{}
	for len(body) > 0 {
		n := int(body[4])<<21 | int(body[5])<<14 | int(body[6])<<7 | int(body[7])
		out[string(body[:4])] = append(out[string(body[:4])], body[10:10+n])
		body = body[10+n:]
	}
	return out, audio
}

func readZip(t *testing.T, b []byte) (names []string, files map[string][]byte) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	files = map[string][]byte{}
	for _, f := range zr.File {
		if f.Method != zip.Store {
			t.Errorf("%s: method %d, want Store", f.Name, f.Method)
		}
		r, _ := f.Open()
		files[f.Name], _ = io.ReadAll(r)
		names = append(names, f.Name)
	}
	return names, files
}

func TestZipExport_CurrentTagsID3(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	title := "Luna Park (renamed)"
	if err := e.judg.UpdateTrack(ctx, human, lt.TrackLuna, &title, nil); err != nil {
		t.Fatal(err)
	}
	x, err := e.svc.PlaylistZIP(ctx, human, lt.PlaylistMix)
	if err != nil {
		t.Fatal(err)
	}
	if x.Kind != "playlist_zip" || x.Filename != "fixture_mix.zip" || x.ZIP == nil || x.ZIP.Entries != 3 || len(x.ZIP.MissingMP3) != 0 {
		t.Fatalf("%+v", x)
	}
	names, files := readZip(t, e.exportFile(t, x))
	want := []string{"01 - Luna Park (renamed).mp3", "02 - Citta lenta.mp3", "03 - Twin A.mp3", "fixture-mix.m3u8"}
	if !slices.Equal(names, want) {
		t.Fatalf("entries %q, want %q", names, want)
	}
	if x.ZIP.Tracks[0].Entry != want[0] || x.ZIP.Tracks[0].TrackID != lt.TrackLuna {
		t.Errorf("zip info %+v", x.ZIP.Tracks)
	}
	f, audio := frames(t, files[want[0]])
	txt := func(id string) string { return string(f[id][0][1:]) }
	if txt("TIT2") != title || txt("TALB") != "fixture-mix" || txt("TRCK") != "1/3" || txt("TPE1") != "metamusic" {
		t.Errorf("frames %q", f)
	}
	if popm := f["POPM"][0]; popm[len(popm)-1] != 255 { // Luna: human 5 stars
		t.Errorf("POPM % x", popm)
	}
	f2, _ := frames(t, files[want[1]])
	if popm := f2["POPM"][0]; popm[len(popm)-1] != 128 || string(f2["TRCK"][0][1:]) != "2/3" { // Citta: 3 stars
		t.Errorf("citta frames %q", f2)
	}
	f3, _ := frames(t, files[want[2]])
	if _, ok := f3["POPM"]; ok { // TwinA: only an agent rating
		t.Error("an agent rating must not produce POPM")
	}
	var take, status string
	for _, kv := range f["TXXX"] {
		k, v, _ := strings.Cut(string(kv[1:]), "\x00")
		if k == "MM_TAKE_ID" {
			take = v
		}
		if k == "MM_STATUS" {
			status = v
		}
	}
	if take != lt.TrackLuna || status != "draft" {
		t.Errorf("TXXX take %q status %q", take, status)
	}
	if !bytes.Equal(audio, e.file(mustMP3Path(t, e, lt.TrackLuna))) {
		t.Error("audio bytes must be the rendition, untouched")
	}
	m3u := string(files["fixture-mix.m3u8"])
	if !strings.HasPrefix(m3u, "#EXTM3U\n#EXTINF:12,Luna Park (renamed)\n01 - Luna Park (renamed).mp3\n#EXTINF:6,") || !strings.HasSuffix(m3u, "03 - Twin A.mp3\n") {
		t.Errorf("m3u8 %q", m3u)
	}
}

func mustMP3Path(t *testing.T, e *env, track string) string {
	t.Helper()
	var p string
	if err := e.st.R.QueryRow(`SELECT path FROM renditions WHERE track_id = ? AND kind = 'mp3_320'`, track).Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestZipExport_Deterministic(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, err := e.svc.PlaylistZIP(ctx, human, lt.PlaylistMix)
	if err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Hour)
	b, err := e.svc.PlaylistZIP(ctx, human, lt.PlaylistMix)
	if err != nil || a.SHA256 != b.SHA256 {
		t.Fatalf("same playlist, different sha: %v", err)
	}
	name := "renamed"
	if _, err := e.pls.Update(ctx, human, lt.PlaylistMix, playlists.PlaylistPatch{Revision: 1, Name: &name}); err != nil {
		t.Fatal(err)
	}
	c, err := e.svc.PlaylistZIP(ctx, human, lt.PlaylistMix)
	if err != nil || c.SHA256 == a.SHA256 {
		t.Fatalf("after update_playlist the sha must change: %v", err)
	}
}

func TestZipExport_MissingMP3(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// Citta's rendition row goes away from the export's view: file removed, Luna's row kept.
	if err := os.Remove(filepath.Join(e.dir, mustMP3Path(t, e, lt.TrackCitta))); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.Exec(`UPDATE renditions SET kind = 'mp3_128' WHERE track_id = ? AND kind = 'mp3_320'`, lt.TrackTwinA); err != nil {
		t.Fatal(err)
	}
	x, err := e.svc.PlaylistZIP(ctx, human, lt.PlaylistMix)
	if err != nil {
		t.Fatal(err)
	}
	if x.ZIP.Entries != 1 || !slices.Equal(x.ZIP.MissingMP3, []string{lt.TrackCitta, lt.TrackTwinA}) {
		t.Fatalf("%+v", x.ZIP)
	}
	names, files := readZip(t, e.exportFile(t, x))
	if !slices.Equal(names, []string{"01 - Luna Park.mp3", "fixture-mix.m3u8"}) || strings.Contains(string(files["fixture-mix.m3u8"]), "Citta") {
		t.Errorf("entries %q", names)
	}
	// a playlist with nothing to export
	p, _ := e.pls.Create(ctx, human, playlists.NewPlaylist{Name: "none", TrackIDs: []string{lt.TrackTwinA}})
	_, err = e.svc.PlaylistZIP(ctx, human, p.ID)
	problem(t, err, 422, "EXPORT_EMPTY")
	if _, err := e.svc.PlaylistZIP(ctx, human, "nope"); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("unknown playlist: %v", err)
	}
}

func noWorkDirs(t *testing.T, e *env) {
	t.Helper()
	ents, _ := os.ReadDir(filepath.Join(e.dir, "incoming"))
	if len(ents) != 0 {
		t.Errorf("incoming/ holds %v", ents)
	}
}

func TestZipExport_Caps(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	many := slices.Repeat([]string{lt.TrackLuna}, 201)
	p, err := e.pls.Create(ctx, human, playlists.NewPlaylist{Name: "many", TrackIDs: many})
	if err != nil {
		t.Fatal(err)
	}
	pe := problem(t, func() error { _, err := e.svc.PlaylistZIP(ctx, human, p.ID); return err }(), 413, "PAYLOAD_TOO_LARGE")
	if pe.Details["limit_tracks"] != 200 || pe.Details["tracks"] != 201 {
		t.Errorf("details %v", pe.Details)
	}
	noWorkDirs(t, e)

	if _, err := e.st.W.Exec(`UPDATE renditions SET bytes = ? WHERE track_id = ? AND kind = 'mp3_320'`, int64(1<<30)+1, lt.TrackLuna); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.Exec(`UPDATE renditions SET bytes = ? WHERE track_id = ? AND kind = 'mp3_320'`, int64(1<<30), lt.TrackCitta); err != nil {
		t.Fatal(err)
	}
	p2, _ := e.pls.Create(ctx, human, playlists.NewPlaylist{Name: "heavy", TrackIDs: []string{lt.TrackLuna, lt.TrackCitta}})
	pe = problem(t, func() error { _, err := e.svc.PlaylistZIP(ctx, human, p2.ID); return err }(), 413, "PAYLOAD_TOO_LARGE")
	if pe.Details["limit_bytes"] != int64(2<<30) || pe.Details["bytes"] != int64(2<<30)+1 {
		t.Errorf("details %v", pe.Details)
	}
	noWorkDirs(t, e)
	if _, err := os.Stat(filepath.Join(e.dir, "derived/exports")); err == nil {
		t.Error("nothing may be written for a refused export")
	}
}

func TestZipExport_EntryNamesFlat(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	titles := map[string]string{
		lt.TrackLuna: "../x", lt.TrackCitta: "a/b", lt.TrackTwinA: `a\b`, lt.TrackTwinB: ".hidden", lt.TrackShort: "tab\there\x01",
	}
	for id, title := range titles {
		if _, err := e.st.W.Exec(`UPDATE tracks SET title = ? WHERE id = ?`, title, id); err != nil {
			t.Fatal(err)
		}
	}
	p, err := e.pls.Create(ctx, human, playlists.NewPlaylist{Name: "../p", TrackIDs: []string{lt.TrackLuna, lt.TrackCitta, lt.TrackTwinA, lt.TrackTwinB, lt.TrackShort}})
	if err != nil {
		t.Fatal(err)
	}
	x, err := e.svc.PlaylistZIP(ctx, human, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	names, _ := readZip(t, e.exportFile(t, x))
	if len(names) != 6 {
		t.Fatalf("entries %q", names)
	}
	for _, n := range names {
		if strings.ContainsAny(n, `/\`) || strings.HasPrefix(n, ".") || strings.ContainsFunc(n, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			t.Errorf("unsafe entry name %q", n)
		}
	}
	if names[5] != "_._p.m3u8" && !strings.HasSuffix(names[5], ".m3u8") {
		t.Errorf("playlist entry %q", names[5])
	}
	// the playlist name "../p" must not start with a dot either
	if strings.HasPrefix(names[5], ".") {
		t.Error("m3u8 starts with a dot")
	}
	for _, f := range []string{x.Filename} {
		if strings.ContainsAny(f, "/\\") || strings.HasPrefix(f, ".") {
			t.Errorf("filename %q", f)
		}
	}
}
