package cover_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/cover"
)

// TestStubsV1_FrozenSignatures pins the mosaic signatures of C123 and the stub answers. Lane V1 deletes stub_v1.go and this test
// and adds the renderer, the key and the goldens.
func TestStubsV1_FrozenSignatures(t *testing.T) {
	t.Parallel()
	in := cover.Input{Kind: cover.KindRecord, ID: "r1", Seed: 7, Parts: []cover.Part{{RootID: "root-1", Seed: 3}}}
	if cover.MosaicVersion != "mm-cover/record-1" || cover.KindRecord != "record" || cover.KindAlbum != "album" {
		t.Error("a mosaic constant changed")
	}
	if cover.RenderMosaic(in, 512) != nil || cover.MosaicKey(in, 512) != "" {
		t.Error("the stubs render nothing")
	}
	if _, _, err := (*cover.Service)(nil).EnsureMosaic(context.Background(), in, 512); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("EnsureMosaic: %v", err)
	}
}
