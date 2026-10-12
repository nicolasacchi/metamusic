package flacmeta_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"runtime"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
)

func read(t *testing.T, b []byte) (flacmeta.Info, error) {
	t.Helper()
	return flacmeta.Read(bytes.NewReader(b), int64(len(b)))
}

func TestRead_StreamInfo(t *testing.T) {
	md5 := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	info, err := read(t, flactest.Build(flactest.Spec{SampleRate: 48000, Channels: 2, BitsPerSample: 24, TotalSamples: 288000, MD5: md5}))
	if err != nil {
		t.Fatal(err)
	}
	si := info.StreamInfo
	if si.SampleRate != 48000 || si.Channels != 2 || si.BitsPerSample != 24 || si.TotalSamples != 288000 || si.MD5 != md5 {
		t.Fatalf("%+v", si)
	}
	if info.Duration() != 6.0 {
		t.Fatalf("duration %v", info.Duration())
	}
}

func TestRead_BlocksAndFramesOffset(t *testing.T) {
	frames := []byte("FRAMEBYTES")
	b := flactest.Build(flactest.Spec{
		TotalSamples: 96000, SeekPoints: 3, Vendor: "mm test", Comments: []string{"TITLE=a", "ARTIST=b", "TITLE=c"},
		PaddingBytes: 100, Frames: frames,
	})
	info, err := read(t, b)
	if err != nil {
		t.Fatal(err)
	}
	var types []flacmeta.BlockType
	for _, bl := range info.Blocks {
		types = append(types, bl.Type)
	}
	want := []flacmeta.BlockType{flacmeta.BlockStreamInfo, flacmeta.BlockSeekTable, flacmeta.BlockVorbisComment, flacmeta.BlockPadding}
	if diff := cmp.Diff(want, types); diff != "" {
		t.Fatal(diff)
	}
	if info.Blocks[0].Offset != 4 || info.Blocks[0].Length != 34 || info.Blocks[1].Offset != 4+4+34 || info.Blocks[1].Length != 54 {
		t.Fatalf("%+v", info.Blocks)
	}
	if info.FramesOffset != int64(len(b)-len(frames)) {
		t.Fatalf("frames offset %d, want %d", info.FramesOffset, len(b)-len(frames))
	}
	if info.Vendor != "mm test" {
		t.Fatalf("vendor %q", info.Vendor)
	}
	if diff := cmp.Diff([]string{"TITLE=a", "ARTIST=b", "TITLE=c"}, info.Comments); diff != "" {
		t.Fatal(diff)
	}
	if info.Count(flacmeta.BlockVorbisComment) != 1 || info.Count(flacmeta.BlockPicture) != 0 {
		t.Fatal("Count")
	}
}

func TestRead_SeekPointsSkipPlaceholders(t *testing.T) {
	info, err := read(t, flactest.Build(flactest.Spec{TotalSamples: 1, SeekPoints: 2, SeekPlaceholders: 3}))
	if err != nil {
		t.Fatal(err)
	}
	if len(info.SeekPoints) != 2 || info.SeekPoints[1].Sample != 96000 || info.SeekPoints[1].FrameSamples != 4096 {
		t.Fatalf("%+v", info.SeekPoints)
	}
}

func TestRead_Errors(t *testing.T) {
	if _, err := read(t, []byte("OggS\x00\x02rest")); !errors.Is(err, flacmeta.ErrNotFLAC) {
		t.Fatalf("ogg: %v", err)
	}
	if _, err := read(t, nil); !errors.Is(err, flacmeta.ErrNotFLAC) {
		t.Fatalf("empty: %v", err)
	}
	good := flactest.Build(flactest.Spec{TotalSamples: 1})
	for _, n := range []int{4, 6, 8, 20, 4 + 4 + 33} {
		if _, err := read(t, good[:n]); !errors.Is(err, flacmeta.ErrCorrupt) {
			t.Errorf("truncated at %d: %v", n, err)
		}
	}
}

func put24(b []byte, v int) { b[0], b[1], b[2] = byte(v>>16), byte(v>>8), byte(v) }

// withBlock returns a file with the given extra last block after STREAMINFO.
func withBlock(typ byte, data []byte, declared int) []byte {
	b := flactest.Build(flactest.Spec{TotalSamples: 1})
	b[4] &^= 0x80 // STREAMINFO no longer last
	hdr := []byte{typ | 0x80, 0, 0, 0}
	put24(hdr[1:], declared)
	b = append(b, hdr...)
	return append(b, data...)
}

func TestRead_HostileLengths(t *testing.T) {
	le := binary.LittleEndian
	bigCount := make([]byte, 64)
	le.PutUint32(bigCount[0:], 0)
	le.PutUint32(bigCount[4:], 0xFFFFFFFF)
	bigVendor := make([]byte, 64)
	le.PutUint32(bigVendor[0:], 1<<31)
	cases := map[string][]byte{
		"comment count":    withBlock(4, bigCount, 64),
		"vendor length":    withBlock(4, bigVendor, 64),
		"block past EOF":   withBlock(1, make([]byte, 10), 5000),
		"comment too big":  withBlock(4, make([]byte, 10), 2<<20),
		"bad seektable":    withBlock(3, make([]byte, 19), 19),
		"short vorbis":     withBlock(4, make([]byte, 3), 3),
		"invalid type 127": withBlock(127, nil, 0),
	}
	var tiny []byte
	tiny = flactest.Build(flactest.Spec{TotalSamples: 1})
	tiny[4] &^= 0x80
	for i := 0; i < 2000; i++ {
		tiny = append(tiny, 1, 0, 0, 0)
	}
	cases["2000 tiny blocks"] = tiny
	notFirst := flactest.Build(flactest.Spec{TotalSamples: 1})
	notFirst[4] = 1 | 0x80
	cases["streaminfo not first"] = notFirst

	for name, b := range cases {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err := read(t, b)
		runtime.ReadMemStats(&after)
		if !errors.Is(err, flacmeta.ErrCorrupt) {
			t.Errorf("%s: err=%v", name, err)
		}
		if g := after.TotalAlloc - before.TotalAlloc; g > 1<<20 {
			t.Errorf("%s: allocated %d bytes", name, g)
		}
	}
}

func TestRead_PictureNotLoaded(t *testing.T) {
	b := flactest.Build(flactest.Spec{TotalSamples: 1, PictureBytes: 16<<20 - 1, Vendor: "v", Comments: []string{"A=b"}})
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	info, err := read(t, b)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if info.Count(flacmeta.BlockPicture) != 1 {
		t.Fatal("picture block not listed")
	}
	if g := after.TotalAlloc - before.TotalAlloc; g > 1<<20 {
		t.Fatalf("allocated %d bytes", g)
	}
}

func TestFramesSHA256(t *testing.T) {
	frames := []byte("some frame bytes")
	b := flactest.Build(flactest.Spec{TotalSamples: 1, Frames: frames, Comments: []string{"X=y"}})
	info, _ := read(t, b)
	got, err := flacmeta.FramesSHA256(bytes.NewReader(b), int64(len(b)), info.FramesOffset)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(frames)
	if got != hex.EncodeToString(sum[:]) {
		t.Fatalf("got %s", got)
	}
	// tag changes leave the frames hash unchanged
	b2 := flactest.Build(flactest.Spec{TotalSamples: 1, Frames: frames, Comments: []string{"X=other", "Y=z"}})
	info2, _ := read(t, b2)
	got2, _ := flacmeta.FramesSHA256(bytes.NewReader(b2), int64(len(b2)), info2.FramesOffset)
	if got != got2 {
		t.Fatal("frames hash depends on tags")
	}
	if _, err := flacmeta.FramesSHA256(bytes.NewReader(b), int64(len(b)), int64(len(b))+1); err == nil {
		t.Fatal("offset past end must fail")
	}
}

func TestAudioID(t *testing.T) {
	if got := flacmeta.AudioID("abc", 288000); got != "abc:288000" {
		t.Fatal(got)
	}
}
