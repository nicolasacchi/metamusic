package earstest_test

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/ears/earstest"
	"github.com/nicolasacchi/metamusic/internal/ladder"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	_ ears.TierA     = (*earstest.TierA)(nil)
	_ ears.Judgments = (*earstest.Judgments)(nil)
)

func TestMain(m *testing.M) {
	store.UseMigratedTemplateForTests()
	os.Exit(m.Run())
}

func TestFixture_Rows(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	fx := lt.Fixture(t, st, lt.FixedNow)
	e := earstest.Fixture(t, st, root, fx, lt.FixedNow)
	count := func(q string) (n int) {
		if err := st.R.QueryRowContext(context.Background(), q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for q, want := range map[string]int{
		`SELECT count(*) FROM analyses WHERE tier = 'A'`:               5,
		`SELECT count(*) FROM analyses WHERE analyzer = 'token_loops'`: 1,
		`SELECT count(*) FROM renditions WHERE kind = 'spectrogram'`:   2,
		`SELECT count(*) FROM artifacts WHERE name = 'semantic.npy'`:   1,
	} {
		if got := count(q); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "derived", e.SpectroSHA[:2], e.SpectroSHA+".spectro.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(raw)) != e.SpectroBytes || len(raw) >= 100000 {
		t.Errorf("jpeg is %d bytes (recorded %d), want < 100000", len(raw), e.SpectroBytes)
	}
	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil || img.Bounds().Dx() != 1308 || img.Bounds().Dy() != 560 {
		t.Errorf("jpeg = %v, %v", img, err)
	}
}

func TestTierA_PictureGeometry(t *testing.T) {
	t.Parallel()
	a := earstest.NewTierA()
	out := filepath.Join(t.TempDir(), "s.png")
	if err := a.SpectrumPicture(context.Background(), "", out, 1024, 384); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(out)
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	// C50.2: (W + 2*x0) x (H + 128) with x0 = int((log10(sr)+1)*25) = 142 at 48 kHz
	if want := (image.Point{1024 + 2*142, 384 + 128}); cfg.Width != want.X || cfg.Height != want.Y {
		t.Errorf("png = %dx%d, want %dx%d", cfg.Width, cfg.Height, want.X, want.Y)
	}
	if earstest.PlotX0(44100) != 141 || earstest.PlotX0(48000) != 142 {
		t.Errorf("x0 = %d / %d", earstest.PlotX0(44100), earstest.PlotX0(48000))
	}
	s, c, err := a.Signal(context.Background(), "", 48000, 12)
	if err != nil || s.DurationS != 12 || c.ClippedFrames != 0 {
		t.Errorf("signal = %+v %+v %v", s, c, err)
	}
	if sg, pic := a.Calls(); sg != 1 || pic != 1 {
		t.Errorf("calls = %d, %d", sg, pic)
	}
}

func TestJudgments_Maps(t *testing.T) {
	t.Parallel()
	j := &earstest.Judgments{Pairs: map[[2]string]ladder.PairStats{{"a", "b"}: {HumanBlindN: 2}},
		Strength: map[string]ladder.Strength{"a": {U: 1, SD: 0.5, NDuels: 3}}, Nu: 0.4}
	ps, _ := j.PairStats(context.Background(), "a", "b")
	st, nu, _ := j.Strengths(context.Background(), []string{"a", "zz"})
	if ps.HumanBlindN != 2 || len(st) != 1 || st["a"].NDuels != 3 || nu != 0.4 {
		t.Errorf("%+v %+v %v", ps, st, nu)
	}
}
