package tagged_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
	"github.com/nicolasacchi/metamusic/internal/media/tagged"
)

// testPNG is a 16-colour paletted PNG of the given size.
func testPNG(t testing.TB, size int) []byte {
	t.Helper()
	pal := make(color.Palette, 16)
	for i := range pal {
		pal[i] = color.RGBA{uint8(i * 16), 0, 0, 255}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewPaletted(image.Rect(0, 0, size, size), pal)); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type block struct {
	typ  byte
	last bool
	data []byte
}

// splitBlocks walks the metadata blocks of a FLAC stream and returns them with the offset of the frames.
func splitBlocks(t testing.TB, b []byte) ([]block, int) {
	t.Helper()
	if string(b[:4]) != "fLaC" {
		t.Fatal("no magic")
	}
	off := 4
	var out []block
	for {
		h := b[off]
		n := int(b[off+1])<<16 | int(b[off+2])<<8 | int(b[off+3])
		out = append(out, block{typ: h & 0x7f, last: h&0x80 != 0, data: b[off+4 : off+4+n]})
		off += 4 + n
		if h&0x80 != 0 {
			return out, off
		}
	}
}

func fullTags() tagged.Tags {
	return tagged.Tags{
		Title: "Song", Artist: "metamusic", Album: "Root", Date: "2026-10-01", Genre: "dream pop",
		TrackNumber: 2, TrackTotal: 3, ReplayGainTrackGain: "-1.50 dB", ReplayGainTrackPeak: "0.900000",
		MM: [][2]string{{"MM_TAKE_ID", "t1"}, {"MM_RECIPE_ID", "r1"}, {"MM_REQUEST_ID", "q1"}, {"MM_SEED", "7"}, {"MM_STATUS", "keeper"}},
	}
}

func masterSpec() flactest.Spec {
	return flactest.Spec{
		TotalSamples: 96000, MD5: [16]byte{9, 8, 7}, Vendor: "old vendor", SeekPoints: 3, SeekPlaceholders: 1,
		Comments: []string{"TITLE=old", "REPLAYGAIN_TRACK_GAIN=-9 dB"}, PaddingBytes: 64, PictureBytes: 50,
		Frames: []byte("\xff\xf8frame-one\xff\xf8frame-two"),
	}
}

func readAll(t testing.TB, spec flactest.Spec, tags tagged.Tags, cover []byte) (out []byte, n int64) {
	t.Helper()
	m := flactest.Build(spec)
	info, err := flacmeta.Read(bytes.NewReader(m), int64(len(m)))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	n, err = tagged.Write(&buf, bytes.NewReader(m), int64(len(m)), info, tags, cover)
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), n
}

func TestWrite_Layout(t *testing.T) {
	t.Parallel()
	spec := masterSpec()
	m := flactest.Build(spec)
	minfo, _ := flacmeta.Read(bytes.NewReader(m), int64(len(m)))
	cover := testPNG(t, 8)
	out, n := readAll(t, spec, fullTags(), cover)
	if n != int64(len(out)) {
		t.Fatalf("returned %d, wrote %d", n, len(out))
	}
	blocks, frames := splitBlocks(t, out)
	var types []byte
	for _, b := range blocks {
		types = append(types, b.typ)
	}
	if want := []byte{0, 3, 4, 6}; !bytes.Equal(types, want) {
		t.Fatalf("block types %v, want %v (no PADDING, one VORBIS_COMMENT)", types, want)
	}
	for i, b := range blocks {
		if b.last != (i == len(blocks)-1) {
			t.Errorf("block %d last flag %v", i, b.last)
		}
	}
	// STREAMINFO and SEEKTABLE verbatim
	for i, bt := range []flacmeta.BlockType{flacmeta.BlockStreamInfo, flacmeta.BlockSeekTable} {
		var src flacmeta.Block
		for _, b := range minfo.Blocks {
			if b.Type == bt {
				src = b
			}
		}
		if !bytes.Equal(blocks[i].data, m[src.Offset+4:src.Offset+4+int64(src.Length)]) {
			t.Errorf("%v not copied verbatim", bt)
		}
	}
	// frames byte for byte
	if !bytes.Equal(out[frames:], m[minfo.FramesOffset:]) {
		t.Error("frames differ")
	}
	// re-read with flacmeta
	info, err := flacmeta.Read(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatal(err)
	}
	if info.Count(flacmeta.BlockVorbisComment) != 1 || info.Count(flacmeta.BlockPicture) != 1 || info.Count(flacmeta.BlockPadding) != 0 {
		t.Errorf("blocks %+v", info.Blocks)
	}
	if info.Vendor != "metamusic "+tagged.Version || info.FramesOffset != int64(frames) || info.StreamInfo.MD5 != spec.MD5 {
		t.Errorf("vendor %q frames %d md5 %x", info.Vendor, info.FramesOffset, info.StreamInfo.MD5)
	}
	want := []string{"TITLE=Song", "ARTIST=metamusic", "ALBUM=Root", "TRACKNUMBER=2", "TRACKTOTAL=3", "DATE=2026-10-01", "GENRE=dream pop",
		"REPLAYGAIN_TRACK_GAIN=-1.50 dB", "REPLAYGAIN_TRACK_PEAK=0.900000",
		"MM_TAKE_ID=t1", "MM_RECIPE_ID=r1", "MM_REQUEST_ID=q1", "MM_SEED=7", "MM_STATUS=keeper"}
	if strings.Join(info.Comments, "|") != strings.Join(want, "|") {
		t.Errorf("comments\n got %q\nwant %q", info.Comments, want)
	}
	// PICTURE: type 3, image/png, 8x8, depth 8, 16 colours
	pic := blocks[3].data
	if binary.BigEndian.Uint32(pic) != 3 {
		t.Error("picture type")
	}
	ml := int(binary.BigEndian.Uint32(pic[4:]))
	if string(pic[8:8+ml]) != "image/png" {
		t.Errorf("mime %q", pic[8:8+ml])
	}
	p := 8 + ml
	if dl := binary.BigEndian.Uint32(pic[p:]); dl != 0 {
		t.Error("description must be empty")
	}
	p += 4
	w, h, depth, colours, dlen := binary.BigEndian.Uint32(pic[p:]), binary.BigEndian.Uint32(pic[p+4:]), binary.BigEndian.Uint32(pic[p+8:]),
		binary.BigEndian.Uint32(pic[p+12:]), binary.BigEndian.Uint32(pic[p+16:])
	if w != 8 || h != 8 || depth != 8 || colours != 16 || !bytes.Equal(pic[p+20:], cover) || int(dlen) != len(cover) {
		t.Errorf("picture %d %d %d %d %d", w, h, depth, colours, dlen)
	}
}

func TestWrite_OptionalPartsOmitted(t *testing.T) {
	t.Parallel()
	spec := flactest.Spec{TotalSamples: 4800, Frames: []byte("frames")} // no SEEKTABLE
	tags := tagged.Tags{Title: "T", Artist: "metamusic", Album: "A", TrackNumber: 1, TrackTotal: 1, Date: "2026-01-02",
		MM: [][2]string{{"MM_REFERENCE", "1"}}}
	out, _ := readAll(t, spec, tags, nil) // no cover
	blocks, _ := splitBlocks(t, out)
	if len(blocks) != 2 || blocks[0].typ != 0 || blocks[1].typ != 4 || !blocks[1].last {
		t.Fatalf("blocks %+v", blocks)
	}
	info, _ := flacmeta.Read(bytes.NewReader(out), int64(len(out)))
	for _, c := range info.Comments {
		if strings.HasPrefix(c, "GENRE=") || strings.HasPrefix(c, "REPLAYGAIN") || strings.HasPrefix(c, "MM_SEED") {
			t.Errorf("unexpected comment %q", c)
		}
	}
	if info.Comments[len(info.Comments)-1] != "MM_REFERENCE=1" {
		t.Errorf("comments %q", info.Comments)
	}
}

func TestWrite_SanitisedValues(t *testing.T) {
	t.Parallel()
	tags := fullTags()
	tags.Title = "a\x00b\tc\r\nd\x7fe\u0085f"
	tags.Album = strings.Repeat("é", 1000) // 2000 bytes
	tags.Genre = "bad\xffutf8"
	tags.MM = [][2]string{{"MM_TAKE_ID", "x\ny"}}
	out, _ := readAll(t, masterSpec(), tags, nil)
	info, _ := flacmeta.Read(bytes.NewReader(out), int64(len(out)))
	got := map[string]string{}
	for _, c := range info.Comments {
		k, v, _ := strings.Cut(c, "=")
		got[k] = v
	}
	if got["TITLE"] != "a b c  d e f" {
		t.Errorf("title %q", got["TITLE"])
	}
	if l := len(got["ALBUM"]); l > 1024 || l < 1020 || strings.ContainsRune(got["ALBUM"], '�') {
		t.Errorf("album %d bytes", l)
	}
	if got["GENRE"] != "bad�utf8" || got["MM_TAKE_ID"] != "x y" {
		t.Errorf("genre %q take %q", got["GENRE"], got["MM_TAKE_ID"])
	}
}

type failWriter struct{ after int }

func (f *failWriter) Write(p []byte) (int, error) {
	if f.after < len(p) {
		return 0, errors.New("disk full")
	}
	f.after -= len(p)
	return len(p), nil
}

func TestWrite_Errors(t *testing.T) {
	t.Parallel()
	m := flactest.Build(masterSpec())
	info, _ := flacmeta.Read(bytes.NewReader(m), int64(len(m)))
	rd := bytes.NewReader(m)
	var sink bytes.Buffer
	if _, err := tagged.Write(&sink, rd, int64(len(m)), flacmeta.Info{}, fullTags(), nil); err == nil {
		t.Error("empty info accepted")
	}
	bad := info
	bad.FramesOffset = int64(len(m)) + 1
	if _, err := tagged.Write(&sink, rd, int64(len(m)), bad, fullTags(), nil); err == nil {
		t.Error("frames offset past the end accepted")
	}
	if _, err := tagged.Write(&sink, rd, int64(len(m)), info, fullTags(), []byte("not a png")); err == nil {
		t.Error("garbage cover accepted")
	}
	big := fullTags()
	big.MM = nil
	for range 20000 {
		big.MM = append(big.MM, [2]string{"MM_X", strings.Repeat("v", 900)})
	}
	if _, err := tagged.Write(&sink, rd, int64(len(m)), info, big, nil); err == nil {
		t.Error("oversized VORBIS_COMMENT accepted")
	}
	// a short master: the declared size is larger than the data
	if _, err := tagged.Write(&sink, bytes.NewReader(m[:len(m)-5]), int64(len(m)), info, fullTags(), nil); err == nil {
		t.Error("truncated master accepted")
	}
	// the writer failing at every stage
	whole, _ := readAll(t, masterSpec(), fullTags(), testPNG(t, 8))
	for _, after := range []int{0, 4, 40, 100, 200, 400, len(whole) - 3} {
		if _, err := tagged.Write(&failWriter{after: after}, bytes.NewReader(m), int64(len(m)), info, fullTags(), testPNG(t, 8)); err == nil {
			t.Errorf("write failure after %d bytes not reported", after)
		}
	}
}

func TestWrite_MasterReadFailures(t *testing.T) {
	t.Parallel()
	m := flactest.Build(masterSpec())
	info, _ := flacmeta.Read(bytes.NewReader(m), int64(len(m)))
	var sink bytes.Buffer
	// STREAMINFO unreadable, then SEEKTABLE unreadable
	for _, keep := range []int{10, int(info.Blocks[1].Offset) + 6} {
		if _, err := tagged.Write(&sink, bytes.NewReader(m[:keep]), int64(len(m)), info, fullTags(), nil); err == nil {
			t.Errorf("master cut at %d accepted", keep)
		}
	}
	// a PICTURE over the block size limit: a PNG header followed by padding
	huge := append(testPNG(t, 8), make([]byte, 1<<24)...)
	if _, err := tagged.Write(&sink, bytes.NewReader(m), int64(len(m)), info, fullTags(), huge); err == nil {
		t.Error("oversized PICTURE accepted")
	}
}
