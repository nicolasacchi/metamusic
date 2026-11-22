package app

import (
	"context"
	"os"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestWireNightShift: a full build wires the night runner with its two loops and the shift OFF (nothing runs until the Owner enables it
// in the app); a Services without the spend path wires nothing.
func TestWireNightShift(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	cfg := config.Config{AuthMode: "local", PublicURL: "http://mm.example.com", RateScale: 100, DataDir: dir}
	s, err := Build(context.Background(), cfg, config.Secrets{Key: make([]byte, 32)}, st, root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.NightShift == nil {
		t.Fatal("a full build does not wire the night shift")
	}
	got := map[string]bool{}
	for _, l := range s.Loops {
		got[l.Name] = l.Run != nil
	}
	if !got["night-shift"] || !got["night-shift-resume"] {
		t.Errorf("loops = %v", got)
	}
	if set, err := s.NightShift.Settings(context.Background()); err != nil || set.Enabled || set.Halted() {
		t.Errorf("settings = %+v %v, want OFF", set, err)
	}
	if at, enabled, err := s.NightShift.At(context.Background()); err != nil || enabled || at != "01:30" {
		t.Errorf("At = %q %v %v", at, enabled, err)
	}
	empty := &BuildEnv{Services: &Services{}}
	if err := wireNightShift(empty); err != nil || empty.Services.NightShift != nil || len(empty.Services.Loops) != 0 {
		t.Errorf("without the spend path: %v, %v, %d loops", err, empty.Services.NightShift, len(empty.Services.Loops))
	}
}
