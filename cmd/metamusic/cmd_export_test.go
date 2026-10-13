package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/export"
	"github.com/nicolasacchi/metamusic/internal/exports"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/synth"
	"github.com/nicolasacchi/metamusic/internal/testtools"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeJSONL(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "songs.jsonl")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const goodLine = `{"id":"syn-a","style":"pop","lyrics":"[verse]\nla la","cot":"full","seed":1}`

func TestValidateBatch_ExitCodes(t *testing.T) {
	tests := []struct {
		name    string
		content string
		args    func(p string) []string
		code    int
		stdout  string
	}{
		{"valid", goodLine + "\n" + strings.Replace(goodLine, "syn-a", "syn-b", 1) + "\n", nil, 0, "2 requests, 0 issues"},
		{"duplicate id", goodLine + "\n" + goodLine + "\n", nil, 1, "duplicate_id"},
		{"unknown key is fatal", `{"id":"x","style":"s","lyrics":"l","title":"t"}` + "\n", nil, 1, "[fatal]"},
		{"bad json", "{not json\n", nil, 1, "bad_json"},
		{"missing file", "", func(string) []string { return []string{"validate-batch", "/nonexistent/x.jsonl"} }, 2, ""},
		{"no args", "", func(string) []string { return []string{"validate-batch"} }, 2, ""},
		{"flag looking arg", "", func(string) []string { return []string{"validate-batch", "--help"} }, 2, ""},
	}
	for _, tc := range tests {
		args := []string{"validate-batch"}
		var p string
		if tc.args == nil {
			p = writeJSONL(t, tc.content)
			args = append(args, p)
		} else {
			args = tc.args(p)
		}
		code, out, errs := do(t, nil, "", args...)
		if code != tc.code || !strings.Contains(out, tc.stdout) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want exit %d containing %q", tc.name, code, out, errs, tc.code, tc.stdout)
		}
	}
}

func TestValidateBatch_AbcPathResolvedBesideFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.abc"), []byte("X:1\nK:C\nCDEF|\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "songs.jsonl")
	if err := os.WriteFile(p, []byte(`{"id":"x","style":"s","lyrics":"l","abc_path":"a.abc"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := do(t, nil, "", "validate-batch", p); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if err := os.Remove(filepath.Join(dir, "a.abc")); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := do(t, nil, "", "validate-batch", p); code != 1 || !strings.Contains(out, "abc_path_unresolved") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

// TestCLI_M2aStubCommands: until M2a-B7 lands, backup/restore fail loudly (exit 2).
func TestCLI_M2aStubCommands(t *testing.T) {
	for _, cmd := range []string{"backup", "restore"} {
		code, _, errs := do(t, nil, "", cmd)
		if code != 2 || !strings.Contains(errs, "not implemented") {
			t.Errorf("%s: exit %d, stderr %q", cmd, code, errs)
		}
	}
}

// exportEnv is a data directory holding the M2a fixture library and an imported source with abc_path.
func exportEnv(t *testing.T) (env map[string]string, abcLine string) {
	t.Helper()
	env = testEnv(t, "proxy")
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	lt.Fixture(t, st, lt.FixedNow)
	abc := "X:1\nT:t\nM:4/4\nL:1/8\nK:C\nCDEF|]\n"
	abcLine = `{"id":"cli-abc","style":"pop","lyrics":"[verse]\nla","cot":"melody","seed":9,"abc_path":"abc/x.abc"}`
	p, err := yue2.ParseRequest([]byte(abcLine), yue2.ParseOptions{ResolveABCPath: func(string) ([]byte, error) { return []byte(abc), nil }})
	if err != nil {
		t.Fatal(err)
	}
	lib := library.New(st, nil, func() time.Time { return lt.FixedNow })
	tx, _ := st.W.BeginTx(ctx, nil)
	rid, _, err := lib.InsertRecipe(ctx, tx, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lib.InsertRecipeSource(ctx, tx, rid, library.SourceRef{Path: "music/abc.jsonl", Line: 1, Raw: []byte(abcLine), RawKind: "jsonl_line"}, lt.RunID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return env, abcLine
}

func recordedLines(t *testing.T, env map[string]string, path string) string {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err := st.R.Query(`SELECT raw FROM recipe_sources WHERE raw_kind = 'jsonl_line' AND source_path = ? ORDER BY line_no`, path)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var raw []byte
		_ = rows.Scan(&raw)
		b.Write(raw)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestCLI_Export(t *testing.T) {
	env, abcLine := exportEnv(t)

	// --source to stdout equals the recorded bytes; the file's own duplicate id (fx-twin) makes the exit 1
	code, out, errs := do(t, env, "", "export", "--source", "music/songs.jsonl")
	if want := recordedLines(t, env, "music/songs.jsonl"); out != want {
		t.Errorf("stdout differs from the recorded bytes:\n%q\n%q", out, want)
	}
	if code != 1 || !strings.Contains(errs, "duplicate_id") || !strings.Contains(errs, "5 lines, 5 requests, 1 issues") {
		t.Errorf("source: exit %d, stderr %q", code, errs)
	}

	// --for-batch renames and passes
	code, out, errs = do(t, env, "", "export", "--source", "music/songs.jsonl", "--for-batch")
	if code != 0 || !strings.Contains(out, `"fx-twin-s103"`) || !strings.Contains(out, `"fx-twin-s104"`) || !strings.Contains(errs, "0 issues") {
		t.Errorf("for-batch: exit %d, stdout %q, stderr %q", code, out, errs)
	}

	// tracks and drafts always validate
	code, out, errs = do(t, env, "", "export", "--tracks", "--tag", "ballad")
	if code != 0 || strings.Count(out, "\n") != 2 || !strings.Contains(out, "fx-twin-s103") {
		t.Errorf("tracks: exit %d, stdout %q, stderr %q", code, out, errs)
	}
	code, out, _ = do(t, env, "", "export", "--tracks", "--min-stars", "4")
	if code != 0 || strings.Count(out, "\n") != 1 || !strings.Contains(out, "fx-luna") {
		t.Errorf("min-stars: exit %d, stdout %q", code, out)
	}
	code, out, errs = do(t, env, "", "export", "--drafts", "--origin", "jsonl_upload")
	if code != 0 || strings.Count(out, "\n") != 2 || !strings.Contains(out, "fxu_001") {
		t.Errorf("drafts: exit %d, stdout %q, stderr %q", code, out, errs)
	}
	code, out, errs = do(t, env, "", "export", "--tracks", "--playlist", lt.PlaylistMix)
	if code != 0 || strings.Count(out, "\n") != 3 {
		t.Errorf("playlist: exit %d, stdout %q, stderr %q", code, out, errs)
	}

	// ABC files are needed: refused on stdout, written with --out
	if code, out, errs := do(t, env, "", "export", "--source", "music/abc.jsonl"); code != 2 || out != "" || !strings.Contains(errs, "--out") {
		t.Errorf("abc to stdout: exit %d, stdout %q, stderr %q", code, out, errs)
	}
	dir := filepath.Join(t.TempDir(), "bundle")
	if code, _, errs := do(t, env, "", "export", "--source", "music/abc.jsonl", "--out", dir); code != 0 {
		t.Fatalf("--out: exit %d, stderr %q", code, errs)
	}
	got, err := os.ReadFile(filepath.Join(dir, "songs.jsonl"))
	if err != nil || string(got) != abcLine+"\n" {
		t.Errorf("songs.jsonl %q %v", got, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "abc/x.abc")); err != nil || !strings.HasPrefix(string(b), "X:1\n") {
		t.Errorf("abc file %q %v", b, err)
	}
	// the written bundle passes validate-batch as it is
	if code, out, _ := do(t, nil, "", "validate-batch", filepath.Join(dir, "songs.jsonl")); code != 0 {
		t.Errorf("validate-batch on the bundle: exit %d: %s", code, out)
	}
	// ... and a second export into the same directory is refused
	if code, _, errs := do(t, env, "", "export", "--source", "music/abc.jsonl", "--out", dir); code != 2 || !strings.Contains(errs, "not empty") {
		t.Errorf("non-empty --out: exit %d, stderr %q", code, errs)
	}
}

func TestCLI_ExportUsage(t *testing.T) {
	env, _ := exportEnv(t)
	for name, tc := range map[string]struct {
		args []string
		code int
	}{
		"no selector":      {[]string{"export"}, 2},
		"two selectors":    {[]string{"export", "--tracks", "--drafts"}, 2},
		"unknown source":   {[]string{"export", "--source", "nope.jsonl"}, 2},
		"unknown flag":     {[]string{"export", "--tracks", "--bogus"}, 2},
		"extra argument":   {[]string{"export", "--tracks", "x"}, 2},
		"unknown playlist": {[]string{"export", "--tracks", "--playlist", "nope"}, 2},
		"bad status":       {[]string{"export", "--tracks", "--status", "weird"}, 2},
		"nothing selected": {[]string{"export", "--tracks", "--status", "discarded"}, 1},
		"no database":      {nil, 2},
	} {
		e := env
		if tc.args == nil {
			e = testEnv(t, "proxy")
			tc.args = []string{"export", "--tracks"}
		}
		if code, _, errs := do(t, e, "", tc.args...); code != tc.code {
			t.Errorf("%s: exit %d, stderr %q", name, code, errs)
		}
	}
}

// TestCLI_ValidateBatch: the validator agrees with the exporter about its own output.
func TestCLI_ValidateBatch(t *testing.T) {
	env, _ := exportEnv(t)
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"--source", "music/songs.jsonl"}, 1},
		{[]string{"--source", "music/songs.jsonl", "--for-batch"}, 0},
		{[]string{"--tracks"}, 0},
	} {
		dir := filepath.Join(t.TempDir(), "o")
		args := append([]string{"export", "--out", dir}, tc.args...)
		eCode, _, errs := do(t, env, "", args...)
		vCode, vOut, _ := do(t, nil, "", "validate-batch", filepath.Join(dir, "songs.jsonl"))
		if eCode != tc.code || vCode != tc.code {
			t.Errorf("%v: export exit %d (%s), validate-batch exit %d (%s), want %d", tc.args, eCode, errs, vCode, vOut, tc.code)
		}
	}
}

// ---- acceptance: the exporter against the imported synthetic archives (heavy) ----

var (
	seedMu   sync.Mutex
	seedDirs = map[string]string{}
)

// seedDir returns the inbox of the profile as the generator wrote it, the original bytes the exporter
// is compared against. Generated once per test binary.
func seedDir(t *testing.T, profile synth.Profile) string {
	t.Helper()
	seedMu.Lock()
	defer seedMu.Unlock()
	if d, ok := seedDirs[string(profile)]; ok {
		return d
	}
	d, err := os.MkdirTemp("", "mm-export-seed-")
	if err != nil {
		t.Fatal(err)
	}
	seedDirs[string(profile)] = d
	if _, err := synth.Archive(context.Background(), testtools.Tools(), d, profile, 1); err != nil {
		t.Fatal(err)
	}
	return d
}

func cleanupSeedTexts() {
	seedMu.Lock()
	defer seedMu.Unlock()
	for _, d := range seedDirs {
		_ = os.RemoveAll(d)
	}
}

func readSeed(t *testing.T, profile synth.Profile, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(seedDir(t, profile), rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// archiveEnv copies the imported archive and returns a CLI environment over it.
func archiveEnv(t *testing.T, profile string) map[string]string {
	t.Helper()
	tpl := archivetest.SyntheticArchive(t, profile)
	return map[string]string{"MM_DATA_DIR": tpl.Copy(t)}
}

func without(file, line string) string {
	return strings.Replace(file, line+"\n", "", 1)
}

// issueSet reduces a validator report to the multiset of (code, id) pairs, ids read from the line.
func issueSet(file string, rep yue2.BatchReport) []string {
	lines := strings.Split(file, "\n")
	var out []string
	for _, is := range rep.Issues {
		var h struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(lines[is.Line-1]), &h)
		out = append(out, is.Code+" "+h.ID)
	}
	slices.Sort(out)
	return out
}

func TestAcceptance_M2a_ExportByteIdentical(t *testing.T) {
	// acceptance profile: the original minus the one invalid line M1 skips; the abc_path row needs ABC files,
	// so the bytes are read from the songs.jsonl inside --out DIR (the CLI refuses stdout then)
	env := archiveEnv(t, "acceptance")
	orig := without(readSeed(t, synth.ProfileAcceptance, "music/songs.jsonl"), "{not json")
	dir := filepath.Join(t.TempDir(), "out")
	code, _, errs := do(t, env, "", "export", "--source", "music/songs.jsonl", "--out", dir)
	got, err := os.ReadFile(filepath.Join(dir, "songs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != orig {
		t.Errorf("acceptance songs.jsonl differs from the original minus the invalid line:\n%s\nwant\n%s", got, orig)
	}
	if code != 1 || !strings.Contains(errs, "duplicate_id") {
		t.Errorf("exit %d, stderr %q", code, errs)
	}
	if abc, err := os.ReadFile(filepath.Join(dir, "abc/syn-song-08.abc")); err != nil || string(abc) != readSeed(t, synth.ProfileAcceptance, "music/abc/syn-song-08.abc") {
		t.Errorf("abc file %q %v", abc, err)
	}
	if code, _, errs := do(t, env, "", "export", "--source", "music/songs.jsonl"); code != 2 || !strings.Contains(errs, "--out") {
		t.Errorf("stdout export needing ABC files: exit %d %q", code, errs)
	}

	// e2e profile: byte for byte on stdout (two syn-song-01 seeds => one duplicate_id, exit 1)
	env2 := archiveEnv(t, "e2e")
	for _, rel := range []string{"music/songs.jsonl", "music/backlog-variants.jsonl"} {
		seed := readSeed(t, synth.ProfileE2E, rel)
		code, out, errs := do(t, env2, "", "export", "--source", rel)
		wantCode := 0
		if rel == "music/songs.jsonl" {
			wantCode = 1
		}
		if out != seed || code != wantCode {
			t.Errorf("e2e %s: exit %d (want %d), stderr %q; identical = %v", rel, code, wantCode, errs, out == seed)
		}
	}

	// a tracks export: raw rows are recorded sources; generated rows are only renamed ones and recipes without
	// a JSONL source (the two syn-song-01 recipes become syn-song-01-s1001 and -s2001)
	st, err := store.Open(context.Background(), filepath.Join(env2["MM_DATA_DIR"], "metamusic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	lib := library.New(st, nil, time.Now)
	res, err := export.Build(context.Background(), exports.NewDBSource(st, lib, playlists.New(st, lib, nil, nil)), export.Selection{Kind: "tracks"})
	if err != nil {
		t.Fatal(err)
	}
	recorded := map[string]bool{}
	rows, _ := st.R.Query(`SELECT raw FROM recipe_sources WHERE raw_kind = 'jsonl_line'`)
	for rows.Next() {
		var b []byte
		_ = rows.Scan(&b)
		recorded[string(b)] = true
	}
	_ = rows.Close()
	renamed := map[string]bool{}
	for _, r := range res.Rows {
		switch {
		case r.Raw && !recorded[string(r.Bytes)]:
			t.Errorf("raw row is not a recorded source: %s", r.Bytes)
		case r.RenamedFrom != "":
			renamed[r.RequestID] = true
		case !r.Raw:
			var n int
			_ = st.R.QueryRow(`SELECT COUNT(*) FROM recipe_sources s JOIN recipes r ON r.id = s.recipe_id WHERE s.raw_kind = 'jsonl_line' AND r.request_id = ?`, r.RequestID).Scan(&n)
			if n != 0 {
				t.Errorf("row %s was regenerated although it has a JSONL source", r.RequestID)
			}
		}
	}
	if !renamed["syn-song-01-s1001"] || !renamed["syn-song-01-s2001"] || len(renamed) != 2 {
		t.Errorf("renamed rows %v", renamed)
	}
}

func TestAcceptance_M2a_ExportPassesBatchValidator(t *testing.T) {
	validates := func(t *testing.T, env map[string]string, name string, args ...string) {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "o")
		code, _, errs := do(t, env, "", append([]string{"export", "--out", dir}, args...)...)
		if code != 0 {
			t.Errorf("%s: export exit %d, stderr %q", name, code, errs)
			return
		}
		if vc, vo, _ := do(t, nil, "", "validate-batch", filepath.Join(dir, "songs.jsonl")); vc != 0 {
			t.Errorf("%s: validate-batch exit %d: %s", name, vc, vo)
		}
	}
	for _, profile := range []string{"acceptance", "e2e"} {
		env := archiveEnv(t, profile)
		validates(t, env, profile+" tracks", "--tracks")
		validates(t, env, profile+" drafts", "--drafts")
		validates(t, env, profile+" for-batch", "--source", "music/songs.jsonl", "--for-batch")
	}
	// the syn-song-01 lineage (two seeds of one request) lives in the e2e profile, the seed family in the acceptance one
	for name, tc := range map[string]struct {
		profile, like string
		want          []string
	}{
		"syn-song-01 lineage":     {"e2e", "syn-song-01", []string{"syn-song-01-s1001", "syn-song-01-s2001"}},
		"syn-song-02 seed family": {"acceptance", "syn-song-02%", []string{"syn-song-02", "syn-song-02-s2002"}},
	} {
		env := archiveEnv(t, tc.profile)
		st, err := store.Open(context.Background(), filepath.Join(env["MM_DATA_DIR"], "metamusic.db"))
		if err != nil {
			t.Fatal(err)
		}
		rows, err := st.R.Query(`SELECT t.id FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE r.request_id LIKE ? ORDER BY t.id`, tc.like)
		if err != nil {
			t.Fatal(err)
		}
		var set []string
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			set = append(set, id)
		}
		_ = rows.Close()
		lib := library.New(st, nil, time.Now)
		res, err := export.Build(context.Background(), exports.NewDBSource(st, lib, playlists.New(st, lib, nil, nil)),
			export.Selection{Kind: "tracks", Tracks: export.TrackFilter{TrackIDs: set}})
		_ = st.Close()
		if err != nil || len(set) != 2 {
			t.Fatalf("%s: %v (tracks %v)", name, err, set)
		}
		var got []string
		for _, r := range res.Rows {
			got = append(got, r.RequestID)
		}
		slices.Sort(got)
		if rep := yue2.ValidateBatch(res.JSONL, nil); !rep.OK || !slices.Equal(got, tc.want) {
			t.Errorf("%s: ids %v, issues %+v", name, got, rep.Issues)
		}
	}

	env := archiveEnv(t, "acceptance")
	// a plain --source export reports exactly the issues the validator finds in the original file
	orig := without(readSeed(t, synth.ProfileAcceptance, "music/songs.jsonl"), "{not json")
	want := issueSet(orig, yue2.ValidateBatch([]byte(orig), os.DirFS(filepath.Join(seedDir(t, synth.ProfileAcceptance), "music"))))
	if !slices.Equal(want, []string{"duplicate_id syn-song-01"}) {
		t.Fatalf("original issues %v", want)
	}
	dir := filepath.Join(t.TempDir(), "o")
	code, _, errs := do(t, env, "", "export", "--source", "music/songs.jsonl", "--out", dir)
	if code != 1 || !strings.Contains(errs, "1 issues") || !strings.Contains(errs, "duplicate_id") {
		t.Errorf("source export: exit %d, stderr %q", code, errs)
	}
	// e2e: the recipe JSONL's issues are those of its seed file
	env2 := archiveEnv(t, "e2e")
	seed := readSeed(t, synth.ProfileE2E, "music/songs.jsonl")
	wantE2E := issueSet(seed, yue2.ValidateBatch([]byte(seed), nil))
	code, _, errs = do(t, env2, "", "export", "--source", "music/songs.jsonl")
	if (len(wantE2E) == 0) != (code == 0) || !strings.Contains(errs, fmt.Sprintf("%d issues", len(wantE2E))) {
		t.Errorf("e2e source: exit %d, stderr %q, seed issues %v", code, errs, wantE2E)
	}
}
