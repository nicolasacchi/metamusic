package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// U1 C308.2 (lane V1): a Listener sees the whole library except discarded takes, which answer 404. Every operation a
// Listener may call and that reads a take must go through the visibility gate, or say in this file why it does not.
//
// The scan reads the Register calls of internal/api/ops_*.go. An operation is Listener-reachable when its Meta scope is
// listen or rate and it is not OwnerOnly (the role matrix, F2, pins the same fact from the registry). Each such operation
// must be in exactly one table below:
//   - visibleGated: the handler calls d.visible(...), the by-id gate of library.Visible;
//   - visibleFiltered: the handler reads no take id of its own; its service filters discarded takes in SQL through
//     view.Viewer (one reason each, pinned by the acceptance tests TestListenerDiscardedIs404 and TestListenerDiscardedNeverListed);
//   - visibleNoTrack: the operation reads no take at all.
//
// A new Listener-reachable operation fails the test until it is classified, so a track reader cannot be added silently.
var visibleGated = map[string]string{
	"get_track": "", "get_track_peaks": "", "get_track_analysis": "", "get_track_spectrogram": "", "compare_tracks": "",
	"get_clip": "", "get_download_link": "", "get_media_links": "", "get_lineage": "", "rate_track": "",
}

var visibleFiltered = map[string]string{
	"search_tracks":      "SQL: compileSearch appends view.NotDiscarded; Listener q and filter rules in the handler",
	"get_library_facets": "SQL: every facet count excludes discarded takes",
	"list_songs":         "SQL: songs.FilterSQL (track_id clause, all-discarded Songs dropped) and the visible state table, Pick and root refs; a discarded track_id lists nothing, like an unknown one (a 404 would be an oracle)",
	"get_song":           "service: songview resolves by=track through the visible takes, takeRows and the lineage block filter them",
	"get_song_atlas":     "Songs only, no take id in the answer; the Song set is the list_songs filter",
	"search_sounds":      "Songs only; the Song set is the list_songs filter",
	"suggest_sequence":   "Songs and Records only (no take id input); a slot on a discarded take resolves as an empty slot",
	"list_playlists":     "SQL: count and duration exclude discarded takes",
	"get_playlist":       "SQL: summaries exclude discarded takes and positions stay dense",
	"list_crates":        "service: crates.count excludes discarded takes of a manual take Crate; smart counts go through SearchTracks",
	"get_crate":          "service: same count rule as list_crates; members are listed through search_tracks with crate_id",
	"list_records":       "service: a slot on a discarded take is an empty slot (records.Resolved), summaries already treat it as not playable",
	"get_record":         "service: records.Resolved turns a slot on a discarded take into an empty slot; the changelog drops hidden takes",
	"get_record_changes": "service: recordview.ChangeViews drops hidden take ids and titles",
	"get_tape":           "service: tape.Get answers 404 for a tape that carries a discarded take",
	"log_listen_events":  "service: listening.Log treats a discarded take as an unknown track (a per-event rejection, never a 404)",
	"list_tags":          "SQL: tag counts exclude discarded takes",
}

var visibleNoTrack = map[string]string{
	"whoami": "the caller", "ping": "liveness", "get_sound_taxonomy": "the taxonomy, no take data",
	"put_device_profile": "a playback profile of the caller's own device; no take involved",
}

var (
	registerSplitRE = regexp.MustCompile(`(?m)^\s*Register\(a, `)
	opIDRE          = regexp.MustCompile(`OperationID:\s*"([a-z0-9_]+)"`)
	metaRE          = regexp.MustCompile(`Meta\{([^}]*)\}`)
)

type handlerSrc struct{ file, body string }

// listenerHandlers maps every Listener-reachable operation id to its Register source.
func listenerHandlers(t *testing.T) map[string]handlerSrc {
	t.Helper()
	out := map[string]handlerSrc{}
	for path, src := range goFiles(t, filepath.Join(repoRoot(t), "internal", "api")) {
		if !strings.HasPrefix(path, "ops_") {
			continue
		}
		parts := registerSplitRE.Split(src, -1)
		for _, chunk := range parts[1:] {
			m := opIDRE.FindStringSubmatch(chunk)
			meta := metaRE.FindStringSubmatch(chunk)
			if m == nil || meta == nil {
				continue
			}
			scoped := strings.Contains(meta[1], "ScopeListen") || strings.Contains(meta[1], "ScopeRate")
			if scoped && !strings.Contains(meta[1], "OwnerOnly: true") {
				out[m[1]] = handlerSrc{file: path, body: chunk}
			}
		}
	}
	return out
}

func TestTrackHandlersCallVisible(t *testing.T) {
	t.Parallel()
	hs := listenerHandlers(t)
	if len(hs) < 25 {
		t.Fatalf("only %d Listener-reachable operations found; the scan is broken", len(hs))
	}
	var bad []string
	for id, h := range hs {
		_, gated := visibleGated[id]
		_, filtered := visibleFiltered[id]
		_, none := visibleNoTrack[id]
		n := 0
		for _, b := range []bool{gated, filtered, none} {
			if b {
				n++
			}
		}
		switch {
		case n == 0:
			bad = append(bad, id+" ("+h.file+"): a Listener may call it and it is not classified; call d.visible or list it with a reason")
		case n > 1:
			bad = append(bad, id+": listed in more than one table")
		case gated && !strings.Contains(h.body, "d.visible(ctx"):
			bad = append(bad, id+" ("+h.file+"): classified as gated but its handler does not call d.visible(ctx, ...)")
		case !gated && strings.Contains(h.body, "d.visible(ctx"):
			bad = append(bad, id+": calls d.visible but is not in visibleGated")
		}
	}
	for _, tbl := range []map[string]string{visibleFiltered, visibleNoTrack} {
		for id, why := range tbl {
			if strings.TrimSpace(why) == "" {
				bad = append(bad, id+": allow-list entry without a reason")
			}
			if _, ok := hs[id]; !ok {
				bad = append(bad, id+": listed but no longer a Listener-reachable operation")
			}
		}
	}
	for id := range visibleGated {
		if _, ok := hs[id]; !ok {
			bad = append(bad, id+": gated but no longer a Listener-reachable operation")
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("C308.2 visibility gate:\n%s", strings.Join(bad, "\n"))
	}
}

// The scan itself must see the gate: a handler source without the call is reported.
func TestTrackHandlersCallVisible_PatternCatches(t *testing.T) {
	t.Parallel()
	src := "\tRegister(a, huma.Operation{\n\t\tOperationID: \"get_x\",\n\t}, Meta{Scope: auth.ScopeListen, Cost: \"free\"},\n\t\tfunc(ctx context.Context, in *trackIDIn) (*o, error) {\n\t\t\treturn nil, nil\n\t\t})\n"
	parts := registerSplitRE.Split(src, -1)
	if len(parts) != 2 || opIDRE.FindStringSubmatch(parts[1])[1] != "get_x" || !strings.Contains(metaRE.FindStringSubmatch(parts[1])[1], "ScopeListen") {
		t.Fatalf("the Register scan does not parse a handler: %v", parts)
	}
	if strings.Contains(parts[1], "d.visible(ctx") {
		t.Fatalf("a handler without the gate reads as gated")
	}
}

// TestLineageTracksCallers pins that library.LineageTracks, which is unfiltered on purpose (it feeds the Owner-side duels
// and the tag numbering of a download), is called only from those packages. A Listener-reachable caller would leak
// discarded takes; it needs a viewer-filtered variant instead.
func TestLineageTracksCallers(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	allowed := map[string]bool{"internal/duels": true, "internal/media/tagged": true, "internal/library": true}
	files, err := filepath.Glob(filepath.Join(root, "internal", "*", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	deep, _ := filepath.Glob(filepath.Join(root, "internal", "*", "*", "*.go"))
	files = append(files, deep...)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		rel, _ := filepath.Rel(root, f)
		dir := filepath.ToSlash(filepath.Dir(rel))
		if allowed[dir] {
			continue
		}
		if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), ".LineageTracks(") {
			t.Errorf("%s calls library.LineageTracks, which does not filter discarded takes for a Listener (C308.2)", rel)
		}
	}
}
