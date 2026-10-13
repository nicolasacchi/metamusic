package cliptest_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/clip/cliptest"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
)

var (
	_ duels.ClipRenderer  = (*cliptest.Renderer)(nil)
	_ clip.WindowAnalyzer = (*cliptest.Analyzer)(nil)
	_ clip.RenderFunc     = cliptest.Render
)

func spec() clip.Spec {
	return clip.Spec{MasterSHA256: "ab", SampleRate: 44100, Channels: 2, W: clip.Window{StartMS: 1000, EndMS: 31000}}
}

func TestRenderer_MeasureAndEnsure(t *testing.T) {
	t.Parallel()
	r := cliptest.NewRenderer()
	r.SetMeasure("ab", clip.Measure{IntegratedLUFS: -12.5, TruePeakDBTP: -0.5})
	ctx := context.Background()
	m, _ := r.Measure(ctx, spec())
	if m.IntegratedLUFS != -12.5 {
		t.Errorf("listed measure = %+v", m)
	}
	other := spec()
	other.MasterSHA256 = "cd"
	if m, _ := r.Measure(ctx, other); m != cliptest.DefaultMeasure {
		t.Errorf("default measure = %+v", m)
	}
	o, err := r.Ensure(ctx, spec())
	if err != nil {
		t.Fatal(err)
	}
	if o.Key != clip.Key(spec(), cliptest.Tool) || o.DurationS != 30 || o.Channels != 2 || o.RelPath != "derived/clips/"+o.Key[:2]+"/"+o.Key+".flac" {
		t.Errorf("object = %+v", o)
	}
	if me, en := r.Calls(); me != 2 || en != 1 {
		t.Errorf("calls = %d, %d", me, en)
	}
}

func TestRender_WritesStreamInfo(t *testing.T) {
	t.Parallel()
	out := filepath.Join(t.TempDir(), "sub", "clip.flac")
	if err := cliptest.Render(context.Background(), "", out, spec()); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	st, _ := f.Stat()
	info, err := flacmeta.Read(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	si := info.StreamInfo
	if si.SampleRate != 48000 || si.BitsPerSample != 24 || si.Channels != 2 || si.TotalSamples != 30*48000 {
		t.Errorf("streaminfo = %+v", si)
	}
}

func TestAnalyzer_KeysByMasterContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "master.flac")
	if err := os.WriteFile(p, []byte("some master"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := cliptest.NewAnalyzer()
	if m, err := a.WindowLoudness(context.Background(), p, 0, 1); err != nil || m != cliptest.DefaultMeasure {
		t.Fatalf("default = %+v, %v", m, err)
	}
	a.SetMeasure(library.SHA256Hex([]byte("some master")), clip.Measure{IntegratedLUFS: -9})
	if m, _ := a.WindowLoudness(context.Background(), p, 0, 1); m.IntegratedLUFS != -9 {
		t.Errorf("listed = %+v", m)
	}
	if _, err := a.WindowLoudness(context.Background(), filepath.Join(dir, "nope"), 0, 1); err == nil {
		t.Error("a missing work file must fail")
	}
}
