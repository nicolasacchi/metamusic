package morning

import (
	"context"
	"errors"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/schedule"
	"math"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"time"
)

// TestMorning_ClosedStoreFaults: with the database gone every entry point returns an error (never a panic, never a partial crate),
// and a crate that was ready stays unreadable rather than half-served.
func TestMorning_ClosedStoreFaults(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	for i := range 4 {
		r.take("Secret "+string(rune('A'+i)), time.Duration(i+1)*time.Hour)
	}
	c, err := r.svc.Build(ctx, owner, BuildIn{})
	if err != nil {
		t.Fatal(err)
	}
	c = r.built(c)
	sha := ""
	if len(c.Sides) > 0 {
		sha = r.scalar(`SELECT sha256 FROM morning_crate_sides WHERE crate_id = ? LIMIT 1`, c.ID)
	}
	r.st.Close()

	calls := map[string]func() error{
		"Get":            func() error { _, err := r.svc.Get(ctx, owner, c.ID); return err },
		"List":           func() error { _, err := r.svc.List(ctx, owner, ListFilter{}); return err },
		"Reveal":         func() error { _, err := r.svc.Reveal(ctx, owner, c.ID); return err },
		"Settings":       func() error { _, err := r.svc.Settings(ctx); return err },
		"UpdateSettings": func() error { _, err := r.svc.UpdateSettings(ctx, owner, SettingsPatch{}); return err },
		"Eligible":       func() error { _, err := r.svc.Eligible(ctx, nil, ""); return err },
		"Sweep":          func() error { _, err := r.svc.Sweep(ctx); return err },
		"Recover":        func() error { return r.svc.Recover(ctx) },
		"Resolve":        func() error { _, err := r.svc.Resolve(ctx, sha, "flac"); return err },
		"SubmitEvents": func() error {
			_, err := r.svc.SubmitEvents(ctx, owner, c.ID, []EventIn{{ID: "01900000-0000-7000-8000-00000000f001", Side: 0, Chapter: 1, Kind: "play", ClientAt: dawn}})
			return err
		},
		"Build": func() error { _, err := r.svc.Build(ctx, owner, BuildIn{}); return err },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s on a closed store returned no error", name)
		}
	}
	// the scheduled run swallows the fault (it is a background job) and must not panic
	r.svc.RunScheduled(ctx, schedule.Key{Day: "2026-11-22", At: "07:00", Planned: dawn})
}

// TestMorning_RefusalsAndValidation: every read and write of the crate is the Owner's side only, and bad filters, cursors and
// settings are refused before anything is read or written.
func TestMorning_RefusalsAndValidation(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	r.take("Secret A", time.Hour)
	c, err := r.svc.Build(ctx, owner, BuildIn{})
	if err != nil {
		t.Fatal(err)
	}
	c = r.built(c)
	for _, p := range []auth.Principal{editor, listener} {
		if _, err := r.svc.Get(ctx, p, c.ID); err == nil {
			t.Errorf("%s: Get", p.Name)
		}
		if _, err := r.svc.List(ctx, p, ListFilter{}); err == nil {
			t.Errorf("%s: List", p.Name)
		}
		if _, err := r.svc.Reveal(ctx, p, c.ID); err == nil {
			t.Errorf("%s: Reveal", p.Name)
		}
		if _, err := r.svc.Build(ctx, p, BuildIn{}); err == nil {
			t.Errorf("%s: Build", p.Name)
		}
		if _, err := r.svc.UpdateSettings(ctx, p, SettingsPatch{}); err == nil {
			t.Errorf("%s: UpdateSettings", p.Name)
		}
		if _, err := r.svc.SubmitEvents(ctx, p, c.ID, nil); err == nil {
			t.Errorf("%s: SubmitEvents", p.Name)
		}
	}
	if _, err := r.svc.Get(ctx, owner, "nope"); code(err) != "NOT_FOUND" {
		t.Errorf("unknown crate: %v", err)
	}
	for name, f := range map[string]ListFilter{"state": {State: "weird"}, "cursor": {Cursor: "nobar"}, "empty cursor": {Cursor: "|x"}} {
		if _, err := r.svc.List(ctx, owner, f); code(err) != "VALIDATION" {
			t.Errorf("list %s: %v", name, err)
		}
	}
	if pg, err := r.svc.List(ctx, owner, ListFilter{State: StateReady, Limit: 1000, Cursor: "9999-01-01T00:00:00.000Z|zzz"}); err != nil || len(pg.Items) != 1 {
		t.Errorf("list ready: %+v %v", pg, err)
	}
	bad := 3
	if _, err := r.svc.UpdateSettings(ctx, owner, SettingsPatch{Minutes: &bad}); code(err) != "MORNING_SETTINGS_INVALID" {
		t.Errorf("minutes 3: %v", err)
	}
	at := "7am"
	if _, err := r.svc.UpdateSettings(ctx, owner, SettingsPatch{At: &at}); code(err) != "MORNING_SETTINGS_INVALID" {
		t.Errorf("at 7am: %v", err)
	}
	// a full patch applies as a whole and records who changed it
	en, tr := true, true
	at, m, sm, mt, an, kd, wm := "06:30", 30, 10, 8, 1, 5, 20
	got, err := r.svc.UpdateSettings(ctx, owner, SettingsPatch{Enabled: &en, At: &at, Minutes: &m, SideMinutes: &sm, MaxTakes: &mt, Anchors: &an, IncludeTruncated: &tr, KeepDays: &kd, WaitMinutes: &wm})
	if err != nil || !got.Enabled || got.At != "06:30" || got.Minutes != 30 || got.SideMinutes != 10 || got.MaxTakes != 8 || got.Anchors != 1 ||
		!got.IncludeTruncated || got.KeepDays != 5 || got.WaitMinutes != 20 || got.UpdatedBy != owner.Key() {
		t.Errorf("settings = %+v %v", got, err)
	}
	if hhmm, on, err := r.svc.dailyAt(ctx); err != nil || !on || hhmm != "06:30" {
		t.Errorf("dailyAt = %q %v %v", hhmm, on, err)
	}
}

// TestMorning_DamagedRows: a damaged timestamp is an error where it is read, never a zero time.
func TestMorning_DamagedRows(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	r.take("Secret A", time.Hour)
	c, err := r.svc.Build(ctx, owner, BuildIn{})
	if err != nil {
		t.Fatal(err)
	}
	c = r.built(c)
	r.exec(`UPDATE morning_crate_settings SET updated_at = 'never'`)
	if _, err := r.svc.Settings(ctx); err == nil {
		t.Error("settings with a damaged stamp")
	}
	if _, _, err := r.svc.dailyAt(ctx); err == nil {
		t.Error("dailyAt with a damaged stamp")
	}
	if _, err := r.svc.Sweep(ctx); err == nil {
		t.Error("sweep with unreadable settings")
	}
	for _, col := range []string{"built_at", "revealed_at"} {
		r.exec(`DROP TRIGGER IF EXISTS morning_crates_summary_final`)
		r.exec(`DROP TRIGGER IF EXISTS morning_crates_transition`)
		set := col + ` = 'sometime'`
		if col == "revealed_at" {
			set += `, revealed_by = 'alice'`
		}
		r.exec(`UPDATE morning_crates SET `+set+` WHERE id = ?`, c.ID)
		if _, err := r.svc.Get(ctx, owner, c.ID); err == nil {
			t.Errorf("Get with a damaged %s", col)
		}
		if _, err := r.svc.List(ctx, owner, ListFilter{}); err == nil {
			t.Errorf("List with a damaged %s", col)
		}
	}
}

func TestMorning_WaitForNight(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	with := func(f NightBatchFunc) *Service {
		o := r.svc.o
		o.NightBatch = f
		return &Service{o: o}
	}
	const day = "2026-11-22"
	if got := with(nil).waitForNight(ctx, day, time.Hour); got != "" {
		t.Errorf("no seam: %q", got)
	}
	if got := with(func(context.Context, string) (string, bool, bool, error) { return "b", true, true, errors.New("boom") }).waitForNight(ctx, day, time.Hour); got != "" {
		t.Errorf("seam error: %q", got)
	}
	if got := with(func(context.Context, string) (string, bool, bool, error) { return "", false, false, nil }).waitForNight(ctx, day, time.Hour); got != "" {
		t.Errorf("no night batch: %q", got)
	}
	if got := with(func(context.Context, string) (string, bool, bool, error) { return "b1", true, true, nil }).waitForNight(ctx, day, time.Hour); got != "b1" {
		t.Errorf("finished: %q", got)
	}
	if got := with(func(context.Context, string) (string, bool, bool, error) { return "b2", false, true, nil }).waitForNight(ctx, day, 0); got != "b2" {
		t.Errorf("deadline reached: %q", got)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if got := with(func(context.Context, string) (string, bool, bool, error) { return "b3", false, true, nil }).waitForNight(cctx, day, time.Hour); got != "b3" {
		t.Errorf("cancelled while waiting: %q", got)
	}
}

func TestMorning_SweepScheduleAndResolve(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	d := r.svc.SweepDaily()
	if at, on, err := d.At(ctx); err != nil || !on || at != SweepAt {
		t.Errorf("sweep at = %q %v %v", at, on, err)
	}
	d.Run(ctx, schedule.Key{}) // nothing to sweep: no error, no panic
	for name, c := range map[string]struct{ sha, ext string }{
		"ext":       {strings.Repeat("a", 64), "wav"},
		"short sha": {"abc", "flac"},
		"unknown":   {strings.Repeat("b", 64), "flac"},
	} {
		if _, err := r.svc.Resolve(ctx, c.sha, c.ext); !errors.Is(err, serve.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if (&Service{o: ServiceOptions{}}).fileExists("x") {
		t.Error("fileExists without a data root")
	}
	if n, err := (&Service{o: ServiceOptions{}}).Sweep(ctx); err != nil || n != 0 {
		t.Errorf("sweep without a store: %d %v", n, err)
	}
}

func TestPlanHelpers(t *testing.T) {
	t.Parallel()
	a := picked{c: Candidate{TrackID: "a", RootID: "r"}}
	b := picked{c: Candidate{TrackID: "b", RootID: "r"}}
	c := picked{c: Candidate{TrackID: "c"}}
	d := picked{c: Candidate{TrackID: "d"}}
	if !hasAdjacent([]picked{a, b}) || hasAdjacent([]picked{a, c, b}) || hasAdjacent(nil) || hasAdjacent([]picked{c, d}) {
		t.Error("hasAdjacent")
	}
	if rootKey(c.c) != "track:c" || rootKey(a.c) != "r" {
		t.Error("rootKey")
	}
	for name, tc := range map[string]struct {
		c    Candidate
		want int64
	}{
		"same rate":  {Candidate{TotalSamples: 96000, SampleRate: SampleRate}, 96000},
		"resampled":  {Candidate{TotalSamples: 44100, SampleRate: 44100}, SampleRate},
		"by seconds": {Candidate{DurationS: 2}, 2 * SampleRate},
	} {
		if got := audioSamples(tc.c); got != tc.want {
			t.Errorf("%s: %d, want %d", name, got, tc.want)
		}
	}
	if sourceRank(SourceManual) != 0 || sourceRank(SourceNightShift) != 1 || sourceRank(SourceJob) != 2 || sourceRank("x") != 3 {
		t.Error("sourceRank")
	}
	if noLoudness(Candidate{LUFS: -14, TruePeak: -1}) || !noLoudness(Candidate{}) || !noLoudness(Candidate{LUFS: math.Inf(1), TruePeak: -1}) {
		t.Error("noLoudness")
	}
}

func TestFailCode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if got := failCode(ctx, &BuildError{Code: CodeLoudness}); got != CodeLoudness {
		t.Errorf("build error: %s", got)
	}
	if got := failCode(ctx, errors.New("x")); got != CodeToolFailed {
		t.Errorf("plain: %s", got)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if got := failCode(cctx, errors.New("x")); got != CodeInterrupted {
		t.Errorf("cancelled: %s", got)
	}
}

// TestMorning_BuildFaultsStoreTheCode: a build that cannot read its input ends failed with a code, never ready and never stuck building.
func TestMorning_BuildFaultsStoreTheCode(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	r.take("Secret A", time.Hour)
	r.exec(`ALTER TABLE analyses RENAME TO analyses_gone`) // the candidate query reads loudness from here
	c, err := r.svc.Build(ctx, owner, BuildIn{})
	if err != nil {
		t.Fatalf("the build must start so the failure code is stored: %v", err)
	}
	c = r.built(c)
	if c.State != StateFailed || c.ErrorCode == nil || *c.ErrorCode != CodeToolFailed {
		t.Errorf("crate = %+v", c)
	}
}

// TestMorning_RevealAndEventsFaults: a table that fails mid-transaction rolls the whole reveal back (the crate stays blind), and a
// reveal of a missing, unfinished or foreign crate is refused.
func TestMorning_RevealAndEventsFaults(t *testing.T) {
	t.Parallel()
	for _, tbl := range []string{"morning_crate_events", "morning_crate_items", "morning_crate_sides"} {
		t.Run(tbl, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			ctx := context.Background()
			r.take("Secret A", time.Hour)
			c, err := r.svc.Build(ctx, owner, BuildIn{})
			if err != nil {
				t.Fatal(err)
			}
			c = r.built(c)
			r.exec(`ALTER TABLE ` + tbl + ` RENAME TO ` + tbl + `_gone`)
			if _, err := r.svc.Reveal(ctx, owner, c.ID); err == nil {
				t.Error("reveal over a broken table")
			}
			if tbl == "morning_crate_events" {
				if _, err := r.svc.SubmitEvents(ctx, owner, c.ID, []EventIn{{ID: "01900000-0000-7000-8000-00000000f002", Side: 0, Chapter: 1, Kind: "play", ClientAt: dawn}}); err == nil {
					t.Error("events over a broken table")
				}
			}
			if got := r.scalar(`SELECT revealed_at IS NULL FROM morning_crates WHERE id = ?`, c.ID); got != "1" {
				t.Errorf("a failed reveal left the crate revealed (%s)", got)
			}
		})
	}
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.svc.Reveal(ctx, owner, "nope"); code(err) != "NOT_FOUND" {
		t.Errorf("unknown: %v", err)
	}
	if _, err := r.svc.Reveal(ctx, agent, "nope"); err == nil {
		t.Error("an agent revealed")
	}
	r.take("Secret A", time.Hour)
	r.build.gate = make(chan struct{})
	c, err := r.svc.Build(ctx, owner, BuildIn{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Reveal(ctx, owner, c.ID); err == nil {
		t.Error("revealed a crate that is still building")
	}
	close(r.build.gate)
	r.svc.Wait()
}
