package flacmeta_test

import (
	"bytes"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta/flactest"
)

// FuzzRead: Read never panics. Seeds are added at runtime (no committed corpus blobs).
func FuzzRead(f *testing.F) {
	f.Add(flactest.Build(flactest.Spec{TotalSamples: 288000}))
	f.Add(flactest.Build(flactest.Spec{TotalSamples: 1, SeekPoints: 3, SeekPlaceholders: 1, Vendor: "v", Comments: []string{"A=b", "C=d"}, PaddingBytes: 40, Frames: []byte("xyz")}))
	f.Add(flactest.Build(flactest.Spec{PictureBytes: 100}))
	f.Add([]byte("fLaC"))
	f.Add([]byte("OggS"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 64<<10 {
			t.Skip()
		}
		info, err := flacmeta.Read(bytes.NewReader(b), int64(len(b)))
		if err == nil && (info.FramesOffset < 4 || info.FramesOffset > int64(len(b))) {
			t.Fatalf("frames offset %d out of range for %d bytes", info.FramesOffset, len(b))
		}
	})
}
