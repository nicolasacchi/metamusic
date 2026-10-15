package serve

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMediaHandler_PNG: a signed png link answers 200 image/png, an unsigned one 403.
func TestMediaHandler_PNG(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	body := buf.Bytes()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "c.png"), body, 0o444); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	key := strings.Repeat("c0", 32)
	res := stubResolver{key + ".png": {RelPath: "c.png", ContentType: "image/png", ETag: key, ModTime: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}}
	s := NewSigner(testKey, fixedClock(handlerNow))
	r := &rig{h: Handler(s, root, res, nil), signer: s, data: body, sha: key, root: dir}

	w := r.do("GET", r.url(key, "png"), nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Content-Disposition") != "" || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("png: %d %v", w.Code, w.Header())
	}
	assertProblem(t, r.do("GET", "/media/"+key+".png", nil), 403, "MEDIA_LINK_INVALID")
	// a link signed for the flac extension does not open the png
	assertProblem(t, r.do("GET", strings.Replace(r.url(key, "flac"), ".flac", ".png", 1), nil), 403, "MEDIA_LINK_INVALID")
}
