// Package trackctx builds the /context bundle (contract C35): everything an
// agent needs to critique and regenerate one take, in one document whose size
// is measured and bounded as the REST body carries it.
package trackctx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/ladder"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/songwriting"
)

// Limits (UTF-16 units of the REST body).
const (
	DefaultLimit = 24000
	IncludeLimit = 96000
)

// Defaults of the trimmable lists.
const (
	defaultComments = 20
	includeComments = 500
	styleKeep       = 500
	briefKeep       = 200 // muse.brief after the siblings are gone (C98)
)

// Bundle is the mm.context/1 document. Sub-objects are open JSON objects whose
// shape C35 documents; unknown values are null, never invented. Marshalling
// writes the keys in the order C35 lists them.
type Bundle struct {
	Format string         `json:"format" example:"mm.context/1"`
	Track  map[string]any `json:"track"`
	Song   map[string]any `json:"song" nullable:"true" doc:"The take's Song with its Pick and up to 12 sibling takes; null while the Song is pending or when the library services are absent"`
	Muse   map[string]any `json:"muse" nullable:"true" doc:"The Song's Muse (title, artist or brief up to 500 characters); null without a Muse"`
	Sounds map[string]any `json:"sounds" nullable:"true" doc:"Sound facets and the tempo check of the take; null without a Song"`
	// Curation (L2, C130.4) lists the Records and Crates the take is in; absent unless UseCuration wired a reader.
	Curation   map[string]any `json:"curation,omitempty" nullable:"true" doc:"Records whose slot resolves to this take and manual Crates holding it or its Song; absent when there are none"`
	Recipe     map[string]any `json:"recipe"`
	Provenance map[string]any `json:"provenance"`
	Score      map[string]any `json:"score" nullable:"true"`
	Lineage    map[string]any `json:"lineage"`
	Judgments  map[string]any `json:"judgments"`
	Perception map[string]any `json:"perception"`
	Listening  map[string]any `json:"listening"`
	Links      map[string]any `json:"_links"`
	// Songwriting holds the operator's duration targets; absent unless UseSongwriting wired a source.
	Songwriting map[string]any `json:"songwriting,omitempty"`
	NextActions []string       `json:"next_actions" nullable:"false"`
	Budget      Budget         `json:"_budget"`
}

// Budget reports the size of the bundle as the REST body carries it.
type Budget struct {
	Chars     int      `json:"chars"`
	Limit     int      `json:"limit"`
	Truncated []string `json:"truncated" nullable:"false"`
}

// ---- ordered JSON ----

// omap is a JSON object that keeps its insertion order.
type omap struct {
	keys []string
	vals map[string]any
}

func newOmap(kv ...any) *omap {
	o := &omap{vals: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// Set stores v under k, appending k to the order when new.
func (o *omap) Set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// Get returns the value under k (nil when absent).
func (o *omap) Get(k string) any { return o.vals[k] }

// MarshalJSON writes the keys in insertion order.
func (o *omap) MarshalJSON() ([]byte, error) {
	if o == nil {
		return []byte("null"), nil
	}
	return writeObject(o.keys, func(k string) any { return o.vals[k] })
}

func writeObject(keys []string, get func(string) any) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(get(k))
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// recipeUnknownNote is the note of a reference track's recipe section (C63).
const recipeUnknownNote = "recipe unknown: this is a reference track; no style, lyrics or seed exist"

// Key order of the sections (C35); keys not listed follow, sorted.
var order = map[string][]string{
	"song":        {"id", "title", "angle", "state", "takes", "pick", "open_proposals", "pending", "siblings"},
	"muse":        {"id", "kind", "title", "artist", "brief"},
	"sounds":      {"taxonomy_version", "facets", "tempo"},
	"curation":    {"records", "crates"},
	"track":       {"id", "title", "status", "stars", "critic_stars", "critic_verdict", "tags", "duration_s", "recipe_known", "lossy", "recorded_at", "url"},
	"recipe":      {"engine", "known", "hash", "request", "note"},
	"provenance":  {"origin", "import_rule", "truncated", "cost", "env", "job_id"},
	"score":       {"source", "bars", "meter", "q", "seconds_per_bar", "aligned", "abc", "abc_truncated"},
	"lineage":     {"path", "operator", "inferred", "rule", "rationale", "children", "siblings"},
	"judgments":   {"comments", "comments_total", "open_fix", "ladder"},
	"perception":  {"tier", "lufs_i", "true_peak_db", "lra_lu", "bandwidth_hz", "sections", "timed", "flags", "blind_spots", "tempo_bpm", "tempo_confidence"},
	"listening":   {"plays", "completion_rate", "last_played_at"},
	"_links":      {"stream", "spectrogram", "comments", "track"},
	"songwriting": {"min_duration_s", "target_min_duration_s", "target_max_duration_s", "guidance"},
}

func sectionJSON(name string, m map[string]any) ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	keys := slices.Clone(order[name])
	keys = slices.DeleteFunc(keys, func(k string) bool { _, ok := m[k]; return !ok })
	var rest []string
	for k := range m {
		if !slices.Contains(keys, k) {
			rest = append(rest, k)
		}
	}
	slices.Sort(rest)
	return writeObject(append(keys, rest...), func(k string) any { return m[k] })
}

// MarshalJSON writes the bundle with the keys in the order C35 lists them.
func (b Bundle) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	put := func(k string, raw []byte, first bool) {
		if !first {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(raw)
	}
	fb, _ := json.Marshal(b.Format)
	put("format", fb, true)
	for _, s := range []struct {
		name string
		m    map[string]any
	}{
		{"track", b.Track}, {"song", b.Song}, {"muse", b.Muse}, {"sounds", b.Sounds}, {"curation", b.Curation}, {"recipe", b.Recipe}, {"provenance", b.Provenance}, {"score", b.Score}, {"lineage", b.Lineage},
		{"judgments", b.Judgments}, {"perception", b.Perception}, {"listening", b.Listening}, {"_links", b.Links},
	} {
		if s.name == "curation" && s.m == nil {
			continue // absent, not null, so bundles without L2 data are byte-identical to before
		}
		raw, err := sectionJSON(s.name, s.m)
		if err != nil {
			return nil, err
		}
		put(s.name, raw, false)
	}
	if b.Songwriting != nil {
		raw, err := sectionJSON("songwriting", b.Songwriting)
		if err != nil {
			return nil, err
		}
		put("songwriting", raw, false)
	}
	na := b.NextActions
	if na == nil {
		na = []string{}
	}
	nb, _ := json.Marshal(na)
	put("next_actions", nb, false)
	bt := b.Budget
	if bt.Truncated == nil {
		bt.Truncated = []string{}
	}
	bb, err := writeObject([]string{"chars", "limit", "truncated"}, func(k string) any {
		switch k {
		case "chars":
			return bt.Chars
		case "limit":
			return bt.Limit
		}
		return bt.Truncated
	})
	if err != nil {
		return nil, err
	}
	put("_budget", bb, false)
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Measure is the UTF-16 length of b as Huma's JSON encoder writes it (HTML
// escaping on: <, > and & count as six characters), trailing newline excluded.
func Measure(b *Bundle) int {
	raw, err := json.Marshal(b)
	if err != nil {
		return 0
	}
	n := 0
	for len(raw) > 0 {
		r, size := utf8.DecodeRune(raw)
		raw = raw[size:]
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// settle sets _budget.chars to its fixed point (the digits of chars are part of
// what chars counts) and returns it.
func settle(b *Bundle) int {
	for range 4 {
		n := Measure(b)
		if n == b.Budget.Chars {
			return n
		}
		b.Budget.Chars = n
	}
	b.Budget.Chars = Measure(b)
	return b.Budget.Chars
}

// ---- trimming ----

func has(include []string, s string) bool { return slices.Contains(include, s) }

// shrink cuts the list under m[key] to n items; it reports whether it changed anything.
func shrink(m map[string]any, key string, n int) bool {
	switch l := m[key].(type) {
	case []any:
		if len(l) > n {
			m[key] = l[:n]
			return true
		}
	case []*omap:
		if len(l) > n {
			m[key] = l[:n]
			return true
		}
	case []map[string]any:
		if len(l) > n {
			m[key] = l[:n]
			return true
		}
	}
	return false
}

func request(b *Bundle) (set func(string, any) bool, get func(string) any) {
	switch r := b.Recipe["request"].(type) {
	case *omap:
		return func(k string, v any) bool { _, ok := r.vals[k]; r.Set(k, v); return ok }, r.Get
	case map[string]any:
		return func(k string, v any) bool { _, ok := r[k]; r[k] = v; return ok }, func(k string) any { return r[k] }
	}
	return func(string, any) bool { return false }, func(string) any { return nil }
}

// Fit trims b to limit characters and returns its measured size and the trimmed
// parts. Order: curation crates and records (3, 0), song siblings (12, 5, 0), muse brief (200), comments (20, 5, 0), children (10, 0), lyrics, style (500
// characters); the parts named by include are protected, except that an ABC text
// longer than the limit is cut as a last resort. _budget is set to its fixed point.
func Fit(b *Bundle, limit int, include []string) (chars int, truncated []string) {
	b.Budget.Limit = limit
	b.Budget.Truncated = []string{}
	note := func(name string) {
		if !slices.Contains(b.Budget.Truncated, name) {
			b.Budget.Truncated = append(b.Budget.Truncated, name)
		}
	}
	chars = settle(b)
	try := func(name string, apply func() bool) {
		if chars > limit && apply() {
			note(name)
			chars = settle(b)
		}
	}
	if b.Curation != nil { // C130.4: crates, then records (6 -> 3 -> 0), before the siblings of the song
		for _, n := range []int{3, 0} {
			try("curation.crates", func() bool { return shrink(b.Curation, "crates", n) })
		}
		for _, n := range []int{3, 0} {
			try("curation.records", func() bool { return shrink(b.Curation, "records", n) })
		}
	}
	if b.Song != nil {
		for _, n := range []int{5, 0} {
			try("song.siblings", func() bool { return shrink(b.Song, "siblings", n) })
		}
	}
	if b.Muse != nil {
		try("muse.brief", func() bool {
			s, ok := b.Muse["brief"].(string)
			if !ok || utf8.RuneCountInString(s) <= briefKeep {
				return false
			}
			b.Muse["brief"] = string([]rune(s)[:briefKeep])
			return true
		})
	}
	if !has(include, "comments") && b.Judgments != nil {
		for _, n := range []int{defaultComments, 5, 0} {
			try("comments", func() bool { return shrink(b.Judgments, "comments", n) })
		}
	}
	if b.Lineage != nil {
		for _, n := range []int{10, 0} {
			try("children", func() bool { return shrink(b.Lineage, "children", n) })
		}
	}
	if !has(include, "lyrics") {
		set, get := request(b)
		try("lyrics", func() bool { return get("lyrics") != nil && set("lyrics", nil) })
	}
	set, get := request(b)
	try("style", func() bool {
		s, ok := get("style").(string)
		if !ok || utf8.RuneCountInString(s) <= styleKeep {
			return false
		}
		return set("style", string([]rune(s)[:styleKeep]))
	})
	if abc, ok := b.Score["abc"].(string); ok && chars > limit {
		runes := []rune(abc)
		b.Score["abc_truncated"] = true
		note("score.abc")       // before measuring: the name is part of the size
		lo, hi := 0, len(runes) // largest prefix that fits (0 when none does)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			b.Score["abc"] = string(runes[:mid])
			if settle(b) <= limit {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		b.Score["abc"] = string(runes[:lo])
		chars = settle(b)
	}
	// Last resorts for protected parts: the largest comment prefix that fits, then the
	// largest lyrics prefix that fits (cut at a rune boundary).
	if chars > limit && has(include, "comments") && b.Judgments != nil {
		full := b.Judgments["comments"]
		if n := listLen(full); n > 0 {
			note("comments")
			lo, hi := 0, n-1
			for lo < hi {
				mid := (lo + hi + 1) / 2
				b.Judgments["comments"] = full
				shrink(b.Judgments, "comments", mid)
				if settle(b) <= limit {
					lo = mid
				} else {
					hi = mid - 1
				}
			}
			b.Judgments["comments"] = full
			shrink(b.Judgments, "comments", lo)
			chars = settle(b)
		}
	}
	if chars > limit && has(include, "lyrics") {
		if l, ok := get("lyrics").(string); ok && l != "" {
			note("lyrics")
			runes := []rune(l)
			lo, hi := 0, len(runes)
			for lo < hi {
				mid := (lo + hi + 1) / 2
				set("lyrics", string(runes[:mid]))
				if settle(b) <= limit {
					lo = mid
				} else {
					hi = mid - 1
				}
			}
			set("lyrics", string(runes[:lo]))
			chars = settle(b)
		}
	}
	return chars, b.Budget.Truncated
}

func listLen(v any) int {
	switch l := v.(type) {
	case []any:
		return len(l)
	case []*omap:
		return len(l)
	case []map[string]any:
		return len(l)
	}
	return 0
}

// ---- builder ----

// Builder assembles bundles.
type Builder struct {
	lib       *library.Service
	j         *judgments.Service
	l         *listening.Service
	signer    *serve.Signer
	publicURL string
	now       func() time.Time
	abc       func(ctx context.Context, trackID string) (string, error)
	per       PerceptionSource
	str       StrengthSource
	gen       func(ctx context.Context) bool
	lib1      LibraryReader  // L1 (C98): nil => the song, muse and sounds sections are null
	curation  CurationReader // L2 (C130.4): nil => no curation section
	sw        TargetSource
}

// TargetSource supplies the songwriting duration targets (*songwriting.Service implements it).
type TargetSource interface {
	Get(ctx context.Context) (songwriting.Targets, error)
}

// UseSongwriting adds the duration targets to every bundle as the "songwriting" section.
func (b *Builder) UseSongwriting(t TargetSource) { b.sw = t }

func (b *Builder) songwritingSection(ctx context.Context) (map[string]any, error) {
	if b.sw == nil {
		return nil, nil
	}
	t, err := b.sw.Get(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"min_duration_s": t.MinDurationS, "target_min_duration_s": t.TargetMinDurationS, "target_max_duration_s": t.TargetMaxDurationS,
		"guidance": fmt.Sprintf("songs should be longer than %d s and aim for %d-%d s: write enough sections for it (lint warns duration_under_target / duration_outside_target; get_songwriting_settings reads these)",
			t.MinDurationS, t.TargetMinDurationS, t.TargetMaxDurationS),
	}, nil
}

// PerceptionSource builds the perception block of a track; *ears.Service implements it (C51.8).
type PerceptionSource interface {
	Perception(ctx context.Context, trackID string) (ears.Perception, error)
}

// StrengthSource is the global ladder fit restricted to some tracks; *duels.Service implements it.
type StrengthSource interface {
	Strengths(ctx context.Context, ids []string) (map[string]ladder.Strength, float64, error)
}

// spectrogramSource is optionally implemented by the PerceptionSource (*ears.Service does): the
// sha256 of the track's spectrogram rendition, "" when it has none.
type spectrogramSource interface {
	SpectrogramSHA256(ctx context.Context, trackID string) (string, error)
}

// UseM2b wires the M2b sources: perception (tier, flags, blind spots, loudness and sections) from Ears
// and the ladder strengths from duels. Either may be nil: without a PerceptionSource the perception
// section is null, without a StrengthSource every ladder is null.
func (b *Builder) UseM2b(p PerceptionSource, s StrengthSource) { b.per, b.str = p, s }

// New builds a Builder.
func New(lib *library.Service, j *judgments.Service, l *listening.Service, signer *serve.Signer, publicURL string, now func() time.Time) *Builder {
	if now == nil {
		now = time.Now
	}
	return &Builder{lib: lib, j: j, l: l, signer: signer, publicURL: strings.TrimRight(publicURL, "/"), now: now}
}

// maxNextActions caps next_actions (C81.1).
const maxNextActions = 5

// generationHint is the next action that points an agent at the variant tools (free to estimate).
const generationHint = "try a new-seed variant: estimate_variant mode=new_seed n_seeds=3 (the estimate is free; spending needs create_variant)"

// UseGeneration sets the test for "generation is enabled" (the provider gate). Without one, generation counts
// as enabled when the database knows a provider (the fake or the serverless backend is configured).
func (b *Builder) UseGeneration(enabled func(ctx context.Context) bool) { b.gen = enabled }

func (b *Builder) generationEnabled(ctx context.Context) bool {
	if b.gen != nil {
		return b.gen(ctx)
	}
	ok, err := b.lib.HasProvider(ctx)
	return err == nil && ok
}

// UseScoreABC sets the reader of a track's score.abc text, used for include=score.
// Without one, include=score answers score.abc as null.
func (b *Builder) UseScoreABC(f func(ctx context.Context, trackID string) (string, error)) { b.abc = f }

var includes = []string{"score", "lyrics", "comments"}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func invalid(field, format string, a ...any) error {
	return &library.ValidationError{Field: field, Msg: fmt.Sprintf(format, a...)}
}

// by names who wrote a comment as an agent reads it: the operator's devices count as the human.
func by(kind string) string {
	if kind == string(auth.KindDevice) {
		return string(auth.KindHuman)
	}
	return kind
}

func commentOmap(c judgments.Comment) *omap {
	var label, conf any
	if c.Section != nil {
		label, conf = c.Section.Label, c.Section.Confidence
	}
	return newOmap("id", c.ID, "at_second", c.AtSecond, "section", label, "section_confidence", conf,
		"kind", c.Kind, "state", c.State, "by", by(c.Author.Kind), "body", c.Body)
}

// collect lists up to want comments matching f, skipping ids in skip.
func (b *Builder) collect(ctx context.Context, trackID string, f judgments.CommentFilter, want int, skip map[string]bool) ([]judgments.Comment, int, error) {
	var out []judgments.Comment
	total := 0
	f.Limit = 200
	for want > 0 {
		page, err := b.j.Comments(ctx, trackID, f)
		if err != nil {
			return nil, 0, err
		}
		total = page.Total
		for _, c := range page.Items {
			if skip[c.ID] {
				continue
			}
			out = append(out, c)
			if len(out) == want {
				return out, total, nil
			}
		}
		if page.NextCursor == nil || len(page.Items) == 0 {
			break
		}
		f.Cursor = *page.NextCursor
	}
	return out, total, nil
}

func (b *Builder) comments(ctx context.Context, trackID string, include []string) (items []any, openFix int, err error) {
	want := defaultComments
	if has(include, "comments") {
		want = includeComments
	}
	fixes, openFix, err := b.collect(ctx, trackID, judgments.CommentFilter{State: []string{"open"}, Kind: []string{"fix"}}, want, nil)
	if err != nil {
		return nil, 0, err
	}
	// the open-fix total counts them all, not just the listed ones
	if len(fixes) == 0 {
		openFix = 0
	}
	skip := map[string]bool{}
	items = []any{}
	for _, c := range fixes {
		skip[c.ID] = true
		items = append(items, commentOmap(c))
	}
	if rest := want - len(fixes); rest > 0 {
		more, _, err := b.collect(ctx, trackID, judgments.CommentFilter{}, rest, skip)
		if err != nil {
			return nil, 0, err
		}
		for _, c := range more {
			items = append(items, commentOmap(c))
		}
	}
	return items, openFix, nil
}

// family returns the root family of a track keyed by id.
func (b *Builder) family(ctx context.Context, root string) (map[string]library.TrackSummary, error) {
	out := map[string]library.TrackSummary{}
	p := library.SearchParams{RootTrackID: root, Limit: 200, Sort: "recorded_asc"}
	for range 10 {
		page, err := b.lib.SearchTracks(ctx, p)
		if err != nil {
			return nil, err
		}
		for _, s := range page.Items {
			out[s.ID] = s
		}
		if page.NextCursor == nil {
			break
		}
		p.Cursor = *page.NextCursor
	}
	return out, nil
}

// Build assembles the bundle of one track; include is a subset of score|lyrics|comments.
func (b *Builder) Build(ctx context.Context, p auth.Principal, trackID string, include []string) (Bundle, error) {
	for _, in := range include {
		if !slices.Contains(includes, in) {
			return Bundle{}, invalid("include", "unknown part %q (want score, lyrics or comments)", in)
		}
	}
	d, err := b.lib.Track(ctx, trackID)
	if err != nil {
		return Bundle{}, err
	}
	out := Bundle{Format: "mm.context/1"}

	// track
	var stars, critic, verdict any
	if h := d.Rating.Human; h != nil && h.Stars != nil {
		stars = *h.Stars
	}
	if c := d.Rating.Critic; c != nil {
		if c.Stars != nil {
			critic = *c.Stars
		}
		if c.Verdict != nil {
			verdict = *c.Verdict
		}
	}
	tags := make([]string, len(d.Tags))
	for i, t := range d.Tags {
		tags[i] = t.Name
	}
	out.Track = map[string]any{"id": d.ID, "title": d.Title, "status": d.Status, "stars": stars, "critic_stars": critic, "critic_verdict": verdict,
		"tags": tags, "duration_s": d.DurationS, "recipe_known": d.RecipeKnown, "lossy": d.Lossy, "recorded_at": d.RecordedAt, "url": b.publicURL + "/tracks/" + d.ID}

	// song, muse, sounds (C98): three nulls without a Song or without the library services
	if b.lib1 != nil {
		ls, err := b.lib1.ForTrack(ctx, d.ID)
		if err != nil {
			return Bundle{}, err
		}
		out.Song, out.Muse, out.Sounds = ls.Song, ls.Muse, ls.Sounds
	}
	// curation (C130.4): the Records and Crates the take is in; absent without a reader or when there are none
	if out.Curation, err = b.curationSection(ctx, d.ID); err != nil {
		return Bundle{}, err
	}

	// recipe and provenance
	if d.RecipeKnown {
		out.Recipe = map[string]any{"engine": d.Recipe.Engine, "known": true, "hash": d.Recipe.Hash, "request": newOmap(
			"id", d.Recipe.RequestID, "style", d.Recipe.Style, "lyrics", d.Recipe.Lyrics, "cot", d.Recipe.Cot, "seed", d.Recipe.Seed,
			"abc_present", d.Score != nil, "cfg_scale", nil)}
	} else {
		// a reference track (D-M2c-1): the stub carries no style, lyrics or seed worth showing
		out.Recipe = map[string]any{"engine": d.Recipe.Engine, "known": false, "hash": d.Recipe.Hash, "request": nil, "note": recipeUnknownNote}
	}
	var tabc, tsem any
	if d.Truncated != nil {
		if d.Truncated.ABC != nil {
			tabc = *d.Truncated.ABC
		}
		if d.Truncated.Semantic != nil {
			tsem = *d.Truncated.Semantic
		}
	}
	var cost, env, jobID any
	if g := d.Generation; g != nil {
		jobID = g.JobID
		if g.Cost != nil {
			cost = newOmap("micro_usd", g.Cost.DisplayMicroUSD, "usd", g.Cost.DisplayUSD, "basis", g.Cost.Basis)
		}
		if g.Env != nil {
			env = g.Env
		}
	}
	out.Provenance = map[string]any{"origin": d.Origin, "import_rule": d.Import.Rule, "truncated": newOmap("abc", tabc, "semantic", tsem),
		"cost": cost, "env": env, "job_id": jobID}

	// score
	if sc := d.Score; sc != nil {
		out.Score = map[string]any{"source": d.Origin, "bars": sc.Bars, "meter": sc.Meter, "q": sc.Q, "seconds_per_bar": sc.SecondsPerBar, "aligned": sc.Aligned}
		if has(include, "score") {
			var abc any
			if b.abc != nil {
				if text, err := b.abc(ctx, d.ID); err == nil {
					abc = text
				}
			}
			out.Score["abc"] = abc
		}
	}

	// lineage
	pathItems := []any{newOmap("id", d.ID, "title", d.Title)}
	children := []any{}
	var childIDs []string
	if len(d.Lineage.Siblings) > 0 || d.Lineage.ParentTrackID != nil {
		fam, err := b.family(ctx, d.Lineage.RootTrackID)
		if err != nil {
			return Bundle{}, err
		}
		for _, id := range slices.Sorted(mapKeys(fam)) {
			s := fam[id]
			if s.ParentTrackID != nil && *s.ParentTrackID == d.ID {
				var st any
				if s.Stars != nil {
					st = *s.Stars
				}
				children = append(children, newOmap("id", s.ID, "title", s.Title, "stars", st, "ladder", nil))
				childIDs = append(childIDs, s.ID)
			}
		}
		slices.SortStableFunc(children, func(x, y any) int {
			return strings.Compare(fam[x.(*omap).Get("id").(string)].RecordedAt, fam[y.(*omap).Get("id").(string)].RecordedAt)
		})
		path := []any{}
		seen := map[string]bool{}
		for cur := d.Lineage.ParentTrackID; cur != nil && !seen[*cur]; {
			seen[*cur] = true
			s, ok := fam[*cur]
			if !ok {
				break
			}
			path = append([]any{newOmap("id", s.ID, "title", s.Title)}, path...)
			cur = s.ParentTrackID
		}
		pathItems = append(path, pathItems...)
	}
	// ladder strengths (global fit, human-source verdicts only): one fit lookup for the track and its children
	var own any
	if b.str != nil {
		ids := append([]string{d.ID}, childIDs...)
		strengths, _, err := b.str.Strengths(ctx, ids)
		switch {
		case errors.Is(err, errors.ErrUnsupported):
			strengths = nil
		case err != nil:
			return Bundle{}, err
		}
		if s, ok := strengths[d.ID]; ok && s.NDuels > 0 {
			own = newOmap("u", s.U, "sd", s.SD, "n_duels", s.NDuels)
		}
		for _, c := range children {
			om := c.(*omap)
			if s, ok := strengths[om.Get("id").(string)]; ok && s.NDuels > 0 {
				om.Set("ladder", newOmap("u", s.U, "sd", s.SD))
			}
		}
	}
	out.Lineage = map[string]any{"path": pathItems, "operator": d.Lineage.Operator, "inferred": d.Lineage.Inferred, "rule": d.Lineage.Rule,
		"rationale": nil, "children": children, "siblings": len(d.Lineage.Siblings)}

	// judgments
	comments, openFix := []any{}, 0
	if b.j != nil {
		if comments, openFix, err = b.comments(ctx, d.ID, include); err != nil {
			return Bundle{}, err
		}
	}
	out.Judgments = map[string]any{"comments": comments, "comments_total": d.Comments, "open_fix": openFix, "ladder": own}

	// perception (Ears computes it; the sections keep M2a's start/end keys)
	if b.per != nil {
		p, err := b.per.Perception(ctx, d.ID)
		if err != nil {
			return Bundle{}, err
		}
		secs := make([]any, len(p.Sections))
		for i, s := range p.Sections {
			secs[i] = newOmap("label", s.Label, "start", s.Start, "end", s.End, "source", s.Source, "confidence", s.Confidence)
		}
		out.Perception = map[string]any{"tier": p.Tier, "lufs_i": p.LUFSI, "true_peak_db": p.TruePeakDB, "lra_lu": p.LRALU,
			"bandwidth_hz": p.BandwidthHz, "sections": secs, "timed": p.Timed, "flags": nonNil(p.Flags), "blind_spots": nonNil(p.BlindSpots),
			"tempo_bpm": p.TempoBPM, "tempo_confidence": p.TempoConfidence}
	}

	// listening
	st := d.Listening
	if b.l != nil {
		if st, err = b.l.Stats(ctx, d.ID); err != nil {
			return Bundle{}, err
		}
	}
	out.Listening = map[string]any{"plays": st.Plays, "completion_rate": st.CompletionRate, "last_played_at": st.LastPlayedAt}

	// links
	links := map[string]any{"stream": nil, "spectrogram": nil, "comments": "/api/tracks/" + d.ID + "/comments", "track": "/api/tracks/" + d.ID}
	if b.signer != nil {
		m, err := b.lib.Media(ctx, d.ID)
		if err != nil && !errors.Is(err, library.ErrMediaUnavailable) {
			return Bundle{}, err
		}
		ttl, bucket := serve.TTLFor(p)
		var mp3 any
		exp := time.Time{}
		if err == nil {
			l := b.signer.Sign(m.MP3.SHA256, "mp3", ttl, bucket)
			mp3, exp = b.publicURL+l.URL, l.Expires
		}
		if m.Flac == nil {
			// a lossy track has no lossless file: no flac key at all (D-M2c-7)
			links["stream"] = newOmap("mp3", mp3, "expires_at", rfc(exp))
		} else {
			fl := b.signer.Sign(m.Flac.SHA256, "flac", ttl, bucket)
			links["stream"] = newOmap("mp3", mp3, "flac", b.publicURL+fl.URL, "expires_at", rfc(fl.Expires))
		}
		if sp, ok := b.per.(spectrogramSource); ok {
			sha, err := sp.SpectrogramSHA256(ctx, d.ID)
			if err != nil {
				return Bundle{}, err
			}
			if sha != "" {
				links["spectrogram"] = b.publicURL + b.signer.Sign(sha, "jpg", ttl, bucket).URL
			}
		}
	}
	out.Links = links
	if out.Songwriting, err = b.songwritingSection(ctx); err != nil {
		return Bundle{}, err
	}

	// next actions
	na := []string{}
	if stars == nil {
		na = append(na, "ask the human to rate this take (rate_track)")
	}
	if openFix > 0 {
		na = append(na, fmt.Sprintf("address %d open fix comments (call_operation list_comments state=open kind=fix)", openFix))
	}
	if !d.Timed {
		na = append(na, "section times need a score: refer to sections by lyrics, not by time")
	}
	if d.Truncated != nil && (d.Truncated.ABC != nil && *d.Truncated.ABC || d.Truncated.Semantic != nil && *d.Truncated.Semantic) {
		na = append(na, "the take is truncated (abc/semantic): prefer a shorter arrangement")
	}
	// The generation hint has the lowest priority: at most maxNextActions entries, the critique last, so
	// the hint is the entry that is dropped when every conditional entry applies (C81.1).
	if d.RecipeKnown && p.Has(auth.ScopeGenerate) && b.generationEnabled(ctx) && len(na)+2 <= maxNextActions {
		na = append(na, generationHint)
	}
	na = append(na, "record your critique as a comment (add_comment) — you cannot hear: see perception.blind_spots")
	out.NextActions = na

	limit := DefaultLimit
	if len(include) > 0 {
		limit = IncludeLimit
	}
	Fit(&out, limit, include)
	return out, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func mapKeys[V any](m map[string]V) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}
