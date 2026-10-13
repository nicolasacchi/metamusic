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
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
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
)

// Bundle is the mm.context/1 document. Sub-objects are open JSON objects whose
// shape C35 documents; unknown values are null, never invented. Marshalling
// writes the keys in the order C35 lists them.
type Bundle struct {
	Format      string         `json:"format" example:"mm.context/1"`
	Track       map[string]any `json:"track"`
	Recipe      map[string]any `json:"recipe"`
	Provenance  map[string]any `json:"provenance"`
	Score       map[string]any `json:"score" nullable:"true"`
	Lineage     map[string]any `json:"lineage"`
	Judgments   map[string]any `json:"judgments"`
	Perception  map[string]any `json:"perception"`
	Listening   map[string]any `json:"listening"`
	Links       map[string]any `json:"_links"`
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

// Key order of the sections (C35); keys not listed follow, sorted.
var order = map[string][]string{
	"track":      {"id", "title", "status", "stars", "critic_stars", "critic_verdict", "tags", "duration_s", "recorded_at", "url"},
	"recipe":     {"engine", "hash", "request"},
	"provenance": {"origin", "import_rule", "truncated", "cost_usd", "env"},
	"score":      {"source", "bars", "meter", "q", "seconds_per_bar", "aligned", "abc", "abc_truncated"},
	"lineage":    {"path", "operator", "inferred", "rule", "rationale", "children", "siblings"},
	"judgments":  {"comments", "comments_total", "open_fix", "ladder"},
	"perception": {"tier", "lufs_i", "true_peak_db", "lra_lu", "bandwidth_hz", "sections", "timed", "flags", "blind_spots"},
	"listening":  {"plays", "completion_rate", "last_played_at"},
	"_links":     {"stream", "comments", "track"},
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
		{"track", b.Track}, {"recipe", b.Recipe}, {"provenance", b.Provenance}, {"score", b.Score}, {"lineage", b.Lineage},
		{"judgments", b.Judgments}, {"perception", b.Perception}, {"listening", b.Listening}, {"_links", b.Links},
	} {
		raw, err := sectionJSON(s.name, s.m)
		if err != nil {
			return nil, err
		}
		put(s.name, raw, false)
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
// parts. Order: comments (20, 5, 0), children (10, 0), lyrics, style (500
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
}

// New builds a Builder.
func New(lib *library.Service, j *judgments.Service, l *listening.Service, signer *serve.Signer, publicURL string, now func() time.Time) *Builder {
	if now == nil {
		now = time.Now
	}
	return &Builder{lib: lib, j: j, l: l, signer: signer, publicURL: strings.TrimRight(publicURL, "/"), now: now}
}

// UseScoreABC sets the reader of a track's score.abc text, used for include=score.
// Without one, include=score answers score.abc as null.
func (b *Builder) UseScoreABC(f func(ctx context.Context, trackID string) (string, error)) { b.abc = f }

var includes = []string{"score", "lyrics", "comments"}

var blindSpots = []string{"timbre", "emotional delivery", "mix balance beyond loudness", "hook catchiness", "diction and intelligibility"}

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
		"tags": tags, "duration_s": d.DurationS, "recorded_at": d.RecordedAt, "url": b.publicURL + "/tracks/" + d.ID}

	// recipe and provenance
	out.Recipe = map[string]any{"engine": d.Recipe.Engine, "hash": d.Recipe.Hash, "request": newOmap(
		"id", d.Recipe.RequestID, "style", d.Recipe.Style, "lyrics", d.Recipe.Lyrics, "cot", d.Recipe.Cot, "seed", d.Recipe.Seed,
		"abc_present", d.Score != nil, "cfg_scale", nil)}
	var tabc, tsem any
	if d.Truncated != nil {
		if d.Truncated.ABC != nil {
			tabc = *d.Truncated.ABC
		}
		if d.Truncated.Semantic != nil {
			tsem = *d.Truncated.Semantic
		}
	}
	out.Provenance = map[string]any{"origin": d.Origin, "import_rule": d.Import.Rule, "truncated": newOmap("abc", tabc, "semantic", tsem),
		"cost_usd": nil, "env": nil}

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
	out.Lineage = map[string]any{"path": pathItems, "operator": d.Lineage.Operator, "inferred": d.Lineage.Inferred, "rule": d.Lineage.Rule,
		"rationale": nil, "children": children, "siblings": len(d.Lineage.Siblings)}

	// judgments
	comments, openFix := []any{}, 0
	if b.j != nil {
		if comments, openFix, err = b.comments(ctx, d.ID, include); err != nil {
			return Bundle{}, err
		}
	}
	out.Judgments = map[string]any{"comments": comments, "comments_total": d.Comments, "open_fix": openFix, "ladder": nil}

	// perception
	per := map[string]any{"tier": "0", "lufs_i": nil, "true_peak_db": nil, "lra_lu": nil, "bandwidth_hz": nil,
		"timed": d.Timed, "flags": []string{}, "blind_spots": blindSpots}
	if l := d.Analysis.Loudness; l != nil {
		per["lufs_i"], per["true_peak_db"], per["lra_lu"] = l.IntegratedLUFS, l.TruePeakDBTP, l.LRA
	}
	if s := d.Analysis.Spectrum; s != nil && s.BandwidthHz != nil {
		per["bandwidth_hz"] = *s.BandwidthHz
	}
	secs := make([]any, len(d.Sections))
	for i, s := range d.Sections {
		secs[i] = newOmap("label", s.Label, "start", s.StartS, "end", s.EndS, "source", s.Source, "confidence", s.Confidence)
	}
	per["sections"] = secs
	out.Perception = per

	// listening
	st := d.Listening
	if b.l != nil {
		if st, err = b.l.Stats(ctx, d.ID); err != nil {
			return Bundle{}, err
		}
	}
	out.Listening = map[string]any{"plays": st.Plays, "completion_rate": st.CompletionRate, "last_played_at": st.LastPlayedAt}

	// links
	links := map[string]any{"stream": nil, "comments": "/api/tracks/" + d.ID + "/comments", "track": "/api/tracks/" + d.ID}
	if b.signer != nil {
		m, err := b.lib.Media(ctx, d.ID)
		if err != nil && !errors.Is(err, library.ErrMediaUnavailable) {
			return Bundle{}, err
		}
		ttl, bucket := serve.TTLFor(p)
		fl := b.signer.Sign(m.Flac.SHA256, "flac", ttl, bucket)
		var mp3 any
		if err == nil {
			mp3 = b.publicURL + b.signer.Sign(m.MP3.SHA256, "mp3", ttl, bucket).URL
		}
		links["stream"] = newOmap("mp3", mp3, "flac", b.publicURL+fl.URL, "expires_at", rfc(fl.Expires))
	}
	out.Links = links

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
	na = append(na, "record your critique as a comment (add_comment) — you cannot hear: see perception.blind_spots")
	out.NextActions = na

	limit := DefaultLimit
	if len(include) > 0 {
		limit = IncludeLimit
	}
	Fit(&out, limit, include)
	return out, nil
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
