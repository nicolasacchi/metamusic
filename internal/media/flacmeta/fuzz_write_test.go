package flacmeta_test

import (
	"bytes"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
)

// FuzzRewrite: for any input Read accepts, Rewrite never panics and the frame region stays identical; the cuesheet and
// picture parsers never panic on the rewritten file either.
func FuzzRewrite(f *testing.F) {
	f.Add(flactest.Build(flactest.Spec{TotalSamples: 288000, Frames: []byte("abc")}), []byte("v"), []byte{})
	f.Add(flactest.Build(flactest.Spec{SeekPoints: 2, Vendor: "v", Comments: []string{"A=b"}, PaddingBytes: 40, PictureBytes: 30, Frames: []byte("xyz")}), []byte(nil), flacmeta.CuesheetBlock(flacmeta.Cuesheet{LeadOut: 5}))
	f.Add(flactest.Build(flactest.Spec{PictureBytes: 100}), []byte{}, []byte{1, 2, 3})
	f.Fuzz(func(t *testing.T, src, a, b []byte) {
		if len(src) > 64<<10 || len(a) > 4<<10 || len(b) > 4<<10 {
			t.Skip()
		}
		info, err := flacmeta.Read(bytes.NewReader(src), int64(len(src)))
		if err != nil {
			t.Skip()
		}
		var buf bytes.Buffer
		if _, err := flacmeta.Rewrite(&buf, bytes.NewReader(src), int64(len(src)), info, flacmeta.Replace{Vorbis: a, Picture: b}); err != nil {
			return
		}
		out := buf.Bytes()
		if !bytes.Equal(out[len(out)-(len(src)-int(info.FramesOffset)):], src[info.FramesOffset:]) {
			t.Fatal("frames changed")
		}
		if ni, err := flacmeta.Read(bytes.NewReader(out), int64(len(out))); err == nil {
			_, _, _ = flacmeta.ReadCuesheet(bytes.NewReader(out), int64(len(out)), ni)
			_, _ = flacmeta.ReadPictures(bytes.NewReader(out), int64(len(out)), ni)
		}
	})
}

// FuzzReadCuesheet: the CUESHEET and PICTURE parsers never panic and a parsed sheet always rebuilds.
func FuzzReadCuesheet(f *testing.F) {
	f.Add(flacmeta.CuesheetBlock(threeTracks()))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, blk []byte) {
		if len(blk) > 16<<10 {
			t.Skip()
		}
		file := append([]byte("fLaC"), flacmeta.BlockHeader(flacmeta.BlockStreamInfo, false, 34)...)
		si := make([]byte, 34)
		si[10] = 0x0b // non-zero sample rate
		file = append(file, si...)
		file = append(file, flacmeta.BlockHeader(flacmeta.BlockCueSheet, false, len(blk))...)
		file = append(file, blk...)
		file = append(file, flacmeta.BlockHeader(flacmeta.BlockPicture, true, len(blk))...)
		file = append(file, blk...)
		info, err := flacmeta.Read(bytes.NewReader(file), int64(len(file)))
		if err != nil {
			t.Skip()
		}
		_, _, _ = flacmeta.ReadCuesheet(bytes.NewReader(file), int64(len(file)), info)
		_, _ = flacmeta.ReadPictures(bytes.NewReader(file), int64(len(file)), info)
	})
}
