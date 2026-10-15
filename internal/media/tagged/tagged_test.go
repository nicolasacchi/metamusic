package tagged_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/tagged"
)

// TestStub_FailsClosed: until M4a-M2 the writer produces nothing and says so.
func TestStub_FailsClosed(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	n, err := tagged.Write(&out, bytes.NewReader(nil), 0, flacmeta.Info{}, tagged.Tags{}, nil)
	if !errors.Is(err, tagged.ErrNotImplemented) || n != 0 || out.Len() != 0 {
		t.Fatalf("Write = %d, %v (%d bytes written)", n, err, out.Len())
	}
	if tagged.Version != "mm-dl/1" {
		t.Errorf("version %q", tagged.Version)
	}
}
