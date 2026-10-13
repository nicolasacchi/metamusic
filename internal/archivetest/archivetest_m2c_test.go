package archivetest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/importer"
	"github.com/nicolasacchi/metamusic/internal/importer/inbox"
	"github.com/nicolasacchi/metamusic/internal/media/mp3info"
	"github.com/nicolasacchi/metamusic/internal/synth"
	"github.com/nicolasacchi/metamusic/internal/testtools"
)

// TestReferenceProfile_DryRun generates the reference archive with the real encoders and plans it with
// both reference flags (no ingest): the plan must match Manifest.Expect, every generated MP3 must
// parse as layer III and the fake one must not.
func TestReferenceProfile_DryRun(t *testing.T) {
	t.Parallel()
	testtools.Require(t, "ffmpeg", "flac", "metaflac")
	dir := t.TempDir()
	man, err := synth.Archive(t.Context(), testtools.Tools(), dir, synth.ProfileReference, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range man.Files {
		if f.Kind != "mp3" || strings.HasSuffix(f.Path, "syn_fake.mp3") {
			continue
		}
		fh, err := os.Open(filepath.Join(dir, filepath.FromSlash(f.Path)))
		if err != nil {
			t.Fatal(err)
		}
		st, _ := fh.Stat()
		_, perr := mp3info.Parse(fh, st.Size())
		_ = fh.Close()
		if perr != nil {
			t.Errorf("%s: %v", f.Path, perr)
		}
	}
	ro, err := inbox.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	im, err := importer.New(nil, nil, nil, nil, importer.Options{Inbox: ro, Principal: "seed-synthetic", ReferenceUnmatched: true, ReferenceLossy: true})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := im.Inventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	s, x := inv.Report.Summary, man.Expect
	if s.TracksNew != x.Tracks || s.TracksReference != x.ReferenceTracks || s.TracksLossy != x.LossyTracks ||
		s.ReferenceGroups != x.ReferenceGroups || s.SiblingsInferred != x.SiblingEdges || s.AudioFiles != x.AudioFiles || s.Ignored != x.Ignored {
		t.Errorf("summary %+v\nexpect %+v", s, x)
	}
	if s.Errors != 0 {
		t.Errorf("errors: %+v", inv.Report.Errors)
	}
	// without the flags the same archive plans only the e2e takes
	im2, _ := importer.New(nil, nil, nil, nil, importer.Options{Inbox: ro, Principal: "seed-synthetic"})
	inv2, err := im2.Inventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if inv2.Report.Summary.TracksNew != x.Tracks-x.ReferenceTracks || inv2.Report.Summary.TracksReference != 0 {
		t.Errorf("plain plan = %+v", inv2.Report.Summary)
	}
}
