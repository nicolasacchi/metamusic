package morningtest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/morning"
)

func side(gain int) morning.SideInput {
	return morning.SideInput{Side: morning.Side{Samples: 480000, Chapters: []morning.Chapter{
		{N: 1, TrackID: "a", Start: 0, AudioStart: 72000, End: 480000, GainCDB: gain}}}}
}

func TestFake_DeterministicAndRecords(t *testing.T) {
	f := &Fake{}
	a, err := f.BuildSide(context.Background(), side(100))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := f.BuildSide(context.Background(), side(100))
	c, _ := f.BuildSide(context.Background(), side(200))
	if a.SHA256 != b.SHA256 || a.SHA256 == c.SHA256 {
		t.Errorf("sha: %s %s %s", a.SHA256, b.SHA256, c.SHA256)
	}
	if a.RelPath != "derived/morning/"+a.SHA256[:2]+"/"+a.SHA256+".flac" || a.Bytes <= 0 || len(a.Chapters) != 1 {
		t.Errorf("result = %+v", a)
	}
	if n := len(f.Calls()); n != 3 {
		t.Errorf("calls = %d", n)
	}
}

func TestFake_WritesStandIn(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	f := &Fake{Data: root}
	for range 2 { // the second build finds the file already claimed
		r, err := f.BuildSide(context.Background(), side(0))
		if err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(filepath.Join(dir, r.RelPath))
		if err != nil || st.Mode().Perm() != 0o444 {
			t.Fatalf("stand-in: %v %v", st, err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "derived/morning"), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "derived/morning"), 0o755) })
	if _, err := f.BuildSide(context.Background(), side(7)); err == nil && os.Getuid() != 0 {
		t.Error("an unwritable data dir must fail")
	}
}

func TestFake_Failures(t *testing.T) {
	boom := errors.New("boom")
	if _, err := (&Fake{Err: boom}).BuildSide(context.Background(), side(0)); !errors.Is(err, boom) {
		t.Errorf("Err not returned: %v", err)
	}
	f := &Fake{Fail: func(in morning.SideInput) error {
		if in.Side.Chapters[0].GainCDB > 0 {
			return boom
		}
		return nil
	}}
	if _, err := f.BuildSide(context.Background(), side(1)); !errors.Is(err, boom) {
		t.Errorf("Fail not applied: %v", err)
	}
	if _, err := f.BuildSide(context.Background(), side(0)); err != nil {
		t.Errorf("Fail applied wrongly: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var be *morning.BuildError
	if _, err := f.BuildSide(ctx, side(0)); !errors.As(err, &be) || be.Code != morning.CodeInterrupted {
		t.Errorf("cancelled: %v", err)
	}
}
