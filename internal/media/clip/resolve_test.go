package clip_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/clip/cliptest"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

func TestResolveClip(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s := clip.New(e.root, e.dir, mediatool.Tools{}, cliptest.NewAnalyzer(), nil, nil, clip.Options{Render: cliptest.Render})
	o, err := s.Ensure(context.Background(), e.fakeMaster(t, "res"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Resolve(context.Background(), o.Key, "flac")
	if err != nil {
		t.Fatal(err)
	}
	if got.RelPath != o.RelPath || got.ContentType != "audio/flac" || got.ETag != o.Key || got.ModTime.IsZero() {
		t.Errorf("object = %+v", got)
	}
	for name, c := range map[string][2]string{
		"unknown key":  {strings.Repeat("ab", 32), "flac"},
		"mp3":          {o.Key, "mp3"},
		"jpg":          {o.Key, "jpg"},
		"short":        {o.Key[:10], "flac"},
		"upper":        {strings.ToUpper(o.Key), "flac"},
		"traversal":    {"../../../../etc/passwd", "flac"},
		"slash in key": {strings.Repeat("a", 61) + "/..", "flac"},
	} {
		if _, err := s.Resolve(context.Background(), c[0], c[1]); !errors.Is(err, serve.ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}
}
