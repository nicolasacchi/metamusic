package exports_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/export"
	"github.com/nicolasacchi/metamusic/internal/exports"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/playlists"
)

// newEnvM2c is newEnv plus the two M2c rows (a reference FLAC and a lossy reference).
func newEnvM2c(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	lt.ApplyM2c(t, e.st, lt.FixedNow)
	return e
}

func trackSel(ids ...string) export.Selection {
	return export.Selection{Kind: "tracks", Tracks: export.TrackFilter{TrackIDs: ids}}
}

func TestExport_ExcludesUnknownRecipe(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	ctx := context.Background()
	base, err := e.svc.JSONL(ctx, human, trackSel(lt.TrackLuna, lt.TrackCitta), "a.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range base.JSONL.Warnings {
		if w.Code == "recipe_unknown_excluded" {
			t.Errorf("note without reference tracks: %+v", w)
		}
	}
	got, err := e.svc.JSONL(ctx, human, trackSel(lt.TrackLuna, lt.TrackCitta, lt.TrackRefFlac), "b.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if got.JSONL.Rows != 2 || !bytes.Equal(e.exportFile(t, got), e.exportFile(t, base)) {
		t.Errorf("rows %d, bytes differ from the known-only export", got.JSONL.Rows)
	}
	var refReq string
	if err := e.st.R.QueryRow(`SELECT r.request_id FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, lt.TrackRefFlac).Scan(&refReq); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(e.exportFile(t, got), []byte(refReq)) {
		t.Error("the reference's request id is in the export")
	}
	// the note names exactly the excluded track
	var note *export.Note
	for i := range got.JSONL.Warnings {
		if got.JSONL.Warnings[i].Code == "recipe_unknown_excluded" {
			note = &got.JSONL.Warnings[i]
		}
	}
	if note == nil || note.Details["count"] != 1 || !cmp.Equal(note.Details["track_ids"], []string{lt.TrackRefFlac}) {
		t.Errorf("note %+v", got.JSONL.Warnings)
	}
}

func TestExport_OnlyReferenceSelection(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	_, err := e.svc.JSONL(context.Background(), human, trackSel(lt.TrackRefFlac, lt.TrackLossy), "c.jsonl")
	pe := problem(t, err, 422, "EXPORT_EMPTY")
	notes, ok := pe.Details["notes"].([]export.Note)
	if !ok || len(notes) != 1 || notes[0].Code != "recipe_unknown_excluded" || notes[0].Details["count"] != 2 {
		t.Fatalf("details %+v", pe.Details)
	}
	ids, _ := notes[0].Details["track_ids"].([]string)
	if !slices.Equal(ids, []string{lt.TrackRefFlac, lt.TrackLossy}) {
		t.Errorf("track_ids %v", ids)
	}
	// a selection matching nothing at all keeps the plain error
	_, err = e.svc.JSONL(context.Background(), human, trackSel("nope"), "d.jsonl")
	if pe := problem(t, err, 422, "EXPORT_EMPTY"); pe.Details != nil {
		t.Errorf("details %+v", pe.Details)
	}
}

func TestDBSource_UnknownRecipeTracks(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	src := exports.NewDBSource(e.st, e.lib, e.pls)
	ids, err := src.UnknownRecipeTracks(context.Background(), export.TrackFilter{})
	if err != nil || !slices.Equal(ids, []string{lt.TrackRefFlac, lt.TrackLossy}) {
		t.Errorf("unknown tracks %v %v", ids, err)
	}
	rows, err := src.SelectRecipes(context.Background(), export.TrackFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if strings.HasPrefix(r.RequestID, "ref_") {
			t.Errorf("stub recipe selected: %+v", r)
		}
	}
}

// id3v2 wraps payload in an ID3v2.3 tag of n filler bytes.
func id3v2(n int, payload []byte) []byte {
	h := []byte{'I', 'D', '3', 3, 0, 0, byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
	return append(append(h, bytes.Repeat([]byte{0}, n)...), payload...)
}

func TestZip_IncludesLossyAndReference(t *testing.T) {
	t.Parallel()
	e := newEnvM2c(t)
	ctx := context.Background()
	// the lossy master carries its own ID3v2 tag, which the export must replace
	payload := bytes.Repeat([]byte{0xff, 0xfb, 0x90, 0x00, 0x11}, 200)
	var lossyPath string
	if err := e.st.R.QueryRow(`SELECT master_path FROM tracks WHERE id = ?`, lt.TrackLossy).Scan(&lossyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.dir, lossyPath), id3v2(300, payload), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := e.pls.Create(ctx, human, playlists.NewPlaylist{Name: "mixed", TrackIDs: []string{lt.TrackLuna, lt.TrackRefFlac, lt.TrackLossy}})
	if err != nil {
		t.Fatal(err)
	}
	x, err := e.svc.PlaylistZIP(ctx, human, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if x.ZIP.Entries != 3 || len(x.ZIP.MissingMP3) != 0 {
		t.Fatalf("zip %+v", x.ZIP)
	}
	_, files := readZip(t, e.exportFile(t, x))
	txxx := func(entry string) map[string]string {
		f, _ := frames(t, files[entry])
		m := map[string]string{}
		for _, kv := range f["TXXX"] {
			k, v, _ := strings.Cut(string(kv[1:]), "\x00")
			m[k] = v
		}
		return m
	}
	byTrack := map[string]string{}
	for _, tr := range x.ZIP.Tracks {
		byTrack[tr.TrackID] = tr.Entry
	}
	known := txxx(byTrack[lt.TrackLuna])
	if known["MM_SEED"] == "" || known["MM_REQUEST_ID"] == "" || known["MM_REFERENCE"] != "" {
		t.Errorf("known TXXX %v", known)
	}
	for _, id := range []string{lt.TrackRefFlac, lt.TrackLossy} {
		m := txxx(byTrack[id])
		if m["MM_REFERENCE"] != "1" || m["MM_TAKE_ID"] != id || m["MM_STATUS"] == "" {
			t.Errorf("%s TXXX %v", id, m)
		}
		if _, ok := m["MM_SEED"]; ok {
			t.Errorf("%s carries MM_SEED", id)
		}
		if _, ok := m["MM_REQUEST_ID"]; ok {
			t.Errorf("%s carries MM_REQUEST_ID", id)
		}
	}
	_, audio := frames(t, files[byTrack[lt.TrackLossy]])
	if !bytes.Equal(audio, payload) {
		t.Errorf("lossy audio: %d bytes, want the master's %d after its own tag", len(audio), len(payload))
	}
}
