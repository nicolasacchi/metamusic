package nightshift

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/clock/clocktest"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

func TestNightSettings_Validate_Table(t *testing.T) {
	t.Parallel()
	str := func(s string) *string { return &s }
	num := func(n int) *int { return &n }
	for _, tc := range []struct {
		name  string
		patch SettingsPatch
		field string // "" = valid
	}{
		{"empty patch", SettingsPatch{}, ""},
		{"at ok", SettingsPatch{At: str("01:30")}, ""},
		{"at last minute", SettingsPatch{At: str("23:59")}, ""},
		{"at hour 24", SettingsPatch{At: str("24:00")}, "at"},
		{"at minute 60", SettingsPatch{At: str("01:60")}, "at"},
		{"at no colon", SettingsPatch{At: str("0130")}, "at"},
		{"at off", SettingsPatch{At: str("off")}, "at"},
		{"takes 1", SettingsPatch{MaxTakes: num(1)}, ""},
		{"takes 12", SettingsPatch{MaxTakes: num(12)}, ""},
		{"takes 0", SettingsPatch{MaxTakes: num(0)}, "max_takes"},
		{"takes 13", SettingsPatch{MaxTakes: num(13)}, "max_takes"},
		{"explore 0", SettingsPatch{ExplorePPM: num(0)}, ""},
		{"explore max", SettingsPatch{ExplorePPM: num(500_000)}, ""},
		{"explore negative", SettingsPatch{ExplorePPM: num(-1)}, "explore_ppm"},
		{"explore above max", SettingsPatch{ExplorePPM: num(500_001)}, "explore_ppm"},
		{"stars 1", SettingsPatch{MinStars: num(1)}, ""},
		{"stars 5", SettingsPatch{MinStars: num(5)}, ""},
		{"stars 0", SettingsPatch{MinStars: num(0)}, "min_stars"},
		{"stars 6", SettingsPatch{MinStars: num(6)}, "min_stars"},
		{"checks on", SettingsPatch{ListeningChecks: str("on")}, ""},
		{"checks off", SettingsPatch{ListeningChecks: str("off")}, ""},
		{"checks auto", SettingsPatch{ListeningChecks: str("auto")}, "listening_checks"},
	} {
		err := tc.patch.Validate()
		if tc.field == "" {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		if !hasCode(err, "VALIDATION_FAILED") || !strings.HasPrefix(err.Error(), "VALIDATION_FAILED") || !strings.Contains(err.Error(), tc.field) {
			t.Errorf("%s: %v, want a VALIDATION_FAILED on %s", tc.name, err, tc.field)
		}
	}
}

// TestUpdateNightShift_Guards: only the Owner as a person changes the settings; the rest is a typed refusal and nothing is written.
func TestUpdateNightShift_Guards(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	on := true
	for _, tc := range []struct {
		p    auth.Principal
		code string
	}{{bob, "OWNER_ONLY"}, {carol, "OWNER_ONLY"}, {phone, "HUMAN_ONLY"}, {agent, "HUMAN_ONLY"}, {auth.CLI, "HUMAN_ONLY"}} {
		if _, err := g.r.UpdateSettings(t.Context(), tc.p, SettingsPatch{Enabled: &on}); !hasCode(err, tc.code) {
			t.Errorf("%s: %v, want %s", tc.p.Name, err, tc.code)
		}
	}
	if _, err := g.r.UpdateSettings(t.Context(), alice, SettingsPatch{MaxTakes: new(99)}); !hasCode(err, "VALIDATION_FAILED") {
		t.Errorf("invalid patch: %v", err)
	}
	if s, err := g.r.Settings(t.Context()); err != nil || s.Enabled || s.UpdatedBy != "system:migration" {
		t.Fatalf("settings = %+v %v, want the untouched OFF default", s, err)
	}
}

// TestUpdateNightShift_EnableClearsHalt: enabling is the only way back from a halt; other edits keep the halt and every field patches alone.
func TestUpdateNightShift_EnableClearsHalt(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	if _, err := g.r.Halt(t.Context(), alice, "no"); err != nil {
		t.Fatal(err)
	}
	at, takes, ppm, stars, lc := "02:05", 6, 250_000, 5, "off"
	s, err := g.r.UpdateSettings(t.Context(), alice, SettingsPatch{At: &at, MaxTakes: &takes, ExplorePPM: &ppm, MinStars: &stars, ListeningChecks: &lc})
	if err != nil || !s.Halted() || s.At != at || s.MaxTakes != 6 || s.ExplorePPM != ppm || s.MinStars != 5 || s.ListeningChecks != "off" || !s.Enabled {
		t.Fatalf("after edit: %+v %v", s, err)
	}
	off := false
	if s, err = g.r.UpdateSettings(t.Context(), alice, SettingsPatch{Enabled: &off}); err != nil || s.Enabled || !s.Halted() {
		t.Fatalf("disabling keeps the halt: %+v %v", s, err)
	}
	on := true
	s, err = g.r.UpdateSettings(t.Context(), alice, SettingsPatch{Enabled: &on})
	if err != nil || !s.Enabled || s.Halted() || s.HaltedBy != nil || s.UpdatedBy != "human:alice" {
		t.Fatalf("enabling: %+v %v", s, err)
	}
	if got, err := g.r.Settings(t.Context()); err != nil || got.Halted() || got.At != at {
		t.Fatalf("stored: %+v %v", got, err)
	}
}

// TestNightShift_OffByDefaultChangesNothing: a database upgraded from schema 0025 (the night shift's migration is the one step) has the
// shift OFF; with a live mandate present and the clock past 01:30 nothing runs, no row, no estimate, no batch, no audit row. The same
// loop then does run once the Owner enables it, so the check is not vacuous.
func TestNightShift_OffByDefaultChangesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "up.db")
	if err := store.CreateBefore(ctx, path, "0026_night_shift.sql"); err != nil { // the schema one migration before the night shift
		t.Fatal(err)
	}
	st, err := store.Open(ctx, path) // the deploy: this applies the night-shift migration over a schema-25 database
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	lt.FixtureM3(t, st, lt.FixedNow)
	g := rigOn(t, st)
	if v, _ := st.UserVersion(ctx); v < 26 {
		t.Fatalf("user_version = %d", v)
	}
	if s, err := g.r.Settings(ctx); err != nil || s.Enabled || s.Halted() || s.At != "01:30" {
		t.Fatalf("shipped settings = %+v %v", s, err)
	}
	g.grant(2, 3) // a live mandate exists, and still nothing runs while the shift is off
	baseline := []int{
		g.count(`SELECT COUNT(*) FROM estimates`), g.count(`SELECT COUNT(*) FROM batches`), g.count(`SELECT COUNT(*) FROM audit_log`),
		g.count(`SELECT COUNT(*) FROM ledger`), g.count(`SELECT COUNT(*) FROM jobs`), g.count(`SELECT COUNT(*) FROM approvals`),
	}
	rome, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Fatal(err)
	}
	clk := clocktest.NewManual(time.Date(2026, 10, 15, 0, 50, 0, 0, rome))
	d := g.r.Daily(rome)
	d.Clock = clk
	d.MaxSleep = time.Minute
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { d.Loop(loopCtx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	step := func(by time.Duration) {
		waitTimer(t, clk)
		clk.Advance(by)
		waitTimer(t, clk) // the loop re-arms its timer only after it handled the tick: a run, if any, has been started by then
	}
	step(time.Hour) // 01:50, past the slot
	step(24 * time.Hour)
	if n := g.count(`SELECT COUNT(*) FROM night_shift_runs`); n != 0 {
		t.Fatalf("a disabled shift ran %d times", n)
	}
	after := []int{
		g.count(`SELECT COUNT(*) FROM estimates`), g.count(`SELECT COUNT(*) FROM batches`), g.count(`SELECT COUNT(*) FROM audit_log`),
		g.count(`SELECT COUNT(*) FROM ledger`), g.count(`SELECT COUNT(*) FROM jobs`), g.count(`SELECT COUNT(*) FROM approvals`),
	}
	for i := range baseline {
		if baseline[i] != after[i] {
			t.Fatalf("table %d changed from %d to %d rows while the shift was off", i, baseline[i], after[i])
		}
	}

	// enabled: the next slot (01:30 local the following night) makes the run
	g.enable()
	waitTimer(t, clk)
	clk.Set(time.Date(2026, 10, 17, 1, 31, 0, 0, rome))
	if run := waitRun(t, g, "2026-10-17"); run.State != StateCreated {
		t.Fatalf("enabled run = %+v", run)
	}
}

// waitTimer blocks until the loop under test is sleeping on the manual clock.
func waitTimer(t *testing.T, clk *clocktest.Manual) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for clk.Pending() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the schedule loop never went to sleep")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestNightShift_ScheduleAtLocalTime: the runner's schedule fires at the configured local time in MM_TZ (a zone that differs from UTC),
// takes an edited time within a wake, and runs the night keyed by the local date of the slot.
func TestNightShift_ScheduleAtLocalTime(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	g.grant(2, 3)
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-10-14 16:50 UTC is 01:50 on the 15th in Tokyo: today's 01:30 slot is 20 minutes old, inside the 2 h catch-up
	clk := clocktest.NewManual(time.Date(2026, 10, 14, 16, 50, 0, 0, time.UTC))
	d := g.r.Daily(tokyo)
	d.Clock = clk
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { d.Loop(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	if run := waitRun(t, g, "2026-10-15"); run.State != StateCreated {
		t.Fatalf("the catch-up night (local date 2026-10-15): %+v", run)
	}
}

// TestNightShift_CatchUpDoesNotRepeat: a restart after the night ran (or was claimed) does not run it again, and a restart too late does not either.
func TestNightShift_CatchUpDoesNotRepeat(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.enable()
	g.grant(2, 3)
	g.r.Run(t.Context(), g.key("2026-10-15"))
	clk := clocktest.NewManual(time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC))
	d := g.r.Daily(time.UTC)
	d.Clock = clk
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { d.Loop(ctx); close(done) }()
	waitTimer(t, clk)
	cancel()
	<-done
	if n := g.count(`SELECT COUNT(*) FROM night_shift_runs`); n != 1 || nightBatches(g) != 1 {
		t.Fatalf("runs %d batches %d, want 1 each", n, nightBatches(g))
	}
	// four hours late: beyond the 2 h grace, no catch-up
	g2 := newRig(t)
	g2.enable()
	g2.grant(2, 3)
	clk2 := clocktest.NewManual(time.Date(2026, 10, 15, 5, 40, 0, 0, time.UTC))
	d2 := g2.r.Daily(time.UTC)
	d2.Clock = clk2
	ctx2, cancel2 := context.WithCancel(t.Context())
	done2 := make(chan struct{})
	go func() { d2.Loop(ctx2); close(done2) }()
	waitTimer(t, clk2)
	cancel2()
	<-done2
	if n := g2.count(`SELECT COUNT(*) FROM night_shift_runs`); n != 0 {
		t.Fatalf("a restart four hours late ran the night (%d rows)", n)
	}
}

// waitRun waits for the scheduled night to leave `planned`.
func waitRun(t *testing.T, g *rig, night string) Run {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if run, ok, err := g.r.RunOf(t.Context(), night); err == nil && ok && run.State != StatePlanned {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("night %s never finished", night)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
