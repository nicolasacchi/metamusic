package liner_test

import (
	"flag"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/liner"
)

var update = flag.Bool("update", false, "rewrite golden files")

func ip(n int) *int       { return &n }
func i64(n int64) *int64  { return &n }
func sp(s string) *string { return &s }
func lines(l liner.Liner) []string {
	out := make([]string, len(l.Lines))
	for i, ln := range l.Lines {
		out[i] = ln.Text
	}
	return out
}

func track(pos int, title string) liner.Track {
	return liner.Track{Position: pos, Title: title, Angle: "slow-burn", Engine: "yue2", DurationS: 200 + float64(pos)}
}

// fixture is the shared synthetic Record behind the goldens.
func fixture(aud liner.Audience, museNames bool) liner.Input {
	return liner.Input{
		Audience: aud, Title: "Synthetic Record 001", Subtitle: "a quiet one", Date: "2026-10-28", MuseNames: museNames,
		Note: "Written for late evenings.\nSynthetic text only.",
		Tracks: []liner.Track{
			{Position: 1, Side: "A", Title: "Synthetic Song One", Angle: "slow-burn", Engine: "yue2", DurationS: 215.4, BPM: ip(92), BPMBasis: "measured",
				Families: []string{"ambient", "folk"}, Why: "won 3 blind duels", Mode: "floating", SessionLabel: "listening 2026-10-01",
				CostMicroUSD: i64(1_230_000), SessionName: "Synthetic Session Alpha", Muse: &liner.MuseLine{Title: "Synthetic Muse Title", Artist: sp("Synthetic Artist")}},
			{Position: 2, Side: "A", Title: "Synthetic Song Two", Angle: "bright", Engine: "yue2", DurationS: 187, BPM: ip(120), BPMBasis: "declared",
				Families: []string{"pop"}, Why: "4★ · undecided", Mode: "pinned", Lossy: true, Resampled: true, SessionLabel: "listening 2026-10-02",
				CostMicroUSD: i64(500_000), SessionName: "Synthetic Session Beta"},
			{Position: 3, Side: "B", Title: "Synthetic Song Three", Angle: "slow-burn", Engine: "yue3", DurationS: 3725, Why: "newest take · undecided", Mode: "floating",
				Upmixed: true, Families: []string{"ambient"}, SessionLabel: "listening 2026-10-03", Muse: &liner.MuseLine{Title: "Synthetic Muse Two"}},
			{Position: 4, Side: "B", Title: "Synthetic Song Four", Mode: "floating"},
		},
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := "testdata/" + name
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs (run with -update):\n%s", name, got)
	}
}

func TestLiner_AppGolden(t *testing.T) {
	t.Parallel()
	golden(t, "liner_app.golden.md", liner.Build(fixture(liner.AudienceApp, false)).Markdown())
}

func TestLiner_ExportGolden(t *testing.T) {
	t.Parallel()
	golden(t, "liner_export.golden.md", liner.Build(fixture(liner.AudienceExport, false)).Markdown())
	golden(t, "liner_export_muses.golden.md", liner.Build(fixture(liner.AudienceExport, true)).Markdown())
}

func TestLiner_Build_Table(t *testing.T) {
	t.Parallel()
	app, exp := liner.AudienceApp, liner.AudienceExport
	long := make([]liner.Track, 400)
	for i := range long {
		long[i] = track(i+1, "Synthetic Song "+strings.Repeat("x", 40)+strconv.Itoa(i))
	}
	many := make([]liner.Track, 3)
	for i, f := range []string{"c", "a", "b"} {
		many[i] = track(i+1, "S"+f)
		many[i].Families = []string{"f-" + f, "f-z", "f-y", "f-x"}
	}
	cases := []struct {
		name  string
		in    liner.Input
		check func(t *testing.T, l liner.Liner, md string)
	}{
		{"app basic", fixture(app, false), func(t *testing.T, l liner.Liner, md string) {
			want := "[A] Synthetic Song One — slow-burn · yue2 · 3:35 · 92 BPM · ambient, folk · won 3 blind duels · session Synthetic Session Alpha · $1.23 · inspired by Synthetic Muse Title — Synthetic Artist"
			if lines(l)[0] != want {
				t.Errorf("line 1 = %q", lines(l)[0])
			}
			if !strings.Contains(md, "Total cost: $1.73") {
				t.Errorf("no cost total:\n%s", md)
			}
		}},
		{"export basic", fixture(exp, false), func(t *testing.T, l liner.Liner, md string) {
			want := "[A] Synthetic Song One — slow-burn · yue2 · 3:35 · 92 BPM · ambient, folk · won 3 blind duels · listening 2026-10-01"
			if lines(l)[0] != want {
				t.Errorf("line 1 = %q", lines(l)[0])
			}
			if len(l.Lines) != 3 || strings.Contains(md, "Total cost") {
				t.Errorf("export lines %d", len(l.Lines))
			}
		}},
		{"export ignores cost session muse", fixture(exp, false), func(t *testing.T, l liner.Liner, md string) {
			for _, s := range []string{"$", "Alpha", "Synthetic Muse", "inspired", "session "} {
				if strings.Contains(md, s) {
					t.Errorf("export carries %q", s)
				}
			}
		}},
		{"export with muse names", fixture(exp, true), func(t *testing.T, l liner.Liner, md string) {
			if !strings.Contains(lines(l)[0], "inspired by Synthetic Muse Title — Synthetic Artist") || !strings.Contains(lines(l)[2], "inspired by Synthetic Muse Two") {
				t.Errorf("muse lines: %q", lines(l))
			}
			if strings.Contains(md, "$") || strings.Contains(md, "Alpha") {
				t.Error("MuseNames must not unlock cost or session names")
			}
		}},
		{"lossy notice", liner.Input{Audience: exp, Tracks: []liner.Track{
			{Position: 1, Title: "a", DurationS: 10, Lossy: true}, {Position: 2, Title: "b", DurationS: 10, Lossy: true},
			{Position: 3, Title: "c", DurationS: 10}, {Position: 4, Title: "d", DurationS: 10}, {Position: 5, Title: "e", DurationS: 10}}},
			func(t *testing.T, l liner.Liner, _ string) {
				if len(l.Notices) != 1 || l.Notices[0] != "Contains lossy sources (2 of 5 tracks)." || !l.Summary.ContainsLossy || l.Summary.LossyN != 2 {
					t.Errorf("%v %+v", l.Notices, l.Summary)
				}
			}},
		{"resampled and upmixed", liner.Input{Audience: exp, Tracks: []liner.Track{
			{Position: 1, Title: "a", DurationS: 10, Resampled: true, Upmixed: true}, {Position: 2, Title: "b", DurationS: 10, Resampled: true}}},
			func(t *testing.T, l liner.Liner, _ string) {
				if strings.Join(l.Notices, "|") != "2 tracks resampled to 48 kHz|1 track mono, duplicated to stereo" {
					t.Errorf("%v", l.Notices)
				}
			}},
		{"empty slot notice only for app", liner.Input{Audience: app, Tracks: []liner.Track{track(1, "a"), {Position: 2}, {Position: 3}}},
			func(t *testing.T, l liner.Liner, _ string) {
				if len(l.Notices) != 1 || l.Notices[0] != "2 slots have no playable take yet" || l.Summary.Songs != 3 || l.Summary.Playable != 1 {
					t.Errorf("%v %+v", l.Notices, l.Summary)
				}
				if lines(l)[1] != "(no playable take yet)" {
					t.Errorf("%q", lines(l))
				}
			}},
		{"empty slot hidden for export", liner.Input{Audience: exp, Tracks: []liner.Track{track(1, "a"), {Position: 2}}},
			func(t *testing.T, l liner.Liner, _ string) {
				if len(l.Notices) != 0 || len(l.Lines) != 1 || l.Summary.Songs != 1 {
					t.Errorf("%v %v %+v", l.Notices, l.Lines, l.Summary)
				}
			}},
		{"pinned why prefix", liner.Input{Audience: exp, Tracks: []liner.Track{
			{Position: 1, Title: "a", DurationS: 10, Mode: "pinned", Why: "4★"}, {Position: 2, Title: "b", DurationS: 10, Mode: "pinned"},
			{Position: 3, Title: "c", DurationS: 10, Mode: "pinned", Why: "pinned as the song's Pick"}}},
			func(t *testing.T, l liner.Liner, _ string) {
				if got := lines(l); got[0] != "a — 0:10 · pinned · 4★" || got[1] != "b — 0:10 · pinned" || got[2] != "c — 0:10 · pinned as the song's Pick" {
					t.Errorf("%q", got)
				}
			}},
		{"side letters", liner.Input{Audience: exp, Tracks: []liner.Track{{Position: 1, Side: "A", Title: "a", DurationS: 5}, {Position: 2, Title: "b", DurationS: 5}}},
			func(t *testing.T, l liner.Liner, _ string) {
				if got := lines(l); got[0] != "[A] a — 0:05" || got[1] != "b — 0:05" {
					t.Errorf("%q", got)
				}
			}},
		{"engines and angles distinct sorted", liner.Input{Audience: exp, Tracks: []liner.Track{
			{Position: 1, Title: "a", DurationS: 5, Engine: "yue3", Angle: "z"}, {Position: 2, Title: "b", DurationS: 5, Engine: "yue2", Angle: "a"},
			{Position: 3, Title: "c", DurationS: 5, Engine: "yue3", Angle: "a"}}},
			func(t *testing.T, l liner.Liner, _ string) {
				if strings.Join(l.Summary.Engines, ",") != "yue2,yue3" || strings.Join(l.Summary.Angles, ",") != "a,z" {
					t.Errorf("%+v", l.Summary)
				}
			}},
		{"families top three", liner.Input{Audience: exp, Tracks: many},
			func(t *testing.T, l liner.Liner, _ string) {
				if strings.Join(l.Summary.Families, ",") != "f-x,f-y,f-z" { // the three shared by every track win; ties by name
					t.Errorf("%v", l.Summary.Families)
				}
			}},
		{"bpm missing and basis", liner.Input{Audience: exp, Tracks: []liner.Track{
			{Position: 1, Title: "a", DurationS: 5}, {Position: 2, Title: "b", DurationS: 5, BPM: ip(100), BPMBasis: "plan"}}},
			func(t *testing.T, l liner.Liner, _ string) {
				if got := lines(l); strings.Contains(got[0], "BPM") || got[1] != "b — 0:05 · 100 BPM (plan)" {
					t.Errorf("%q", got)
				}
			}},
		{"long durations", liner.Input{Audience: exp, Tracks: []liner.Track{
			{Position: 1, Title: "a", DurationS: 3599.4}, {Position: 2, Title: "b", DurationS: 3600}, {Position: 3, Title: "c", DurationS: 36061.5}}},
			func(t *testing.T, l liner.Liner, _ string) {
				if got := lines(l); got[0] != "a — 59:59" || got[1] != "b — 1:00:00" || got[2] != "c — 10:01:02" || l.Summary.Duration != "12:01:01" {
					t.Errorf("%q %s", got, l.Summary.Duration)
				}
			}},
		{"cost formats", liner.Input{Audience: app, Tracks: []liner.Track{
			{Position: 1, Title: "a", DurationS: 5, CostMicroUSD: i64(0)}, {Position: 2, Title: "b", DurationS: 5, CostMicroUSD: i64(4999)},
			{Position: 3, Title: "c", DurationS: 5, CostMicroUSD: i64(5000)}, {Position: 4, Title: "d", DurationS: 5, CostMicroUSD: i64(1_230_000)}}},
			func(t *testing.T, l liner.Liner, md string) {
				got := lines(l)
				if !strings.HasSuffix(got[0], "$0.00") || !strings.HasSuffix(got[1], "$0.00") || !strings.HasSuffix(got[2], "$0.01") || !strings.HasSuffix(got[3], "$1.23") {
					t.Errorf("%q", got)
				}
				if !strings.Contains(md, "Total cost: $1.24") { // 1_234_999 micro-USD
					t.Errorf("total:\n%s", md)
				}
			}},
		{"zero cost total", liner.Input{Audience: app, Tracks: []liner.Track{{Position: 1, Title: "a", DurationS: 5, CostMicroUSD: i64(0)}}},
			func(t *testing.T, _ liner.Liner, md string) {
				if !strings.Contains(md, "Total cost: $0.00") {
					t.Errorf("total:\n%s", md)
				}
			}},
		{"8 KB cut at a whole line", liner.Input{Audience: exp, Title: "T", Tracks: long}, func(t *testing.T, l liner.Liner, md string) {
			if len(md) > liner.MaxMarkdownBytes || !strings.HasSuffix(md, "not shown: the list was cut at 8 KB.\n") {
				t.Errorf("len %d tail %q", len(md), md[max(0, len(md)-80):])
			}
			for _, row := range strings.Split(md, "\n") {
				if strings.HasPrefix(row, "1") && strings.Contains(row, ". Synthetic Song") && !strings.HasSuffix(row, "0:00") && !strings.Contains(row, "·") {
					t.Errorf("a cut line: %q", row)
				}
			}
		}},
		{"description is bounded", liner.Input{Audience: exp, Tracks: long, Title: "T"}, func(t *testing.T, l liner.Liner, _ string) {
			l.Notices = append(l.Notices, strings.Repeat("é", 2000))
			if d := l.Description(); utf8.RuneCountInString(d) > 1000 || !utf8.ValidString(d) || !strings.HasSuffix(d, "…") {
				t.Errorf("description %d runes", utf8.RuneCountInString(d))
			}
		}},
		{"overlong note is cut", liner.Input{Audience: exp, Title: "T", Note: strings.Repeat("é", 20000), Tracks: []liner.Track{track(1, "a")}}, func(t *testing.T, _ liner.Liner, md string) {
			if len(md) > liner.MaxMarkdownBytes || !utf8.ValidString(md) {
				t.Errorf("len %d valid %v", len(md), utf8.ValidString(md))
			}
		}},
		{"operator text cannot open a block", liner.Input{Audience: exp, Title: "T\n# x", Tracks: []liner.Track{{Position: 1, Title: "a\n## b", DurationS: 5}}}, func(t *testing.T, l liner.Liner, md string) {
			if strings.Contains(md, "\n## b") || strings.Contains(md, "\n# x") {
				t.Errorf("injected block:\n%s", md)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			l := liner.Build(c.in)
			md := l.Markdown()
			if again := liner.Build(c.in).Markdown(); again != md {
				t.Error("Build is not deterministic")
			}
			if len(md) > liner.MaxMarkdownBytes || utf8.RuneCountInString(l.Description()) > 1000 {
				t.Errorf("size: md %d description %d", len(md), utf8.RuneCountInString(l.Description()))
			}
			c.check(t, l, md)
		})
	}
	if len(cases) < 17 {
		t.Errorf("%d rows", len(cases))
	}
}

// TestLiner_ExportIgnoresPrivateFields: canary strings in cost, session and Muse never appear in the export bytes, with or
// without MuseNames for cost and session, and Muse text appears only with the opt-in.
func TestLiner_ExportIgnoresPrivateFields(t *testing.T) {
	t.Parallel()
	in := liner.Input{Audience: liner.AudienceExport, Title: "Synthetic Record 002", Tracks: []liner.Track{{
		Position: 1, Title: "Synthetic Song", DurationS: 100, SessionLabel: "listening 2026-10-01",
		CostMicroUSD: i64(987_654_321), SessionName: "CANARY-SESSION", Muse: &liner.MuseLine{Title: "CANARY-MUSE-TITLE", Artist: sp("CANARY-MUSE-ARTIST")},
	}}}
	for _, muses := range []bool{false, true} {
		in.MuseNames = muses
		l := liner.Build(in)
		all := l.Markdown() + "\n" + l.Description()
		for _, s := range []string{"CANARY-SESSION", "987", "$", "654"} {
			if strings.Contains(all, s) {
				t.Errorf("MuseNames=%v: export carries %q", muses, s)
			}
		}
		if got := strings.Contains(all, "CANARY-MUSE-TITLE"); got != muses {
			t.Errorf("MuseNames=%v: muse title present = %v", muses, got)
		}
		if strings.Contains(l.Description(), "CANARY-MUSE") {
			t.Error("Description carries Muse text")
		}
	}
	// the app audience ignores the export opt-in: Muse text is always on
	in.Audience, in.MuseNames = liner.AudienceApp, false
	if md := liner.Build(in).Markdown(); !strings.Contains(md, "CANARY-MUSE-TITLE") || !strings.Contains(md, "CANARY-SESSION") || !strings.Contains(md, "$987.65") {
		t.Errorf("app notes lack their private fields:\n%s", md)
	}
	// an unknown audience is treated as export
	in.Audience = "other"
	if md := liner.Build(in).Markdown(); strings.Contains(md, "CANARY") {
		t.Error("unknown audience leaks")
	}
	// the app cost total never reaches Description
	in.Audience = liner.AudienceApp
	if d := liner.Build(in).Description(); strings.Contains(d, "$") || strings.Contains(d, "CANARY") {
		t.Errorf("app Description: %q", d)
	}
}

func TestLiner_FrozenSignatures(t *testing.T) {
	t.Parallel()
	if liner.AudienceApp != "app" || liner.AudienceExport != "export" {
		t.Error("an audience name changed")
	}
	if l := liner.Build(liner.Input{Audience: liner.AudienceExport, Title: "Synthetic Record 001"}); l.Title != "Synthetic Record 001" || !strings.HasPrefix(l.Markdown(), "# Synthetic Record 001\n") {
		t.Errorf("empty input: %+v", l)
	}
}
