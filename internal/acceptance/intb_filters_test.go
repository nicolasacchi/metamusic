package acceptance_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/view"
)

// filterDecisions: every query parameter of every Listener operation is decided here, by name. "banned" parameters are the
// C308.3b list (422 FILTER_NOT_ALLOWED); every other one is "open" with the reason it does not read hidden data. The reflection
// below reads the parameters from the registry, so a new parameter fails TestIntBFilterSweep until someone decides it, and the
// two oracle tests then prove the decision: no open parameter answers differently for a hidden value than for a made-up one,
// and no open parameter's answer moves when only hidden data changes.
var filterDecisions = map[string]string{
	// shown on the Listener's own rows
	"is_pick": "shown as is_pick on every take", "has_score": "shown as has_score", "recipe_known": "shown as recipe_known", "truncated": "shown as truncated",
	"lossy": "shown as lossy", "has_pick": "the Song's pick is shown", "pick_decided": "a Pick of a Song is shown with its source state", "state": "the Song state is shown",
	"include_archived": "archived is shown", "include_hidden": "hidden is shown", "status": "the take status is shown (discarded is removed)",
	"min_stars": "the viewer's own rating", "unrated": "the viewer's own rating", "tag": "tags are shown", "style": "style terms are shown",
	"sound": "sounds are shown", "tempo_mismatch": "declared and measured BPM are both shown", "min_duration_s": "duration is shown",
	"max_duration_s": "duration is shown", "recorded_from": "recorded_at is shown", "recorded_to": "recorded_at is shown",
	"in_record": "membership in a published Record is shown; a draft Record is not (oracle test)",
	// scoping, paging, shape
	"limit": "paging", "cursor": "an opaque paging token", "sort": "ordering of shown fields", "q": "FTS column filter over title and lyrics (ftsListener)",
	"dim": "mark, not filter", "k": "neighbour count", "x": "Atlas axis", "y": "Atlas axis", "include": "response blocks (the hidden ones are removed by the shaper)",
	"counts": "counts of visible members", "kind": "crate or record change kind", "subject": "crate subject", "facet": "taxonomy facet", "value": "taxonomy value",
	"unmapped": "taxonomy", "level": "peaks level", "inline": "spectrogram disposition", "by": "id resolution of a visible id", "include_events": "events are removed by the shaper",
	"take_limit": "paging", "changes_limit": "paging", "order": "ordering", "strategy": "sequence strategy",
	"from_s": "clip window", "to_s": "clip window", "match_lufs": "clip loudness",
	// ids of things a Listener may open: a hidden id must answer as an unknown one (oracle test)
	"crate_id": "a smart Crate with a hidden filter is refused (crates.ExpandSongs)", "song_id": "id of a visible Song; a hidden one answers as unknown",
	"near_song_id": "id of a visible Song", "first_song_id": "id of a visible Song", "last_song_id": "id of a visible Song", "track_id": "id of a visible take",
	"record_id": "id of a published Record; a draft answers as unknown", "a": "visible take id", "b": "visible take id",
}

// filterDecision answers "banned" or "open" for a parameter, or "" when nobody decided it.
func filterDecision(name string) string {
	if _, ok := view.BannedFilterKey(name); ok {
		return "banned"
	}
	if filterDecisions[name] != "" {
		return "open"
	}
	return ""
}

// filterValues are the realistic values the sweep sends for an open parameter.
func (w *intbWorld) filterValues(tb testing.TB, p intbParam) []string {
	tb.Helper()
	if len(p.Enum) > 0 {
		return p.Enum
	}
	switch p.Type {
	case "boolean":
		return []string{"true", "false"}
	case "integer":
		return []string{"1", "3"}
	case "number":
		return []string{"30"}
	}
	vis := w.Visible
	switch p.Name {
	case "crate_id":
		return w.Crates
	case "song_id", "near_song_id", "first_song_id", "last_song_id":
		return []string{w.Songs[0], w.Songs[1], w.GoneSong}
	case "track_id", "a", "b":
		return []string{vis[0], vis[1], w.Discarded[0]}
	case "record_id":
		return []string{w.Records[0], w.Draft}
	case "q":
		return []string{"synthetic", "twin", "dance"}
	case "tag", "style", "sound":
		return []string{"dance", "reggae", "mood:chill"}
	case "recorded_from", "recorded_to":
		return []string{"2026-10-01T00:00:00Z"}
	case "x", "y":
		return []string{"tempo", "energy"}
	case "strategy":
		return []string{"arc", "shuffle"}
	case "kind":
		return []string{"published", "created"}
	case "facet", "value":
		return []string{"genre_family", "dance"}
	case "cursor":
		return nil
	}
	tb.Fatalf("filterValues: no realistic value for the string parameter %q", p.Name)
	return nil
}

// listenerQueryOps are the Listener-reachable operations that take query parameters.
func listenerQueryOps(ops []intbOp) []intbOp {
	var out []intbOp
	for _, o := range allowedFor(ops, intbListener) {
		if o.Method == "GET" && len(o.Query) > 0 {
			out = append(out, o)
		}
	}
	return out
}

// listenerGetPath builds a GET of op with one query string (path ids filled in with a visible one where needed).
func (w *intbWorld) listenerGetPath(op intbOp, q string) string {
	p := pathParam.ReplaceAllString(op.Path, w.pathID(op))
	if q != "" {
		p += "?" + q
	}
	return p
}

func (w *intbWorld) pathID(op intbOp) string {
	switch {
	case strings.HasPrefix(op.Path, "/api/tracks/"):
		return lt.TrackLuna
	case strings.HasPrefix(op.Path, "/api/songs/"):
		return w.Songs[0]
	case strings.HasPrefix(op.Path, "/api/records/"):
		return w.Records[0]
	case strings.HasPrefix(op.Path, "/api/playlists/"):
		return w.Playlists[0]
	}
	return ""
}

// signature reduces an answer to what an oracle would read: the status, every id and the counts, in order.
func signature(status int, body []byte) string {
	var v any
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	_ = dec.Decode(&v)
	var out []string
	var walk func(k string, n any)
	walk = func(k string, n any) {
		switch x := n.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for kk := range x {
				keys = append(keys, kk)
			}
			sort.Strings(keys)
			for _, kk := range keys {
				walk(kk, x[kk])
			}
		case []any:
			out = append(out, fmt.Sprintf("[%d", len(x)))
			for _, c := range x {
				walk(k, c)
			}
		case string:
			if k == "id" || strings.HasSuffix(k, "_id") || k == "code" {
				out = append(out, k+"="+x)
			}
		case json.Number:
			if k == "total" || k == "count" || k == "takes" || k == "songs" || strings.HasSuffix(k, "_total") {
				out = append(out, k+"="+x.String())
			}
		}
	}
	walk("", v)
	return fmt.Sprintf("%d %s", status, strings.Join(out, " "))
}

// TestIntBFilterSweep (C308.3b, plan 4): reflection over the registry decides every query parameter of every Listener
// operation. A banned one answers 422 FILTER_NOT_ALLOWED naming it, an open one is on the decision list, and crate_id over a
// smart Crate with a hidden filter is refused without naming the stored key.
func TestIntBFilterSweep(t *testing.T) {
	t.Parallel()
	w := newIntbWorld(t)
	ops := intbOps(t)
	banned := map[string]bool{}
	for _, o := range listenerQueryOps(ops) {
		for _, p := range o.Query {
			switch filterDecision(p.Name) {
			case "":
				t.Errorf("%s has the query parameter %q and nobody decided whether a Listener may use it: add it to view.ListenerBannedFilters (and the handler's filter set) or to filterDecisions with a reason", o.ID, p.Name)
			case "banned":
				banned[p.Name] = true
				val := "x"
				switch {
				case p.Type == "boolean":
					val = "true"
				case p.Name == "color_by":
					val = "muse"
				case len(p.Enum) > 0:
					val = p.Enum[0]
				case p.Type == "integer":
					val = "1"
				}
				resp := w.Carol.Do(t, "GET", w.listenerGetPath(o, p.Name+"="+url.QueryEscape(val)), nil)
				var pr struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
				}
				_ = json.Unmarshal(resp.Body, &pr)
				want, _ := view.BannedFilterKey(p.Name) // no_muse is the Muse filter's other spelling and is named as muse_id
				if resp.Status != 422 || pr.Code != "FILTER_NOT_ALLOWED" || pr.Details["filter"] != want {
					t.Errorf("%s ?%s=%s for a Listener: %d %s, want 422 FILTER_NOT_ALLOWED naming %s", o.ID, p.Name, val, resp.Status, resp.Body, want)
				}
				// the same request is fine for the Owner and the Editor: the refusal is the role's, not the parameter's
				if r := w.Own.Do(t, "GET", w.listenerGetPath(o, p.Name+"="+url.QueryEscape(val)), nil); r.Status == 422 && strings.Contains(string(r.Body), "FILTER_NOT_ALLOWED") {
					t.Errorf("%s ?%s refused to the Owner", o.ID, p.Name)
				}
			}
		}
	}
	for _, name := range view.ListenerBannedFilters { // a banned name that no operation has any more is a stale list entry
		if !banned[name] {
			t.Errorf("view.ListenerBannedFilters names %q but no Listener operation takes it", name)
		}
	}
	// crate_id over a smart Crate with a hidden filter, on every operation that takes it
	for _, o := range listenerQueryOps(ops) {
		if !slices.ContainsFunc(o.Query, func(p intbParam) bool { return p.Name == "crate_id" }) {
			continue
		}
		crate := w.SmartBad
		if o.ID == "search_tracks" { // a take-subject Crate for the take search
			crate = w.SmartBadTake
		}
		resp := w.Carol.Do(t, "GET", w.listenerGetPath(o, "crate_id="+crate), nil)
		if resp.Status != 422 || !strings.Contains(string(resp.Body), "FILTER_NOT_ALLOWED") || strings.Contains(string(resp.Body), "angle") {
			t.Errorf("%s crate_id over a smart Crate with a hidden filter: %d %s", o.ID, resp.Status, resp.Body)
		}
	}
}

// hiddenValues are the values an oracle would try, one per kind of hidden data.
func (w *intbWorld) hiddenValues(tb testing.TB) map[string]string {
	tb.Helper()
	first := func(q string) string {
		s := intbSQLStrings(tb, w.R, q)
		if len(s) == 0 {
			tb.Fatalf("hiddenValues: nothing for %s", q)
		}
		return s[0]
	}
	return map[string]string{
		"discarded take": w.Discarded[0], "all-discarded song": w.GoneSong, "draft record": w.Draft,
		"muse id": first(`SELECT id FROM muses ORDER BY id LIMIT 1`), "session id": first(`SELECT id FROM sessions LIMIT 1`),
		"draft id": first(`SELECT id FROM recipe_drafts LIMIT 1`), "request id": first(`SELECT request_id FROM recipes LIMIT 1`),
		"job id": first(`SELECT id FROM jobs LIMIT 1`), "raw style": first(`SELECT style FROM recipes LIMIT 1`),
		"comment text": canaryNote, "muse text": canaryMuse, "username": "alice", "master sha": first(`SELECT master_sha256 FROM tracks LIMIT 1`),
		"batch id": w.BobBatch, "owner verdict": "owner " + canaryVerdict,
	}
}

func fakeOf(val string) string {
	if len(val) == 36 && strings.Count(val, "-") == 4 {
		return "01900000-0000-7000-8000-00000000dead"
	}
	return strings.Repeat("x", len(val))
}

// TestIntBFilterNotAnOracleByValue (plan 4): for every open string parameter of every Listener operation, a real hidden value
// and a made-up one of the same shape answer identically (status, ids and counts).
func TestIntBFilterNotAnOracleByValue(t *testing.T) {
	t.Parallel()
	w := newIntbWorld(t)
	hidden := w.hiddenValues(t)
	calls := 0
	for _, o := range listenerQueryOps(intbOps(t)) {
		for _, p := range o.Query {
			if filterDecision(p.Name) != "open" || p.Type == "boolean" || p.Type == "integer" || p.Type == "number" || len(p.Enum) > 0 || p.Name == "cursor" {
				continue
			}
			for kind, val := range hidden {
				fake := fakeOf(val)
				a := w.Carol.Do(t, "GET", w.listenerGetPath(o, p.Name+"="+url.QueryEscape(val)), nil)
				b := w.Carol.Do(t, "GET", w.listenerGetPath(o, p.Name+"="+url.QueryEscape(fake)), nil)
				calls += 2
				norm := func(r string) string { return strings.ReplaceAll(strings.ReplaceAll(r, val, "X"), fake, "X") }
				if sa, sb := norm(signature(a.Status, a.Body)), norm(signature(b.Status, b.Body)); sa != sb {
					t.Errorf("%s ?%s: the %s answers differently from a made-up value:\n  real: %.200s\n  fake: %.200s", o.ID, p.Name, kind, sa, sb)
				}
				if normBody(a.Body, val) != normBody(b.Body, fake) && a.Status/100 != 2 {
					t.Errorf("%s ?%s: the %s refusal body differs from a made-up value's: %.200s vs %.200s", o.ID, p.Name, kind, a.Body, b.Body)
				}
			}
		}
	}
	if calls < 500 {
		t.Errorf("the oracle sweep made only %d calls", calls)
	}
}

func normBody(b []byte, v string) string { return strings.ReplaceAll(string(b), v, "X") }

// TestIntBFilterNotAnOracleByState (plan 4): every Listener request with every open parameter is answered, then the Owner and
// the Editor change only hidden facts (private comments, Muse membership, listens, the Editor's ratings, the contents of a
// draft Record); the answers must not move by one id. A parameter that read any of them would be a boolean oracle.
func TestIntBFilterNotAnOracleByState(t *testing.T) {
	t.Parallel()
	w := newIntbWorld(t)
	var reqs []string
	for _, o := range listenerQueryOps(intbOps(t)) {
		for _, p := range o.Query {
			if filterDecision(p.Name) != "open" {
				continue
			}
			for _, v := range w.filterValues(t, p) {
				reqs = append(reqs, w.listenerGetPath(o, p.Name+"="+url.QueryEscape(v)))
			}
		}
	}
	reqs = append(reqs, "/api/tracks?limit=100", "/api/songs?limit=100", "/api/atlas", "/api/sounds/search", "/api/records", "/api/library/facets")
	snap := func() map[string]string {
		m := map[string]string{}
		for _, r := range reqs {
			resp := w.Carol.Do(t, "GET", r, nil)
			m[r] = signature(resp.Status, resp.Body)
		}
		return m
	}
	before := snap()
	own, bob := w.Own, w.Bob
	for i, id := range w.Visible[:min(30, len(w.Visible))] {
		own.OK(t, "POST", "/api/tracks/"+id+"/comments", map[string]any{"body": fmt.Sprintf("later private note %d", i)}, "Idempotency-Key", fmt.Sprintf("intb-state-o-%d", i))
		bob.OK(t, "POST", "/api/tracks/"+id+"/comments", map[string]any{"body": fmt.Sprintf("editor note %d", i)}, "Idempotency-Key", fmt.Sprintf("intb-state-b-%d", i))
		bob.OK(t, "PUT", "/api/tracks/"+id+"/rating", map[string]any{"stars": 1 + i%5, "verdict": "editor verdict"})
		own.OK(t, "POST", "/api/listen-events", map[string]any{"events": []map[string]any{{"id": fmt.Sprintf("0190bbbb-0000-7000-8000-%012d", i), "track_id": id, "session_id": "intb-state",
			"kind": "complete", "client_at": "2026-11-19T10:00:00Z"}}})
	}
	mu := obj(own.OK(t, "POST", "/api/muses", map[string]any{"kind": "brief", "title": "A later Muse", "brief": "another brief"}, "Idempotency-Key", "intb-state-muse"))
	own.OK(t, "POST", "/api/muses/"+mu["id"].(string)+"/songs", map[string]any{"song_ids": w.Songs[len(w.Songs)-8:]}, "Idempotency-Key", "intb-state-assign")
	rec := obj(own.OK(t, "GET", "/api/records/"+w.Draft, nil))
	own.OK(t, "POST", "/api/records/"+w.Draft+"/slots", map[string]any{"revision": obj(rec["record"])["revision"], "ops": []map[string]any{{"op": "add", "song_id": w.Songs[3]}}})
	w.R.Settle(t)
	after := snap()
	for _, r := range reqs {
		if before[r] != after[r] {
			t.Errorf("%s answers differently after only hidden data changed:\n  before: %.240s\n  after:  %.240s", r, before[r], after[r])
		}
	}
}
