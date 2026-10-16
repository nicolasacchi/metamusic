package synth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// Profile selects a synthetic archive layout (C21).
type Profile string

// Profiles. ProfileDev generates the same archive as ProfileE2E; ProfileReference is that archive
// plus recipe-less audio for the reference opt-ins (C66).
const (
	ProfileAcceptance Profile = "acceptance"
	ProfileE2E        Profile = "e2e"
	ProfileDev        Profile = "dev"
	ProfileReference  Profile = "reference"
)

// File is one generated path, inbox-relative with forward slashes.
type File struct {
	Path string
	Kind string // flac | mp3 | json | jsonl | csv | abc | npy | zip | symlink | fifo
}

// AudioSpec describes one rendered take, for tests that check the analysis.
type AudioSpec struct {
	Path        string
	RequestID   string
	Seed        int64
	LUFS        float64
	Seconds     float64
	BandLimitHz int
}

// Expect are the numbers an import of the archive (with Manifest.Mapping) must
// produce (C21).
type Expect struct {
	Recipes, RecipeSources, Tracks    int
	Drafts, DraftsBacklog             int
	DraftsFulfilled                   int
	Collections, CollectionDrafts     int
	Refs, SiblingEdges, Scores        int
	AudioFiles, AudioIdentities       int
	DuplicateGroups, Ignored          int
	RecipeLineErrors, AudioSkipped    int
	LoudnessSpreadLU                  float64
	ExitCode                          int
	BacklogGroups, BacklogCollection  int // list_drafts: groups_total and the size of the collection filter
	ReferenceRowsWithSeedMismatch     int
	TimedScores                       int
	ResultDirTakes, MappedTakes       int
	VariantiDrafts                    int
	SiblingRuleSameRequest, SiblingSF int
	// M2c (ProfileReference, imported with both reference flags)
	ReferenceTracks, LossyTracks, ReferenceGroups int
}

// Manifest lists everything Archive generated.
type Manifest struct {
	Profile Profile
	Files   []File
	Audio   []AudioSpec
	// Mapping is the operator mapping file content that goes with the profile
	// ("" when none is needed). It is not part of the inbox.
	Mapping string
	Expect  Expect
}

type archive struct {
	root  string
	files []File
	audio []AudioSpec
	jobs  []renderJob
	post  []func() error
	// ffmpeg is the encoder of the reference profile's MP3 files.
	ffmpeg string
	// short is Options.ShortAudio.
	short bool
}

type renderJob struct {
	rel   string
	tone  Tone
	style FLACStyle
	tags  []string // when set, the render is retagged in place
	// special, when non-zero, renders a purpose-built defect signal instead of a tone.
	special SpecialKind
}

func (a *archive) add(rel, kind string) { a.files = append(a.files, File{Path: rel, Kind: kind}) }

func (a *archive) abs(rel string) string { return filepath.Join(a.root, filepath.FromSlash(rel)) }

func (a *archive) text(rel, kind, content string) error {
	p := a.abs(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	a.add(rel, kind)
	return os.WriteFile(p, []byte(content), 0o644) // #nosec G306 -- synthetic inbox file
}

// flac registers a rendered take.
func (a *archive) flac(rel, id string, seed int64, tone Tone, style FLACStyle, tags ...string) {
	a.add(rel, "flac")
	a.audio = append(a.audio, AudioSpec{Path: rel, RequestID: id, Seed: seed, LUFS: tone.LUFS, Seconds: tone.Seconds, BandLimitHz: tone.BandLimitHz})
	a.jobs = append(a.jobs, renderJob{rel: rel, tone: tone, style: style, tags: tags})
}

// specialFLAC registers a defect-signal take (C55).
func (a *archive) specialFLAC(rel, id string, seed int64, kind SpecialKind, seconds float64) {
	a.add(rel, "flac")
	a.audio = append(a.audio, AudioSpec{Path: rel, RequestID: id, Seed: seed, Seconds: seconds})
	a.jobs = append(a.jobs, renderJob{rel: rel, tone: Tone{Seconds: seconds}, special: kind})
}

// npy registers a semantic.npy written after the directories exist.
func (a *archive) npy(rel string, o NPYOptions) {
	a.add(rel, "npy")
	a.post = append(a.post, func() error { return SemanticNPY(a.abs(rel), o) })
}

// copyOf registers a byte copy of an already registered render.
func (a *archive) copyOf(src, dst string) {
	a.add(dst, "flac")
	a.post = append(a.post, func() error { return copyFile(a.abs(src), a.abs(dst)) })
}

// retagOf registers a retagged copy (same frames, other bytes).
func (a *archive) retagOf(ctx context.Context, t mediatool.Tools, src, dst string, comments ...string) {
	a.add(dst, "flac")
	a.post = append(a.post, func() error { return Retag(ctx, t, a.abs(src), a.abs(dst), comments) })
}

// Archive writes the synthetic inbox for profile into dir (created if needed)
// and returns its Manifest. seed offsets the audio variants, nothing else; the
// same arguments give byte-identical text files and the same loudness.
func Archive(ctx context.Context, t mediatool.Tools, dir string, profile Profile, seed int64) (Manifest, error) {
	return ArchiveWith(ctx, t, dir, profile, seed, Options{})
}

// Options tunes an archive without changing its recipes, text files or expectations.
type Options struct {
	// ShortAudio renders the one take that only the player e2e specs walk to its natural end
	// ("Syn song 02") at ShortSeconds instead of 6 s, so a real play-through costs a couple of seconds.
	// Every other take keeps its length: the library duration filter, the seek, waveform, comment-pin,
	// listen and ears specs read them. The masters stay real, valid FLAC files.
	ShortAudio bool
}

// ShortSeconds is the length of the short e2e take (Options.ShortAudio).
const ShortSeconds = 2

// ArchiveWith is Archive with Options.
func ArchiveWith(ctx context.Context, t mediatool.Tools, dir string, profile Profile, seed int64, opts Options) (Manifest, error) {
	if err := needAbs(dir); err != nil {
		return Manifest{}, err
	}
	a := &archive{root: dir, short: opts.ShortAudio}
	var m Manifest
	var err error
	switch profile {
	case ProfileAcceptance:
		m, err = a.acceptance(ctx, t, seed)
	case ProfileE2E, ProfileDev:
		m, err = a.e2e(ctx, t, seed)
		m.Profile = profile
	case ProfileReference:
		m, err = a.reference(ctx, t, seed)
		m.Profile = profile
	default:
		return Manifest{}, fmt.Errorf("synth: unknown profile %q", profile)
	}
	if err != nil {
		return Manifest{}, err
	}
	if err := a.render(ctx, t); err != nil {
		return Manifest{}, err
	}
	for _, f := range a.post {
		if err := f(); err != nil {
			return Manifest{}, err
		}
	}
	if err := a.stamp(); err != nil {
		return Manifest{}, err
	}
	m.Files, m.Audio = a.files, a.audio
	return m, nil
}

// render runs the FLAC renders, a few at a time.
func (a *archive) render(ctx context.Context, t mediatool.Tools) error {
	for _, f := range a.files {
		if err := os.MkdirAll(filepath.Dir(a.abs(f.Path)), 0o750); err != nil {
			return err
		}
	}
	sem := make(chan struct{}, 4)
	errs := make([]error, len(a.jobs))
	var wg sync.WaitGroup
	for i, j := range a.jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			errs[i] = a.renderOne(ctx, t, j)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("synth: rendering %s: %w", a.jobs[i].rel, err)
		}
	}
	return nil
}

func (a *archive) renderOne(ctx context.Context, t mediatool.Tools, j renderJob) error {
	dst := a.abs(j.rel)
	if j.special != 0 {
		return Special(ctx, t, dst, j.special, j.tone.Seconds)
	}
	if len(j.tags) == 0 {
		return FLAC(ctx, t, dst, j.tone, j.style)
	}
	raw := dst + ".render"
	defer func() { _ = os.Remove(raw) }()
	if err := FLAC(ctx, t, raw, j.tone, j.style); err != nil {
		return err
	}
	return Retag(ctx, t, raw, dst, j.tags)
}

// stamp gives every file an mtime well in the past, in manifest order (so
// recorded_at and sibling roots are deterministic) and older than any MinAge.
func (a *archive) stamp() error {
	base := time.Now().Add(-6 * time.Hour).Truncate(time.Second)
	for i, f := range a.files {
		if f.Kind == "symlink" {
			continue
		}
		at := base.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(a.abs(f.Path), at, at); err != nil {
			return fmt.Errorf("synth: stamping %s: %w", f.Path, err)
		}
	}
	return nil
}

// ---- text builders ----------------------------------------------------------

// jsonStr renders s as a JSON string without escaping non-ASCII or HTML.
func jsonStr(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

const style = "synthetic pop, 120 bpm"

// line renders a request in `yue2 batch --input` format (sorted keys, Python
// separators). extra holds already formatted leading keys (sorted before "cot").
func line(id string, seed int, lyrics, cot, extra string) string {
	return fmt.Sprintf(`{%s"cot": %s, "id": %s, "lyrics": %s, "seed": %d, "style": %s}`,
		extra, jsonStr(cot), jsonStr(id), jsonStr(lyrics), seed, jsonStr(style))
}

// yue2Request writes request.json exactly like YuE2 (asdict order, indent 2, explicit nulls).
func yue2Request(id string, seed int, lyrics string) string {
	return fmt.Sprintf("{\n  \"style\": %s,\n  \"lyrics\": %s,\n  \"cot\": \"full\",\n  \"seed\": %d,\n  \"abc\": null,\n  \"cfg_scale\": null,\n  \"id\": %s\n}\n",
		jsonStr(style), jsonStr(lyrics), seed, jsonStr(id))
}

var syllables = []string{"la", "mo", "ri", "ne", "tu", "sa", "ko", "vi", "de", "pa"}

// lyrics3 returns three sections (verse, chorus, outro) of invented words
// derived from key.
func lyrics3(key string) string {
	w := func(n int) string {
		h := 0
		for _, c := range key {
			h = h*31 + int(c)
		}
		var b strings.Builder
		for i := 0; i < 3; i++ {
			h = h*1103515245 + 12345 + n
			if i > 0 {
				b.WriteString(" ")
			}
			b.WriteString(syllables[(h>>8&0xffff)%len(syllables)] + syllables[(h>>16&0xffff)%len(syllables)])
		}
		return b.String()
	}
	return fmt.Sprintf("[Verse 1]\n%s\n[Chorus]\n%s\n[Outro]\n%s", w(1), w(2), w(3))
}

func lyrics2(key string) string {
	l := lyrics3(key)
	return l[:strings.Index(l, "\n[Outro]")]
}

// abc3 is a timed score: 3 sections x 2 bars, 4/4 at 120 = 2 s per bar (12 s).
const abc3 = "X:1\nT:t\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n% verse\nCDEF|CDEF|\n% chorus\nCDEF|CDEF|\n% outro\nCDEF|CDEF|]\n"

const (
	lyricsEdge  = "[Verse 1]\nè à ì <b>&\n[Chorus]\n< > &\n[Outro]\n<b>&"
	lyricsHTML  = "[Verse 1]\nlamo <b>&\n[Chorus]\nrine tusa"
	shortBytes  = "ID3\x03\x00\x00\x00\x00\x00\x00synthetic"
	variantStep = 20
)

func (a *archive) variant(seed int64, n int) int { return n + int((seed-1)%5)*variantStep }

// backlog writes backlog-variants.jsonl (and optionally pilot files) and returns the lines.
func (a *archive) backlog() (all, pilot []string) {
	for g := 1; g <= 4; g++ {
		for v := 1; v <= 3; v++ {
			id := fmt.Sprintf("synb_%03d_v%d", g, v)
			l := line(id, 3000+g*10+v, lyrics2(id), "full", "")
			all = append(all, l)
			if g == 1 {
				pilot = append(pilot, l)
			}
		}
	}
	return all, pilot
}

func joinLines(ls ...string) string { return strings.Join(ls, "\n") + "\n" }

const pilotJSON = `{"name": "pilot-synthetic", "rule": "synthetic rule", "songs": [{"n": 1, "title": "Synthetic Title", "artist": "Synthetic Artist", "why": "synthetic why", "variant_ids": ["synb_001_v1", "synb_001_v2", "synb_001_v3"]}]}` + "\n"

func (a *archive) acceptance(ctx context.Context, t mediatool.Tools, seed int64) (Manifest, error) {
	v := func(n int) int { return a.variant(seed, n) }
	tone := func(lufs, secs float64, variant int) Tone {
		return Tone{LUFS: lufs, Seconds: secs, BPM: 120, Variant: v(variant)}
	}
	songs := joinLines(
		line("syn-song-01", 1001, lyrics2("syn-song-01"), "full", ""),
		line("syn-song-02", 1002, lyrics2("syn-song-02"), "full", ""),
		line("syn-song-03", 1003, lyricsEdge, "full", ""),
		line("syn-song-04", 1004, lyrics2("syn-song-04"), "full", ""),
		line("syn-song-05", 1005, lyrics3("syn-song-05"), "full", ""),
		line("syn-song-06", 1006, lyrics2("syn-song-06"), "full", ""),
		line("syn-song-07", 1007, lyrics2("syn-song-07"), "full", ""),
		`{not json`,
		line("syn-song-01", 2001, lyrics2("syn-song-01"), "full", ""),
		line("syn-song-02-s2002", 2002, lyrics2("syn-song-02"), "full", ""),
		line("syn-song-08", 1008, lyrics2("syn-song-08"), "melody", `"abc_path": "abc/syn-song-08.abc", `),
	)
	steps := []struct{ rel, kind, content string }{
		{"music/songs.jsonl", "jsonl", songs},
		{"music/abc/syn-song-08.abc", "abc", "X:1\nT:eight\nM:4/4\nL:1/8\nQ:1/4=100\nK:C\nCDEF|CDEF|]\n"},
		{"music/requests/syn-song-06.json", "json", line("syn-song-06", 1006, lyrics2("syn-song-06"), "full", "") + "\n"},
	}
	// per-song JSON: the same request plus a title (same hash, second source)
	steps[2].content = strings.TrimSuffix(steps[2].content, "}\n") + `, "title": "Synthetic Six"}` + "\n"
	for _, s := range steps {
		if err := a.text(s.rel, s.kind, s.content); err != nil {
			return Manifest{}, err
		}
	}

	// takes, in recorded order (sibling roots are the earliest)
	a.flac("music/flac/syn_song_01.flac", "syn-song-01", 1001, tone(-10, 6, 0), StyleYuE2)
	a.flac("music/flac/syn_song_01_s2001.flac", "syn-song-01", 2001, tone(-14, 6, 1), StyleYuE2)
	a.flac("music/flac/syn_song_02.flac", "syn-song-02", 1002, Tone{LUFS: -14, Seconds: 6, BandLimitHz: 8000, Variant: v(2)}, StyleYuE2)
	a.flac("music/flac/syn_song_02_s2002.flac", "syn-song-02-s2002", 2002, tone(-14, 6, 3), StyleYuE2)
	a.flac("music/flac/syn_song_03.flac", "syn-song-03", 1003, tone(-14, 12, 4), StyleYuE2)
	a.flac("music/flac/syn_song_04.flac", "syn-song-04", 1004, tone(-20, 6, 5), StyleYuE2)
	a.flac("music/flac/take_final.flac", "syn-song-07", 1007, tone(-14, 6, 6), StyleYuE2, "MM_REQUEST_ID=syn-song-07")
	a.flac("music/flac/synthetic_six.flac", "syn-song-06", 1006, tone(-14, 6, 7), StyleYuE2)
	a.flac("music/results/syn-song-05/audio.flac", "syn-song-05", 1005, tone(-14, 12, 8), StyleYuE2)
	a.copyOf("music/results/syn-song-05/audio.flac", "music/flac/syn_song_05.flac")
	if err := a.text("music/flac/syn_song_04.mp3", "mp3", shortBytes+"04"); err != nil {
		return Manifest{}, err
	}
	a.flac("music/flac/.syn_song_06.flac.Xy12Ab", "syn-song-06", 1006, tone(-14, 6, 9), StyleYuE2)
	a.add("music/flac/link_to_02.flac", "symlink")
	a.add("music/flac/pipe.flac", "fifo")
	a.copyOf("music/flac/syn_song_04.flac", "music/audio/syn_song_04.flac")
	a.retagOf(ctx, t, "music/flac/syn_song_04.flac", "music/player-export/Syn Song 04.flac", "TITLE=Syn Song 04")
	for _, n := range []string{"syn_song_02", "syn_song_03", "take_final", "orphan_take"} {
		if err := a.text("music/mp3/"+n+".mp3", "mp3", shortBytes+n); err != nil {
			return Manifest{}, err
		}
	}

	rd := "music/results/syn-song-05/"
	for _, s := range []struct{ rel, kind, content string }{
		{rd + "request.json", "json", yue2Request("syn-song-05", 1005, lyrics3("syn-song-05"))},
		{rd + "result.json", "json", `{"truncated": {"abc": false, "semantic": true}, "duration": 12.0}` + "\n"},
		{rd + "config.json", "json", `{"model": "synthetic"}` + "\n"},
		{rd + "plan.json", "json", `{"sections": 3}` + "\n"},
		{rd + "score.abc", "abc", abc3},
		{rd + "latent.npy", "npy", "npy-latent"},
		{"music/scores/syn_song_03.abc", "abc", abc3},
	} {
		if err := a.text(s.rel, s.kind, s.content); err != nil {
			return Manifest{}, err
		}
	}

	a.npy(rd+"semantic.npy", NPYOptions{TokensPerS: 50, Seconds: 12, Vocab: 1024, Seed: 1005})

	all, pilot := a.backlog()
	var csv strings.Builder
	csv.WriteString("\xef\xbb\xbfvariant_id,track_n,source_title,source_artist,variant,variant_type,seed\r\n")
	n := 0
	for g := 1; g <= 4; g++ {
		for vv := 1; vv <= 3; vv++ {
			n++
			sd := 3000 + g*10 + vv
			if g == 3 && vv == 2 {
				sd = 99999 // differs from the JSONL line
			}
			fmt.Fprintf(&csv, "synb_%03d_v%d,%d,Synthetic Title %d,Synthetic Artist %d,v%d,type%d,%d\r\n", g, vv, n, g, g, vv, vv, sd)
		}
	}
	for _, s := range []struct{ rel, kind, content string }{
		{"music/backlog-variants.jsonl", "jsonl", joinLines(all...)},
		{"music/pilot.jsonl", "jsonl", joinLines(pilot...)},
		{"music/pilot.json", "json", pilotJSON},
		{"music/index.csv", "csv", csv.String()},
		{"music/playlist.json", "json", `{"playlist": "synthetic", "owner": "synthetic owner", "url": "https://mm.example.com/p", "note": "", "tracks": [{"title": "Synthetic Playlist Title", "artist": "Synthetic Playlist Artist"}]}` + "\n"},
		{"music/review.json", "json", `{"pass": true, "files": 3, "errors": [], "warnings": []}` + "\n"},
		{"music/old-batch.zip", "zip", "PK\x03\x04../x"},
		{"varianti/syn-song-01-v2.json", "json", line("syn-song-01-v2", 4001, lyrics2("syn-song-01-v2"), "full", "") + "\n"},
	} {
		if err := a.text(s.rel, s.kind, s.content); err != nil {
			return Manifest{}, err
		}
	}
	a.flac("unmatched/mystery.flac", "", 0, tone(-14, 3, 10), StyleYuE2)

	// the symlink and the FIFO are created now (their targets exist after render)
	for _, d := range []string{"music/flac"} {
		if err := os.MkdirAll(a.abs(d), 0o750); err != nil {
			return Manifest{}, err
		}
	}
	if err := os.Symlink("syn_song_02.flac", a.abs("music/flac/link_to_02.flac")); err != nil {
		return Manifest{}, err
	}
	if err := syscall.Mkfifo(a.abs("music/flac/pipe.flac"), 0o644); err != nil {
		return Manifest{}, fmt.Errorf("synth: creating FIFO: %w", err)
	}

	return Manifest{
		Profile: ProfileAcceptance,
		Mapping: "music/flac/syn_song_01.flac,syn-song-01,1001,,\nunmatched/mystery.flac,-,,,\n",
		Expect: Expect{
			Recipes: 23, RecipeSources: 28, Tracks: 9, Drafts: 23, DraftsBacklog: 14, DraftsFulfilled: 9,
			Collections: 1, CollectionDrafts: 3, Refs: 11, SiblingEdges: 2, Scores: 2, TimedScores: 2,
			AudioFiles: 13, AudioIdentities: 10, DuplicateGroups: 2, Ignored: 12, RecipeLineErrors: 1, AudioSkipped: 1,
			LoudnessSpreadLU: 10.0, ExitCode: 1, BacklogGroups: 6, BacklogCollection: 3, ReferenceRowsWithSeedMismatch: 1,
			ResultDirTakes: 1, MappedTakes: 1, VariantiDrafts: 1, SiblingRuleSameRequest: 1, SiblingSF: 1,
		},
	}, nil
}

// e2e builds the e2e/dev profile: 9 takes (6 songs, 3 ears), 12 backlog recipes, 1 varianti draft.
func (a *archive) e2e(ctx context.Context, t mediatool.Tools, seed int64) (Manifest, error) {
	v := func(n int) int { return a.variant(seed, n) }
	songs := joinLines(
		line("syn-song-01", 1001, lyrics2("syn-song-01"), "full", ""),
		line("syn-song-01", 2001, lyrics2("syn-song-01"), "full", ""),
		line("syn-song-02", 1002, lyrics2("syn-song-02"), "full", ""),
		line("syn-song-03", 1003, lyrics3("syn-song-03"), "full", ""),
		line("syn-song-04", 1004, lyricsHTML, "full", ""),
		line("syn-song-05", 1005, lyrics2("syn-song-05"), "full", ""),
		line("syn-ears-clip", 5001, lyrics2("syn-ears-clip"), "full", ""),
		line("syn-ears-gap", 5002, lyrics2("syn-ears-gap"), "full", ""),
		line("syn-ears-loop", 5003, lyrics3("syn-ears-loop"), "full", ""),
	)
	all, _ := a.backlog()
	for _, s := range []struct{ rel, kind, content string }{
		{"music/songs.jsonl", "jsonl", songs},
		{"music/scores/syn_song_03.abc", "abc", abc3},
		{"music/backlog-variants.jsonl", "jsonl", joinLines(all...)},
		{"music/pilot.json", "json", pilotJSON},
		{"varianti/syn-song-01-v2.json", "json", line("syn-song-01-v2", 4001, lyrics2("syn-song-01-v2"), "full", "") + "\n"},
	} {
		if err := a.text(s.rel, s.kind, s.content); err != nil {
			return Manifest{}, err
		}
	}
	a.flac("music/flac/syn_song_01_s1001.flac", "syn-song-01", 1001, Tone{LUFS: -12, Seconds: 6, BPM: 120, Variant: v(0)}, StyleYuE2)
	a.flac("music/flac/syn_song_01_s2001.flac", "syn-song-01", 2001, Tone{LUFS: -14, Seconds: 8, BPM: 100, Variant: v(1)}, StyleYuE2)
	song02 := 6.0
	if a.short {
		song02 = ShortSeconds
	}
	a.flac("music/flac/syn_song_02.flac", "syn-song-02", 1002, Tone{LUFS: -15, Seconds: song02, BPM: 90, Variant: v(2)}, StyleYuE2)
	a.flac("music/flac/syn_song_03.flac", "syn-song-03", 1003, Tone{LUFS: -14, Seconds: 12, BPM: 120, Variant: v(3)}, StyleYuE2)
	a.flac("music/flac/syn_song_04.flac", "syn-song-04", 1004, Tone{LUFS: -16, Seconds: 7, BPM: 110, Variant: v(4)}, StyleYuE2)
	a.flac("music/flac/syn_song_05.flac", "syn-song-05", 1005, Tone{LUFS: -13, Seconds: 9, BPM: 130, Variant: v(5)}, StyleYuE2)

	// ears group (C55): one defect per take
	a.specialFLAC("music/flac/syn_ears_clip.flac", "syn-ears-clip", 5001, SpecialClipBursts, 8)
	a.specialFLAC("music/flac/syn_ears_gap.flac", "syn-ears-gap", 5002, SpecialGap, 10)
	rd := "music/results/syn-ears-loop/"
	a.flac(rd+"audio.flac", "syn-ears-loop", 5003, Tone{LUFS: -14, Seconds: 12, BPM: 120, Variant: v(6)}, StyleYuE2)
	for _, s := range []struct{ rel, kind, content string }{
		{rd + "request.json", "json", yue2Request("syn-ears-loop", 5003, lyrics3("syn-ears-loop"))},
		{rd + "result.json", "json", `{"truncated": {"abc": false, "semantic": false}, "duration": 12.0}` + "\n"},
		{rd + "score.abc", "abc", abc3},
	} {
		if err := a.text(s.rel, s.kind, s.content); err != nil {
			return Manifest{}, err
		}
	}
	a.npy(rd+"semantic.npy", NPYOptions{TokensPerS: 50, Seconds: 12, Vocab: 1024, Seed: 5003, LoopAtS: 3, Period: 25, Repeats: 12})
	return Manifest{
		Expect: Expect{
			Recipes: 22, RecipeSources: 22, Tracks: 9, Drafts: 22, DraftsBacklog: 13, DraftsFulfilled: 9,
			Collections: 1, CollectionDrafts: 3, SiblingEdges: 1, Scores: 2, TimedScores: 2,
			AudioFiles: 9, AudioIdentities: 9, BacklogGroups: 5, BacklogCollection: 3, VariantiDrafts: 1,
			SiblingRuleSameRequest: 1, LoudnessSpreadLU: 12.8, ExitCode: 0,
		},
	}, nil
}

// mp3 registers an MP3 encoded after the renders from the FLAC at src (44.1 kHz stereo, 128 kbps).
// When src is empty a throw-away FLAC of tone is rendered first.
func (a *archive) mp3(ctx context.Context, rel, src string, tone Tone) {
	a.add(rel, "mp3")
	if src == "" {
		src = rel + ".src.flac"
		a.jobs = append(a.jobs, renderJob{rel: src, tone: tone, style: StyleYuE2})
	}
	a.post = append(a.post, func() error {
		dst := a.abs(rel)
		tmp := dst + ".tmp.mp3"
		err := run(ctx, a.ffmpeg, ffmpegArgs("-i", a.abs(src), "-ac", "2", "-ar", "44100",
			"-codec:a", "libmp3lame", "-b:a", "128k", "-map_metadata", "-1", "-id3v2_version", "0", "-write_xing", "0", tmp)...)
		if src == rel+".src.flac" {
			_ = os.Remove(a.abs(src))
		}
		return publish(tmp, dst, err)
	})
}

// reference is the e2e archive plus recipe-less audio (C66): two unmatched FLACs, six standalone MP3
// (two lossy reference groups of three and two members, plus a lone file), a fake MP3 that stays an
// orphan and an MP3 sidecar of one unmatched FLAC.
func (a *archive) reference(ctx context.Context, t mediatool.Tools, seed int64) (Manifest, error) {
	a.ffmpeg = t.FFmpeg
	m, err := a.e2e(ctx, t, seed)
	if err != nil {
		return Manifest{}, err
	}
	v := func(n int) int { return a.variant(seed, n) }
	tone := func(variant int, secs float64) Tone {
		return Tone{LUFS: -14, Seconds: secs, BPM: 100, Variant: v(variant)}
	}
	a.flac("music/ref/syn_ref_a.flac", "", 0, tone(20, 5), StyleYuE2)
	a.flac("music/ref/syn_ref_b.flac", "", 0, tone(21, 5), StyleYuE2)
	a.mp3(ctx, "music/ref/syn_ref_a.mp3", "music/ref/syn_ref_a.flac", Tone{})
	for i, n := range []string{"syn_trio_p1", "syn_trio_p2", "syn_trio_p3", "syn_lone", "syn_pair_p2", "syn_pair_p5"} {
		a.mp3(ctx, "music/lossy/"+n+".mp3", "", tone(30+i, 4))
	}
	if err := a.text("music/lossy/syn_fake.mp3", "mp3", shortBytes+"fake"); err != nil {
		return Manifest{}, err
	}
	e := &m.Expect
	e.Tracks += 8
	e.AudioFiles += 8
	e.AudioIdentities += 8
	e.SiblingEdges += 3
	e.Ignored += 2 // the fake MP3 and the sidecar
	e.ReferenceTracks, e.LossyTracks, e.ReferenceGroups = 8, 6, 2
	return m, nil
}
