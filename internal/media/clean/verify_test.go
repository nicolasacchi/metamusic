package clean

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/media/clean/cleantest"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
)

func TestCheckFLAC(t *testing.T) {
	t.Parallel()
	frames := bytes.Repeat([]byte{0xff, 0xf8}, 100)
	ok := func(comments ...string) flactest.Spec {
		return flactest.Spec{TotalSamples: 96000, Vendor: Vendor, SeekPoints: 2, Comments: comments, Frames: frames}
	}
	cue := func(b []byte) []byte { // the same file with an extra CUESHEET block
		info, err := flacmeta.Read(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if _, err := flacmeta.Rewrite(&out, bytes.NewReader(b), int64(len(b)), info, flacmeta.Replace{Cuesheet: flacmeta.CuesheetBlock(flacmeta.Cuesheet{})}); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	cases := []struct {
		name string
		b    []byte
		leak bool
	}{
		{"title only", flactest.Build(ok("TITLE=x")), false},
		{"title lower case", flactest.Build(ok("title=x")), false},
		{"no comment block", flactest.Build(flactest.Spec{TotalSamples: 96000, Frames: frames}), false},
		{"padding", flactest.Build(flactest.Spec{TotalSamples: 96000, Vendor: Vendor, Comments: []string{"TITLE=x"}, PaddingBytes: 100, Frames: frames}), false},
		{"MM key", flactest.Build(ok("TITLE=x", "MM_SEED=1")), true},
		{"other comment", flactest.Build(ok("TITLE=x", "ARTIST=y")), true},
		{"picture", flactest.Build(flactest.Spec{TotalSamples: 96000, Vendor: Vendor, Comments: []string{"TITLE=x"}, PictureBytes: 50, Frames: frames}), true},
		{"foreign vendor", flactest.Build(flactest.Spec{TotalSamples: 96000, Vendor: "reference libFLAC", Comments: []string{"TITLE=x"}, Frames: frames}), true},
		{"cuesheet", cue(flactest.Build(ok("TITLE=x"))), true},
	}
	for _, c := range cases {
		err := Check(KindFLAC, bytes.NewReader(c.b), int64(len(c.b)))
		if c.leak != errors.Is(err, ErrLeak) || (!c.leak && err != nil) {
			t.Errorf("%s: err = %v, leak want %v", c.name, err, c.leak)
		}
	}
	if err := Check(KindFLAC, bytes.NewReader([]byte("nope")), 4); err == nil || errors.Is(err, ErrLeak) {
		t.Errorf("not a flac: %v", err)
	}
	if err := Check("nope", nil, 0); err == nil {
		t.Errorf("unknown kind")
	}
	two := flactest.Build(ok("TITLE=x"))
	// two VORBIS_COMMENT blocks
	info, _ := flacmeta.Read(bytes.NewReader(two), int64(len(two)))
	bl := info.Blocks[len(info.Blocks)-1]
	dup := append([]byte{}, two[:info.FramesOffset]...)
	dup[bl.Offset] &^= 0x80 // not last any more
	dup = append(dup, dup[bl.Offset:bl.Offset+4+int64(bl.Length)]...)
	dup[len(dup)-int(bl.Length)-4] |= 0x80
	dup = append(dup, frames...)
	if err := Check(KindFLAC, bytes.NewReader(dup), int64(len(dup))); !errors.Is(err, ErrLeak) {
		t.Errorf("two comment blocks: %v", err)
	}
	long := strings.Repeat("k", 100) + "=v"
	if err := Check(KindFLAC, bytes.NewReader(flactest.Build(ok(long))), int64(len(flactest.Build(ok(long))))); !errors.Is(err, ErrLeak) || strings.Contains(err.Error(), long[:50]) {
		t.Errorf("long key: %v", err)
	}
}

func TestCheckMP3(t *testing.T) {
	t.Parallel()
	frames := cleantest.MPEGFrames(3, 5)
	tag := func(body []byte, ver byte, flags byte) []byte {
		sz := len(body)
		return append(append([]byte{'I', 'D', '3', ver, 0, flags}, byte(sz>>21&0x7f), byte(sz>>14&0x7f), byte(sz>>7&0x7f), byte(sz&0x7f)), body...)
	}
	fr := func(id string, n int) []byte {
		return append(append([]byte(id), 0, 0, 0, byte(n), 0, 0), bytes.Repeat([]byte{'a'}, n)...)
	}
	good := append(tag(fr("TIT2", 4), 4, 0), frames...)
	cases := []struct {
		name string
		b    []byte
		leak bool
	}{
		{"title only", good, false},
		{"no tag", frames, false},
		{"second frame", append(tag(append(fr("TIT2", 4), fr("TPE1", 4)...), 4, 0), frames...), true},
		{"txxx", append(tag(fr("TXXX", 6), 4, 0), frames...), true},
		{"v2.3", append(tag(fr("TIT2", 4), 3, 0), frames...), true},
		{"flags", append(tag(fr("TIT2", 4), 4, 0x80), frames...), true},
		{"padding after the frame", append(tag(append(fr("TIT2", 4), make([]byte, 5)...), 4, 0), frames...), true},
		{"truncated frame header", append(tag(append(fr("TIT2", 4), 'T', 'I'), 4, 0), frames...), true},
		{"frame overruns the tag", append(tag(fr("TIT2", 4)[:12], 4, 0), frames...), true},
		{"id3v1 trailer", append(append([]byte{}, good...), append([]byte("TAG"), make([]byte, 125)...)...), true},
		{"gap before the first frame", append(tag(fr("TIT2", 4), 4, 0), append([]byte{1, 2, 3}, frames...)...), true},
		{"bad size", append([]byte{'I', 'D', '3', 4, 0, 0, 0xff, 0xff, 0xff, 0xff}, frames...), true},
		{"size past the end", append([]byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0x7f, 0x7f}, frames...), true},
	}
	for _, c := range cases {
		err := Check(KindMP3, bytes.NewReader(c.b), int64(len(c.b)))
		if c.leak != errors.Is(err, ErrLeak) || (!c.leak && err != nil) {
			t.Errorf("%s: err = %v, leak want %v", c.name, err, c.leak)
		}
	}
	if err := Check(KindMP3, bytes.NewReader([]byte("no audio here")), 13); err == nil || errors.Is(err, ErrLeak) {
		t.Errorf("not an mp3: %v", err)
	}
}

func codes(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Severity+" "+f.Code)
	}
	return out
}

func TestVerify(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Options{})
	ctx := context.Background()
	built := map[string]Rendition{}
	for _, id := range []string{lt.TrackTwinA, lt.TrackTwinB, lt.TrackRefFlac, lt.TrackLuna} {
		for _, k := range []string{KindFLAC, KindMP3} {
			if id == lt.TrackLuna {
				continue
			}
			r, err := e.svc.Ensure(ctx, id, k)
			if err != nil {
				t.Fatal(id, k, err)
			}
			built[id+k] = r
		}
	}
	if r, err := e.svc.Ensure(ctx, lt.TrackLossy, KindMP3); err != nil {
		t.Fatal(err)
	} else {
		built[lt.TrackLossy] = r
	}
	if fs, err := Verify(ctx, e.st.R, e.data, true); err != nil || len(fs) != 0 {
		t.Fatalf("a clean library: %v %v", codes(fs), err)
	}
	masters := map[string][]byte{}
	for id, tn := range e.taints {
		masters[id] = e.read(t, tn.MasterPath)
	}

	// corrupted bytes: hash mismatch (error), file removed with remove set
	a := built[lt.TrackTwinA+KindFLAC]
	put(t, e.data, a.Path, append(e.read(t, a.Path), 0))
	// a tag leak: a file whose row matches its hash but carries MM_ keys
	leaky := flactest.Build(flactest.Spec{TotalSamples: 96000, Vendor: Vendor, Comments: []string{"TITLE=x", "MM_SEED=9"}, Frames: []byte{1, 2, 3}})
	b := built[lt.TrackTwinB+KindFLAC]
	put(t, e.data, b.Path, leaky)
	if _, err := e.st.W.Exec(`UPDATE renditions SET sha256 = ?, bytes = ? WHERE track_id = ? AND kind = ?`, sum(leaky), len(leaky), lt.TrackTwinB, KindFLAC); err != nil {
		t.Fatal(err)
	}
	// a missing file
	m := built[lt.TrackTwinB+KindMP3]
	if err := e.data.Remove(m.Path); err != nil {
		t.Fatal(err)
	}
	// the audio no longer matches the master: STREAMINFO MD5 differs
	if _, err := e.st.W.Exec(`UPDATE tracks SET audio_md5 = ? WHERE id = ?`, strings.Repeat("ab", 16), lt.TrackRefFlac); err != nil {
		t.Fatal(err)
	}
	// a row naming a file outside derived/
	if _, err := e.st.W.Exec(`UPDATE renditions SET path = 'audio/masters/x.flac' WHERE track_id = ? AND kind = ?`, lt.TrackRefFlac, KindMP3); err != nil {
		t.Fatal(err)
	}
	fs, err := Verify(ctx, e.st.R, e.data, false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		lt.TrackTwinA + "/" + KindFLAC:   "error rendition_hash_mismatch",
		lt.TrackTwinB + "/" + KindFLAC:   "error rendition_tag_leak",
		lt.TrackTwinB + "/" + KindMP3:    "warning rendition_missing",
		lt.TrackRefFlac + "/" + KindFLAC: "error rendition_audio_mismatch",
		lt.TrackRefFlac + "/" + KindMP3:  "error rendition_path",
	}
	got := map[string]string{}
	for _, f := range fs {
		got[f.Subject] = f.Severity + " " + f.Code
		if f.Detail == "" {
			t.Errorf("%s has no detail", f.Subject)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("findings %v", codes(fs))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
	// report-only: nothing removed
	for _, r := range []Rendition{a, b} {
		if _, err := e.data.Stat(r.Path); err != nil {
			t.Fatalf("report-only verify removed %s", r.Path)
		}
	}
	if _, err := Verify(ctx, e.st.R, e.data, true); err != nil {
		t.Fatal(err)
	}
	for _, r := range []Rendition{a, b, built[lt.TrackRefFlac+KindFLAC]} {
		if _, err := e.data.Stat(r.Path); err == nil {
			t.Errorf("a bad file survived verify: %s", r.Path)
		}
	}
	if _, err := e.data.Stat("audio/masters/x.flac"); err == nil {
		t.Errorf("unexpected file")
	}
	for id, mb := range masters { // verify never writes a master
		if got := e.read(t, e.taints[id].MasterPath); !bytes.Equal(got, mb) {
			t.Errorf("master %s changed", id)
		}
	}
	// restore: the rendition directory is empty, the first request rebuilds and verify is clean
	if _, err := e.st.W.Exec(`UPDATE tracks SET audio_md5 = ? WHERE id = ?`, hexOf(e.taints[lt.TrackRefFlac].MD5), lt.TrackRefFlac); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.Exec(`UPDATE renditions SET path = ? WHERE track_id = ? AND kind = ?`, built[lt.TrackRefFlac+KindMP3].Path, lt.TrackRefFlac, KindMP3); err != nil {
		t.Fatal(err)
	}
	for _, r := range built {
		_ = e.data.Remove(r.Path)
	}
	before := e.svc.Builds()
	for _, id := range []string{lt.TrackTwinA, lt.TrackTwinB, lt.TrackRefFlac} {
		for _, k := range []string{KindFLAC, KindMP3} {
			if _, err := e.svc.Ensure(ctx, id, k); err != nil {
				t.Fatalf("rebuild %s %s: %v", id, k, err)
			}
		}
	}
	if _, err := e.svc.Ensure(ctx, lt.TrackLossy, KindMP3); err != nil {
		t.Fatal(err)
	}
	if e.svc.Builds()-before != 7 {
		t.Errorf("rebuilt %d, want 7", e.svc.Builds()-before)
	}
	if fs, err := Verify(ctx, e.st.R, e.data, true); err != nil || len(fs) != 0 {
		t.Fatalf("after the restore: %v %v", codes(fs), err)
	}
	// a cancelled context stops the walk
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Verify(cctx, e.st.R, e.data, true); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

func hexOf(b [16]byte) string {
	const d = "0123456789abcdef"
	out := make([]byte, 32)
	for i, c := range b {
		out[2*i], out[2*i+1] = d[c>>4], d[c&15]
	}
	return string(out)
}
