package flacmeta_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
)

func TestBlockHeader(t *testing.T) {
	t.Parallel()
	if got := flacmeta.BlockHeader(flacmeta.BlockPicture, true, 0x010203); !bytes.Equal(got, []byte{0x86, 1, 2, 3}) {
		t.Fatalf("% x", got)
	}
	if got := flacmeta.BlockHeader(flacmeta.BlockSeekTable, false, 54); !bytes.Equal(got, []byte{3, 0, 0, 54}) {
		t.Fatalf("% x", got)
	}
}

func TestVorbisComment_Escapes(t *testing.T) {
	t.Parallel()
	c := []flacmeta.Comment{
		{Key: "TITLE", Value: "Zażółć gęślą · 夜"},
		{Key: "DESCRIPTION", Value: "a=b=c"},
		{Key: "EMPTY", Value: ""},
	}
	if err := flacmeta.ValidateComments(c); err != nil {
		t.Fatal(err)
	}
	b := flacmeta.VorbisComment("v1", c)
	// round trip through the reader: the payload parses back to exactly the entries
	file := append([]byte("fLaC"), flacmeta.BlockHeader(flacmeta.BlockStreamInfo, false, 34)...)
	si := make([]byte, 34)
	binary.BigEndian.PutUint64(si[10:], 48000<<44|1<<41|23<<36)
	file = append(file, si...)
	file = append(file, flacmeta.BlockHeader(flacmeta.BlockVorbisComment, true, len(b))...)
	file = append(file, b...)
	info, err := flacmeta.Read(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"TITLE=Zażółć gęślą · 夜", "DESCRIPTION=a=b=c", "EMPTY="}
	if diff := cmp.Diff(want, info.Comments); diff != "" || info.Vendor != "v1" {
		t.Fatal(diff, info.Vendor)
	}
	for _, bad := range []flacmeta.Comment{{Key: ""}, {Key: "A=B"}, {Key: "tab\t"}, {Key: "é"}, {Key: "K~"}, {Key: "K", Value: "a\xffb"}, {Key: "K", Value: "a\x00b"}} {
		if flacmeta.ValidateComments([]flacmeta.Comment{bad}) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPictureBlock_Golden(t *testing.T) {
	t.Parallel()
	got := flacmeta.PictureBlock(flacmeta.Picture{Type: 3, MIME: "image/png", Desc: "d", W: 512, H: 256, Depth: 8, Colors: 16, Data: []byte{9, 8, 7}})
	want := []byte{
		0, 0, 0, 3, 0, 0, 0, 9, 'i', 'm', 'a', 'g', 'e', '/', 'p', 'n', 'g', 0, 0, 0, 1, 'd',
		0, 0, 2, 0, 0, 0, 1, 0, 0, 0, 0, 8, 0, 0, 0, 16, 0, 0, 0, 3, 9, 8, 7,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("% x", got)
	}
}

// cueTrackBytes spells one CUESHEET track entry out byte by byte (offset, number, 12 ISRC, flags, 13 reserved, index count).
func cueTrackBytes(off uint64, num byte, idx ...uint64) []byte {
	b := binary.BigEndian.AppendUint64(nil, off)
	b = append(b, num)
	b = append(b, make([]byte, 12)...)
	b = append(b, 0)
	b = append(b, make([]byte, 13)...)
	b = append(b, byte(len(idx)))
	for i, o := range idx {
		b = binary.BigEndian.AppendUint64(b, o)
		b = append(b, byte(i+1), 0, 0, 0)
	}
	return b
}

func threeTracks() flacmeta.Cuesheet {
	one := []flacmeta.CueIndex{{Offset: 0, Number: 1}}
	return flacmeta.Cuesheet{
		Tracks: []flacmeta.CueTrack{
			{Offset: 0, Number: 1, Indexes: one}, {Offset: 48000, Number: 2, Indexes: one}, {Offset: 144000, Number: 3, Indexes: one},
		},
		LeadOut: 288000,
	}
}

func TestCuesheet_Block_Golden(t *testing.T) {
	t.Parallel()
	c := threeTracks()
	if err := flacmeta.ValidateCuesheet(c); err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 128+8+1+258)
	want = append(want, 4) // three tracks plus the lead-out
	want = append(want, cueTrackBytes(0, 1, 0)...)
	want = append(want, cueTrackBytes(48000, 2, 0)...)
	want = append(want, cueTrackBytes(144000, 3, 0)...)
	want = append(want, cueTrackBytes(288000, 255)...)
	got := flacmeta.CuesheetBlock(c)
	if !bytes.Equal(got, want) || len(got) != 576 {
		t.Fatalf("cuesheet block differs (%d bytes)", len(got))
	}
	// the chapter milliseconds the Vorbis form carries for the same offsets
	var ms []uint64
	for _, tr := range c.Tracks {
		ms = append(ms, tr.Offset*1000/48000)
	}
	if diff := cmp.Diff([]uint64{0, 1000, 3000}, ms); diff != "" {
		t.Fatal(diff)
	}
}

func TestCuesheet_Validate(t *testing.T) {
	t.Parallel()
	ok := threeTracks()
	cases := map[string]func(*flacmeta.Cuesheet){
		"no tracks":       func(c *flacmeta.Cuesheet) { c.Tracks = nil },
		"zero number":     func(c *flacmeta.Cuesheet) { c.Tracks[0].Number = 0 },
		"lead-out number": func(c *flacmeta.Cuesheet) { c.Tracks[2].Number = 255 },
		"number order":    func(c *flacmeta.Cuesheet) { c.Tracks[1].Number = 1 },
		"offset order":    func(c *flacmeta.Cuesheet) { c.Tracks[1].Offset = 200000 },
		"past lead-out":   func(c *flacmeta.Cuesheet) { c.LeadOut = 100 },
		"no index":        func(c *flacmeta.Cuesheet) { c.Tracks[0].Indexes = nil },
	}
	for name, mut := range cases {
		c := threeTracks()
		c.Tracks = append([]flacmeta.CueTrack(nil), c.Tracks...)
		mut(&c)
		if flacmeta.ValidateCuesheet(c) == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := flacmeta.ValidateCuesheet(ok); err != nil {
		t.Fatal(err)
	}
}

func TestFlacmeta_Rewrite_Golden(t *testing.T) {
	t.Parallel()
	frames := []byte("\xff\xf8frame-one\xff\xf8frame-two")
	src := flactest.Build(flactest.Spec{
		TotalSamples: 288000, MD5: [16]byte{9, 8, 7}, Vendor: "old", Comments: []string{"TITLE=old"},
		SeekPoints: 3, SeekPlaceholders: 1, PaddingBytes: 64, PictureBytes: 50, Frames: frames,
	})
	oldInfo, err := flacmeta.Read(bytes.NewReader(src), int64(len(src)))
	if err != nil {
		t.Fatal(err)
	}
	vc := flacmeta.VorbisComment("new vendor", []flacmeta.Comment{{Key: "TITLE", Value: "New"}, {Key: "CHAPTER001", Value: "00:00:00.000"}})
	cue := flacmeta.CuesheetBlock(threeTracks())
	pic := flacmeta.PictureBlock(flacmeta.Picture{Type: 3, MIME: "image/png", W: 2, H: 2, Depth: 8, Colors: 16, Data: []byte("png")})
	var buf bytes.Buffer
	n, err := flacmeta.Rewrite(&buf, bytes.NewReader(src), int64(len(src)), oldInfo, flacmeta.Replace{Vorbis: vc, Cuesheet: cue, Picture: pic})
	if err != nil || n != int64(buf.Len()) {
		t.Fatal(n, buf.Len(), err)
	}
	out := buf.Bytes()
	info, err := flacmeta.Read(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatal(err)
	}
	var types []flacmeta.BlockType
	for _, b := range info.Blocks {
		types = append(types, b.Type)
	}
	want := []flacmeta.BlockType{flacmeta.BlockStreamInfo, flacmeta.BlockSeekTable, flacmeta.BlockVorbisComment, flacmeta.BlockCueSheet, flacmeta.BlockPicture}
	if diff := cmp.Diff(want, types); diff != "" {
		t.Fatal(diff)
	}
	// only the last block carries the flag
	for i, b := range info.Blocks {
		if last := out[b.Offset]&0x80 != 0; last != (i == len(info.Blocks)-1) {
			t.Fatalf("block %d last flag %v", i, last)
		}
	}
	// STREAMINFO and SEEKTABLE verbatim, frames identical
	for _, i := range []int{0, 1} {
		a, b := oldInfo.Blocks[i], info.Blocks[i]
		if !bytes.Equal(src[a.Offset+4:a.Offset+4+int64(a.Length)], out[b.Offset+4:b.Offset+4+int64(b.Length)]) {
			t.Fatalf("block %d changed", i)
		}
	}
	if !bytes.Equal(out[info.FramesOffset:], frames) {
		t.Fatal("frames changed")
	}
	a, _ := flacmeta.FramesSHA256(bytes.NewReader(src), int64(len(src)), oldInfo.FramesOffset)
	b, _ := flacmeta.FramesSHA256(bytes.NewReader(out), int64(len(out)), info.FramesOffset)
	if a != b {
		t.Fatal("frame hash changed")
	}
	if info.Vendor != "new vendor" || len(info.Comments) != 2 || info.Comments[0] != "TITLE=New" {
		t.Fatalf("%q %q", info.Vendor, info.Comments)
	}
	c, ok, err := flacmeta.ReadCuesheet(bytes.NewReader(out), int64(len(out)), info)
	if err != nil || !ok || cmp.Diff(threeTracks(), c) != "" {
		t.Fatal(ok, err, cmp.Diff(threeTracks(), c))
	}
	pics, err := flacmeta.ReadPictures(bytes.NewReader(out), int64(len(out)), info)
	if err != nil || len(pics) != 1 || pics[0].MIME != "image/png" || string(pics[0].Data) != "png" || pics[0].Colors != 16 {
		t.Fatalf("%+v %v", pics, err)
	}
}

func TestRewrite_KeepAndDrop(t *testing.T) {
	t.Parallel()
	src := flactest.Build(flactest.Spec{Vendor: "old", Comments: []string{"TITLE=old"}, PictureBytes: 50, PaddingBytes: 10, Frames: []byte("F")})
	info, _ := flacmeta.Read(bytes.NewReader(src), int64(len(src)))
	run := func(r flacmeta.Replace) flacmeta.Info {
		var buf bytes.Buffer
		if _, err := flacmeta.Rewrite(&buf, bytes.NewReader(src), int64(len(src)), info, r); err != nil {
			t.Fatal(err)
		}
		i, err := flacmeta.Read(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err != nil {
			t.Fatal(err)
		}
		return i
	}
	keep := run(flacmeta.Replace{})
	if keep.Vendor != "old" || keep.Count(flacmeta.BlockPicture) != 1 || keep.Count(flacmeta.BlockPadding) != 0 {
		t.Fatalf("keep: %+v", keep.Blocks)
	}
	drop := run(flacmeta.Replace{Picture: []byte{}})
	if drop.Count(flacmeta.BlockPicture) != 0 || drop.Count(flacmeta.BlockVorbisComment) != 1 {
		t.Fatalf("drop: %+v", drop.Blocks)
	}
	bare := run(flacmeta.Replace{Vorbis: []byte{}, Picture: []byte{}})
	if len(bare.Blocks) != 1 {
		t.Fatalf("bare: %+v", bare.Blocks)
	}
	var buf bytes.Buffer
	if _, err := flacmeta.Rewrite(&buf, bytes.NewReader(src), int64(len(src)), info, flacmeta.Replace{Picture: make([]byte, flacmeta.MaxBlockLen+1)}); err == nil {
		t.Fatal("oversized block accepted")
	}
	if _, err := flacmeta.Rewrite(&buf, bytes.NewReader(src), int64(len(src)), flacmeta.Info{}, flacmeta.Replace{}); err == nil {
		t.Fatal("no STREAMINFO accepted")
	}
}
