package archtest

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// writeOwnersL2 is C119.2: the packages that may write each L2 table (non-test Go; helper subtrees are exempt).
var writeOwnersL2 = map[string][]string{
	"records":        {"internal/records"},
	"record_slots":   {"internal/records"},
	"record_changes": {"internal/records"},
	"crates":         {"internal/crates"},
	"crate_items":    {"internal/crates"},
	"tapes":          {"internal/tape"},
}

var (
	// muses.ordinal is written by the database only: the 0010 backfill and the assign trigger.
	ordinalWriteRE = regexp.MustCompile(`(?is)\bupdate\s+muses\s+set\b[^;` + "`" + `"]*\bordinal\b|\binsert\s+(?:or\s+\w+\s+)?into\s+muses\s*\([^)]*\bordinal\b`)
	// a write to the settings table, and a curation.* key literal
	settingsWriteRE = regexp.MustCompile(`(?i)\b(?:insert\s+(?:or\s+\w+\s+)?into|update|delete\s+from)\s+settings\b`)
	curationKeyRE   = regexp.MustCompile(`"curation\.[a-z_]+"`)
	syncerKindRE    = regexp.MustCompile(`slot_upgraded|slot_filled|slot_emptied|slot_song_followed|KindSlotUpgraded|KindSlotFilled|KindSlotEmptied|KindSlotSongFollowed`)
	insertChangeRE  = regexp.MustCompile(`(?i)insert\s+(?:or\s+\w+\s+)?into\s+record_changes\b`)
	syncedSeqRE     = regexp.MustCompile(`(?is)\bupdate\s+record_slots\s+set\b[^;` + "`" + `"]*\b(?:synced_seq|song_id)\b`)
	duplicateRE     = regexp.MustCompile(`'duplicate'|ReasonDuplicate`)
)

// syncFile reports whether a file of internal/records is part of the Syncer (the code of Syncer.Settle and catchUpTx).
func syncFile(path string) bool {
	return inDir(path, "internal/records") && strings.HasPrefix(filepath.Base(path), "sync")
}

// l2Offenders returns one message per violation of C119.2 in files (slash paths relative to the repo root).
func l2Offenders(files map[string]string) []string {
	var off []string
	for path, src := range files {
		dir := path[:strings.LastIndex(path, "/")+1]
		if helperSubtree(strings.TrimSuffix(dir, "/")) || inDir(path, "internal/store/migrations") {
			continue
		}
		for _, m := range writeRE.FindAllStringSubmatch(src, -1) {
			owners, owned := writeOwnersL2[strings.ToLower(m[1])]
			if !owned {
				continue
			}
			allowed := false
			for _, o := range owners {
				allowed = allowed || inDir(path, o)
			}
			if !allowed {
				off = append(off, path+": writes "+m[1])
			}
		}
		if ordinalWriteRE.MatchString(src) {
			off = append(off, path+": writes muses.ordinal (the database owns it)")
		}
		if settingsWriteRE.MatchString(src) && curationKeyRE.MatchString(src) && !inDir(path, "internal/curation") {
			off = append(off, path+": writes a curation.* setting outside internal/curation")
		}
		if inDir(path, "internal/records") && !syncFile(path) {
			if syncedSeqRE.MatchString(src) {
				off = append(off, path+": updates record_slots.synced_seq or song_id outside the Syncer")
			}
			if insertChangeRE.MatchString(src) && (syncerKindRE.MatchString(src) || duplicateRE.MatchString(src)) {
				off = append(off, path+": inserts Syncer-owned changelog rows outside the Syncer")
			}
		}
	}
	return off
}

func TestSQL_WriteOwners_L2(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	all := map[string]string{}
	for p, s := range goFiles(t, filepath.Join(root, "internal")) {
		all["internal/"+p] = s
	}
	for p, s := range goFiles(t, filepath.Join(root, "cmd")) {
		all["cmd/"+p] = s
	}
	if len(all) < 100 {
		t.Fatalf("scanned only %d files", len(all))
	}
	if off := l2Offenders(all); len(off) > 0 {
		t.Errorf("C119.2 violated: %v", off)
	}
	// the checker bites
	plant := map[string]string{
		"internal/api/a.go":           "var q = `INSERT INTO records(id) VALUES (?)`",
		"internal/records/ok.go":      "var q = `UPDATE records SET title = ?`",
		"internal/crates/b.go":        "var q = `DELETE FROM record_slots`",
		"internal/albums/c.go":        "var q = `UPDATE crate_items SET removed_at = ?`",
		"internal/crates/ok.go":       "var q = `INSERT OR IGNORE INTO crate_items(id) VALUES (?)`",
		"internal/recordview/d.go":    "var q = `INSERT INTO tapes(id) VALUES (?)`",
		"internal/tape/ok.go":         "var q = `UPDATE tapes SET state = 'running'`",
		"internal/library/e.go":       "var q = `UPDATE muses SET ordinal = 5 WHERE id = ?`",
		"internal/muses/f.go":         "var q = `INSERT INTO muses(id, title, ordinal) VALUES (?, ?, ?)`",
		"internal/muses/ok.go":        "var q = `UPDATE muses SET title = ? WHERE id = ?`",
		"internal/api/g.go":           "var q = `INSERT INTO settings(key, value) VALUES ('x', 'y')` + \"curation.export_muse_names\"",
		"internal/api/ok.go":          "var q = `SELECT value FROM settings WHERE key = ?` + \"curation.export_muse_names\"",
		"internal/curation/ok.go":     "var q = `INSERT INTO settings(key, value) VALUES ('curation.export_muse_names', 'true')` + \"curation.export_muse_names\"",
		"internal/records/h.go":       "var q = `UPDATE record_slots SET synced_seq = ? WHERE id = ?`",
		"internal/records/i.go":       "var q = `UPDATE record_slots SET song_id = ?, synced_seq = 0 WHERE id = ?`",
		"internal/records/sync.go":    "var q = `UPDATE record_slots SET song_id = ?, synced_seq = 0 WHERE id = ?`",
		"internal/records/j.go":       "var q = `INSERT INTO record_changes(kind) VALUES ('slot_upgraded')`",
		"internal/records/k.go":       "var q = `INSERT INTO record_changes(kind, reason) VALUES (?, ?)` + ReasonDuplicate",
		"internal/records/sync2.go":   "var q = `INSERT INTO record_changes(kind) VALUES ('slot_filled')`",
		"internal/records/edit.go":    "var q = `INSERT INTO record_changes(kind) VALUES ('slot_pinned')`",
		"internal/records/verify.go":  "var q = `SELECT kind FROM record_changes WHERE kind IN ('slot_upgraded', 'slot_filled')`",
		"internal/libtest/helper.go":  "var q = `INSERT INTO records(id) VALUES (?)`",
		"internal/store/migrations/x": "var q = `UPDATE muses SET ordinal = 1`",
	}
	off := l2Offenders(plant)
	want := map[string]bool{
		"internal/api/a.go": true, "internal/crates/b.go": true, "internal/albums/c.go": true, "internal/recordview/d.go": true, "internal/library/e.go": true,
		"internal/muses/f.go": true, "internal/api/g.go": true, "internal/records/h.go": true, "internal/records/i.go": true, "internal/records/j.go": true, "internal/records/k.go": true,
	}
	got := map[string]bool{}
	for _, o := range off {
		got[o[:strings.Index(o, ":")]] = true
	}
	for p := range want {
		if !got[p] {
			t.Errorf("planted offender %s was not found (found %v)", p, off)
		}
	}
	for p := range got {
		if !want[p] {
			t.Errorf("%s was reported but is allowed: %v", p, off)
		}
	}
}

// albumWordScope is where D-L2-2 applies: the L2 packages, every L2-named Go file and the L2 web files. The word belongs to
// the Subsonic catalog (internal/albums and the shipped protocol code), the FLAC ALBUM tag keys and the docs.
var (
	albumScopeDirs = []string{"internal/records", "internal/crates", "internal/tape", "internal/curation", "internal/liner", "internal/recordview"}
	albumWebFiles  = []string{
		"web/src/routes/Records.svelte", "web/src/routes/Record.svelte", "web/src/routes/Crates.svelte", "web/src/routes/Crate.svelte",
		"web/src/routes/SettingsCuration.svelte", "web/src/routes/HumanPublish.svelte", "web/src/routes/HumanCuration.svelte",
		"web/src/lib/recordView.ts", "web/src/lib/curationApi.ts", "web/src/lib/crateApi.ts", "web/src/lib/crateFilter.ts", "web/src/lib/tapeView.ts",
		"web/src/lib/energy.ts", "web/src/lib/recordPlayer.ts",
		"web/src/lib/components/RecordCover.svelte", "web/src/lib/components/SlotRow.svelte", "web/src/lib/components/SongPicker.svelte",
		"web/src/lib/components/ChangeLog.svelte", "web/src/lib/components/LinerPreview.svelte", "web/src/lib/components/EnergyCurve.svelte",
		"web/src/lib/components/TapePanel.svelte", "web/src/lib/components/RecordStatus.svelte", "web/src/lib/components/AddToCrate.svelte",
		"web/src/lib/components/AddToRecord.svelte", "web/src/lib/components/FilterBuilder.svelte", "web/src/lib/components/CrateMembers.svelte",
		"web/src/lib/components/ChapterList.svelte", "web/src/lib/components/CurationToggles.svelte",
	}
	albumWordRE = regexp.MustCompile(`(?i)album`)
	// forms that name the catalog package or a FLAC tag key, not the product vocabulary
	albumAllowedRE = regexp.MustCompile(`internal/albums|\balbums\.[A-Z]\w*|\bpackage albums\b|\bAlbums\s+\*?albums\.|\bAlbums:\s*albums\.|\.Albums\b|REPLAYGAIN_ALBUM_(?:GAIN|PEAK)|"ALBUM"|\bALBUM=`)
)

// albumInScope reports whether a source path is under D-L2-2.
func albumInScope(path string) bool {
	for _, d := range albumScopeDirs {
		if inDir(path, d) {
			return true
		}
	}
	if strings.HasSuffix(path, ".go") && strings.Contains(filepath.Base(path), "l2") && !inDir(path, "internal/albums") {
		return true
	}
	for _, f := range albumWebFiles {
		if path == f {
			return true
		}
	}
	if strings.HasPrefix(path, "web/src/") && !strings.HasSuffix(path, ".test.ts") && !strings.HasSuffix(path, ".d.ts") {
		// routes and components added later keep the rule: anything named Record*, Crate* or Tape*
		base := strings.ToLower(filepath.Base(path))
		return strings.HasPrefix(base, "record") || strings.HasPrefix(base, "crate") || strings.HasPrefix(base, "tape")
	}
	return false
}

func albumOffenders(files map[string]string) []string {
	var off []string
	for path, src := range files {
		if !albumInScope(path) || strings.HasSuffix(path, "_test.go") {
			continue
		}
		if albumWordRE.MatchString(albumAllowedRE.ReplaceAllString(src, "")) {
			off = append(off, path)
		}
	}
	return off
}

// TestNoAlbumWord_L2 is D-L2-2: the product vocabulary is Record, slot, Crate and Tape; the word album stays inside the
// Subsonic catalog package, the FLAC ALBUM tag keys and the docs.
func TestNoAlbumWord_L2(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	all := map[string]string{}
	for p, s := range goFiles(t, filepath.Join(root, "internal")) {
		all["internal/"+p] = s
	}
	for p, s := range goFiles(t, filepath.Join(root, "cmd")) {
		all["cmd/"+p] = s
	}
	for _, f := range albumWebFiles {
		if b, err := os.ReadFile(filepath.Join(root, f)); err == nil {
			all[f] = string(b)
		}
	}
	// any other L2-named web source that exists (routes and components added later keep the rule)
	err := fs.WalkDir(os.DirFS(root), "web/src", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.Contains(p, "node_modules") || !albumInScope(p) {
			return nil
		}
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			return err
		}
		all[p] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if off := albumOffenders(all); len(off) > 0 {
		t.Errorf("the word album appears outside the Subsonic catalog (D-L2-2): %v", off)
	}
	plant := map[string]string{
		"internal/records/a.go":                   "// the album of a Record",
		"internal/tape/b.go":                      `var k = "ALBUM"; var g = "REPLAYGAIN_ALBUM_GAIN"; var p = "REPLAYGAIN_ALBUM_PEAK"`,
		"internal/tape/c.go":                      `var t = "Album title"`,
		"internal/app/l2_albums.go":               "import \"github.com/nicolasacchi/metamusic/internal/albums\"\nvar a = albums.New()\ntype D struct{ Albums *albums.Service }",
		"internal/app/l2_x.go":                    "// one album per Muse",
		"internal/api/ops_records_l2.go":          "var d = `Subsonic albums`",
		"internal/subsonic/browse.go":             "// getAlbum",
		"internal/albums/catalog.go":              "// the album catalog",
		"web/src/routes/Records.svelte":           "<h2>Albums</h2>",
		"web/src/routes/Library.svelte":           "<p>album</p>",
		"internal/records/a_test.go":              "// album in a test",
		"internal/curation/ok.go":                 "// opt-ins for the catalog names",
		"internal/recordview/d.go":                "type X struct{ Album string }",
		"web/src/lib/components/TapePanel.svelte": "ALBUM=x",
		"web/src/lib/tapeFoo.ts":                  "const t = 'album art'",
		"web/src/routes/Offline.svelte":           "<p>album</p>",
	}
	got := map[string]bool{}
	for _, p := range albumOffenders(plant) {
		got[p] = true
	}
	for _, p := range []string{"internal/records/a.go", "internal/tape/c.go", "internal/app/l2_x.go", "internal/api/ops_records_l2.go", "web/src/routes/Records.svelte", "internal/recordview/d.go", "web/src/lib/tapeFoo.ts"} {
		if !got[p] {
			t.Errorf("planted offender %s was not found", p)
		}
	}
	if len(got) != 7 {
		t.Errorf("offenders %v, want exactly the seven planted ones", got)
	}
}
