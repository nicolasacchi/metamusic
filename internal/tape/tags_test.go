package tape

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
)

func tagMap(c []flacmeta.Comment) map[string]string {
	m := map[string]string{}
	for _, e := range c {
		m[e.Key] = e.Value
	}
	return m
}

func tagsInput(lossy int) TagsInput {
	items := plannedN(4)
	for i := 0; i < lossy; i++ {
		items[i].Item.Lossy = true
	}
	items[1].Resampled, items[2].Upmixed = true, true
	return TagsInput{
		Snapshot: Snapshot{
			RecordID: "rec-1", Title: "Night Drive", Description: "A liner line.",
			Tags: []flacmeta.Comment{{Key: "TITLE", Value: "Night Drive"}, {Key: "ALBUM", Value: "Night Drive"}, {Key: "ARTIST", Value: "metamusic"},
				{Key: "DATE", Value: "2026-10-01"}, {Key: "GENRE", Value: "dream pop; synthwave"}},
		},
		Items: items, TargetLUFS: -14.0, IncludeLiner: true,
	}
}

func TestTags_Table(t *testing.T) {
	t.Parallel()
	got := BuildTags(tagsInput(0))
	want := []flacmeta.Comment{
		{Key: "TITLE", Value: "Night Drive"}, {Key: "ALBUM", Value: "Night Drive"}, {Key: "ARTIST", Value: "metamusic"},
		{Key: "DATE", Value: "2026-10-01"}, {Key: "GENRE", Value: "dream pop; synthwave"},
		{Key: "DESCRIPTION", Value: "A liner line."},
		{Key: "REPLAYGAIN_ALBUM_GAIN", Value: "-4.00 dB"},
		{Key: "MM_RECORD_ID", Value: "rec-1"}, {Key: "MM_TAPE_VERSION", Value: "mm-tape/1"}, {Key: "MM_TARGET_LUFS", Value: "-14.0"},
		{Key: "MM_CONTAINS_LOSSY_SOURCES", Value: "false"},
		{Key: "MM_SOURCES", Value: "lossless=4;lossy=0;resampled=1;upmixed=1"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
	if err := flacmeta.ValidateComments(got); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(got, BuildTags(tagsInput(0))); diff != "" {
		t.Fatal("not deterministic")
	}

	t.Run("lossy", func(t *testing.T) {
		m := tagMap(BuildTags(tagsInput(2)))
		if m["MM_CONTAINS_LOSSY_SOURCES"] != "true" || m["DESCRIPTION"] != "A liner line.\nContains lossy sources (2 of 4 tracks)" ||
			m["MM_SOURCES"] != "lossless=2;lossy=2;resampled=1;upmixed=1" {
			t.Fatalf("%v", m)
		}
		in := tagsInput(1)
		in.IncludeLiner = false
		if m := tagMap(BuildTags(in)); m["DESCRIPTION"] != "Contains lossy sources (1 of 4 tracks)" {
			t.Fatalf("%q", m["DESCRIPTION"])
		}
	})
	t.Run("no liner, no lossy: no DESCRIPTION", func(t *testing.T) {
		in := tagsInput(0)
		in.IncludeLiner = false
		if _, ok := tagMap(BuildTags(in))["DESCRIPTION"]; ok {
			t.Fatal("DESCRIPTION present")
		}
	})
	t.Run("description keeps the lossy line under the cap", func(t *testing.T) {
		in := tagsInput(3)
		in.Snapshot.Description = strings.Repeat("é", 700)
		d := tagMap(BuildTags(in))["DESCRIPTION"]
		if len(d) > 1024 || !strings.HasSuffix(d, "Contains lossy sources (3 of 4 tracks)") {
			t.Fatalf("%d bytes: %q", len(d), d[len(d)-50:])
		}
	})
	t.Run("defaults and owned keys", func(t *testing.T) {
		in := tagsInput(0)
		in.Snapshot.Tags = []flacmeta.Comment{{Key: "DATE", Value: "2026-01-02"}, {Key: "MM_RECORD_ID", Value: "forged"},
			{Key: "REPLAYGAIN_ALBUM_PEAK", Value: "9"}, {Key: "CHAPTER001", Value: "x"}, {Key: "bad=key", Value: "x"}, {Key: "DESCRIPTION", Value: "forged"}}
		m := tagMap(BuildTags(in))
		if m["TITLE"] != "Night Drive" || m["ALBUM"] != "Night Drive" || m["ARTIST"] != "metamusic" || m["MM_RECORD_ID"] != "rec-1" ||
			m["DESCRIPTION"] != "A liner line." || m["DATE"] != "2026-01-02" {
			t.Fatalf("%v", m)
		}
		for _, k := range []string{"REPLAYGAIN_ALBUM_PEAK", "CHAPTER001", "bad=key"} {
			if _, ok := m[k]; ok {
				t.Errorf("%s survived", k)
			}
		}
	})
	t.Run("never a revision, tape id or export date", func(t *testing.T) {
		for _, c := range BuildTags(tagsInput(1)) {
			k := strings.ToLower(c.Key)
			if strings.Contains(k, "revision") || strings.Contains(k, "tape_id") || strings.Contains(k, "exported") {
				t.Errorf("tag %s varies between exports", c.Key)
			}
		}
	})
	t.Run("replaygain from the target", func(t *testing.T) {
		in := tagsInput(0)
		in.TargetLUFS = -16.5
		if m := tagMap(BuildTags(in)); m["REPLAYGAIN_ALBUM_GAIN"] != "-1.50 dB" || m["MM_TARGET_LUFS"] != "-16.5" {
			t.Fatalf("%v", m)
		}
	})
	t.Run("values are cleaned", func(t *testing.T) {
		in := tagsInput(0)
		in.Snapshot.Tags = []flacmeta.Comment{{Key: "GENRE", Value: "a\tb\x00c\xff"}}
		if g := tagMap(BuildTags(in))["GENRE"]; g != "a b c�" {
			t.Fatalf("%q", g)
		}
	})
}

func TestPeakTag(t *testing.T) {
	t.Parallel()
	for peak, want := range map[int32]string{0: "0.000000", 8388608: "1.000000", 4194304: "0.500000", 8388607: "1.000000"} {
		if got := PeakTag(peak); got.Key != "REPLAYGAIN_ALBUM_PEAK" || got.Value != want {
			t.Errorf("%d: %+v", peak, got)
		}
	}
}
