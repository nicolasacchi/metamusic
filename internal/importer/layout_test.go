package importer

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
)

// This file builds the C21 `acceptance` inbox layout with header-only FLACs
// (flactest). M1-B9 checks synth.Archive against the same numbers.

const syn06Lyrics = "[Verse 1]\nla six\n[Chorus]\nsing it"

func synLyrics(id string) string { return fmt.Sprintf("[Verse 1]\nla %s\n[Chorus]\nsing it", id) }

// threeSections matches abc3 (verse / chorus / outro).
func synLyrics3(id string) string {
	return fmt.Sprintf("[Verse 1]\nla %s\n[Chorus]\nsing it\n[Outro]\nfade", id)
}

func synYue2Request(id string, seed int, lyrics string) string {
	b := new(strings.Builder)
	fmt.Fprintf(b, "{\n  \"style\": \"synthetic pop, 120 bpm\",\n  \"lyrics\": %q,\n  \"cot\": \"full\",\n  \"seed\": %d,\n  \"abc\": null,\n  \"cfg_scale\": null,\n  \"id\": %q\n}\n", lyrics, seed, id)
	return b.String()
}

// jsonStr renders a Go string as a JSON string without escaping non-ASCII or HTML.
func jsonStr(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

func line(id string, seed int, lyrics, cot, extra string) string {
	return fmt.Sprintf(`{%s"cot": %s, "id": %s, "lyrics": %s, "seed": %d, "style": "synthetic pop, 120 bpm"}`,
		extra, jsonStr(cot), jsonStr(id), jsonStr(lyrics), seed)
}

// acceptanceLayout creates the C21 acceptance inbox in e and the mapping file.
func acceptanceLayout(t *testing.T, e *env) {
	t.Helper()
	songs := lines(
		line("syn-song-01", 1001, synLyrics("syn-song-01"), "full", ""),
		line("syn-song-02", 1002, synLyrics("syn-song-02"), "full", ""),
		line("syn-song-03", 1003, "[Verse 1]\nè à ì <b>&\n[Chorus]\n< > &\n[Outro]\n<b>&", "full", ""),
		line("syn-song-04", 1004, synLyrics("syn-song-04"), "full", ""),
		line("syn-song-05", 1005, synLyrics3("syn-song-05"), "full", ""),
		line("syn-song-06", 1006, syn06Lyrics, "full", ""),
		line("syn-song-07", 1007, synLyrics("syn-song-07"), "full", ""),
		`{not json`,
		line("syn-song-01", 2001, synLyrics("syn-song-01"), "full", ""),
		line("syn-song-02-s2002", 2002, synLyrics("syn-song-02"), "full", ""),
		line("syn-song-08", 1008, synLyrics("syn-song-08"), "melody", `"abc_path": "abc/syn-song-08.abc", `),
	)
	e.put("music/songs.jsonl", songs)
	e.put("music/abc/syn-song-08.abc", "X:1\nT:eight\nM:4/4\nL:1/8\nQ:1/4=100\nK:C\nCDEF|CDEF|]\n")
	// per-song JSON: the same request plus a title (same hash, second source)
	e.put("music/requests/syn-song-06.json", line("syn-song-06", 1006, syn06Lyrics, "full", `"title": "Synthetic Six", `))

	e.flac("music/flac/syn_song_01.flac", "01")
	e.flac("music/flac/syn_song_01_s2001.flac", "01-2001")
	e.flac("music/flac/syn_song_02.flac", "02")
	e.flac("music/flac/syn_song_02_s2002.flac", "02-2002")
	e.flac("music/flac/syn_song_03.flac", "03")
	e.flac("music/flac/syn_song_04.flac", "04")
	e.flac("music/flac/take_final.flac", "07", "MM_REQUEST_ID=syn-song-07")
	e.flac("music/flac/synthetic_six.flac", "06")
	e.flac("music/results/syn-song-05/audio.flac", "05")
	e.flac("music/flac/syn_song_05.flac", "05") // byte copy of the result-dir audio
	e.put("music/flac/syn_song_04.mp3", "mp3-04")
	e.flac("music/flac/.syn_song_06.flac.Xy12Ab", "06-take2") // rsync temp: a distinct complete take
	if err := os.Symlink("syn_song_02.flac", e.abs("music/flac/link_to_02.flac")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(e.abs("music/flac/pipe.flac"), 0o644); err != nil {
		t.Skip(err)
	}
	e.flac("music/audio/syn_song_04.flac", "04") // byte duplicate
	e.flac("music/player-export/Syn Song 04.flac", "04", "TITLE=Syn Song 04", "ENCODER=player")

	e.put("music/mp3/syn_song_02.mp3", "mp3-02")
	e.put("music/mp3/syn_song_03.mp3", "mp3-03")
	e.put("music/mp3/take_final.mp3", "mp3-tf")
	e.put("music/mp3/orphan_take.mp3", "mp3-orphan")

	rd := "music/results/syn-song-05/"
	e.put(rd+"request.json", synYue2Request("syn-song-05", 1005, synLyrics3("syn-song-05")))
	e.put(rd+"result.json", `{"truncated": {"abc": false, "semantic": true}, "duration": 12.0}`)
	e.put(rd+"config.json", `{"model": "synthetic"}`)
	e.put(rd+"plan.json", `{"sections": 3}`)
	e.put(rd+"score.abc", abc3)
	e.put(rd+"semantic.npy", "npy-semantic")
	e.put(rd+"latent.npy", "npy-latent")

	e.put("music/scores/syn_song_03.abc", abc3)

	var backlog, pilot []string
	for g := 1; g <= 4; g++ {
		for v := 1; v <= 3; v++ {
			id := fmt.Sprintf("synb_%03d_v%d", g, v)
			l := line(id, 3000+g*10+v, synLyrics(id), "full", "")
			backlog = append(backlog, l)
			if g == 1 {
				pilot = append(pilot, l)
			}
		}
	}
	e.put("music/backlog-variants.jsonl", lines(backlog...))
	e.put("music/pilot.jsonl", lines(pilot...))
	e.put("music/pilot.json", `{"name": "pilot-synthetic", "rule": "synthetic rule", "songs": [{"n": 1, "title": "Synthetic Title", "artist": "Synthetic Artist", "why": "synthetic why", "variant_ids": ["synb_001_v1", "synb_001_v2", "synb_001_v3"]}]}`)
	var csv strings.Builder
	csv.WriteString("\xef\xbb\xbfvariant_id,track_n,source_title,source_artist,variant,variant_type,seed\r\n")
	n := 0
	for g := 1; g <= 4; g++ {
		for v := 1; v <= 3; v++ {
			n++
			seed := 3000 + g*10 + v
			if g == 3 && v == 2 {
				seed = 99999 // differs from the JSONL line
			}
			fmt.Fprintf(&csv, "synb_%03d_v%d,%d,Synthetic Title %d,Synthetic Artist %d,v%d,type%d,%d\r\n", g, v, n, g, g, v, v, seed)
		}
	}
	e.put("music/index.csv", csv.String())
	e.put("music/playlist.json", `{"playlist": "synthetic", "owner": "synthetic owner", "url": "https://mm.example.com/p", "note": "", "tracks": [{"title": "Synthetic Playlist Title", "artist": "Synthetic Playlist Artist"}]}`)
	e.put("music/review.json", `{"pass": true, "files": 3, "errors": [], "warnings": []}`)
	e.put("music/old-batch.zip", "PK\x03\x04../x")

	e.put("varianti/syn-song-01-v2.json", line("syn-song-01-v2", 4001, synLyrics("syn-song-01-v2"), "full", ""))
	e.flac("unmatched/mystery.flac", "mystery")
	e.mapping("music/flac/syn_song_01.flac,syn-song-01,1001,,\nunmatched/mystery.flac,-,,,\n")
}

// acceptanceExpect are the C21 numbers a dry-run of the layout must report.
func checkAcceptanceSummary(t *testing.T, s Summary) {
	t.Helper()
	want := map[string][2]int{
		"recipes_unique": {s.RecipesUnique, 23}, "recipe_sources": {s.RecipeSources, 28}, "recipes_valid": {s.RecipesValid, 28},
		"recipe_lines_invalid": {s.RecipeLinesInvalid, 1}, "tracks_new": {s.TracksNew, 9}, "drafts_new": {s.DraftsNew, 23},
		"drafts_fulfilled": {s.DraftsFulfilled, 9}, "collections": {s.Collections, 1}, "refs_attached": {s.RefsAttached, 11},
		"siblings_inferred": {s.SiblingsInferred, 2}, "scores_attached": {s.ScoresAttached, 2}, "audio_files": {s.AudioFiles, 13},
		"audio_identities": {s.AudioIdentities, 10}, "duplicate_groups": {s.DuplicateGroups, 2}, "ignored": {s.Ignored, 12},
		"audio_skipped": {s.AudioSkipped, 1}, "errors": {s.Errors, 1}, "audio_matched": {s.AudioMatched, 9},
		"audio_unmatched": {s.AudioUnmatched, 0}, "audio_ambiguous": {s.AudioAmbiguous, 0}, "unsettled": {s.Unsettled, 0},
	}
	for k, v := range want {
		if v[0] != v[1] {
			t.Errorf("summary.%s = %d, want %d", k, v[0], v[1])
		}
	}
}
