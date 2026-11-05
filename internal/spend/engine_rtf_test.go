package spend

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/money"
)

func (r *rig) rtfRow(engine, class, gpu string) (rtf, load, samples int, by string, canary sql.NullString) {
	r.t.Helper()
	err := r.st.R.QueryRow(`SELECT rtf_milli, fixed_s, samples, updated_by, canary_id FROM engine_rtf WHERE engine=? AND task_class=? AND gpu_class=?`,
		engine, class, gpu).Scan(&rtf, &load, &samples, &by, &canary)
	if err != nil {
		r.t.Fatal(err)
	}
	return
}

func TestEngineRTF_SeedThenMeasured(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	rtf, load, ok, err := r.s.EngineRTF(ctx, r.st.R, "acestep15", "turbo", []string{"A5000"})
	if err != nil || !ok || rtf != 150 || load != 10 {
		t.Fatalf("seed: %d %d %v %v", rtf, load, ok, err)
	}
	if _, _, ok, _ := r.s.EngineRTF(ctx, r.st.R, "acestep15", "turbo", []string{"A5000", "H100"}); ok {
		t.Error("a class with neither row nor seed must make the answer not ok (no_rtf)")
	}
	if _, _, ok, _ := r.s.EngineRTF(ctx, r.st.R, "other", "turbo", []string{"A5000"}); ok {
		t.Error("unknown engine has no seed")
	}
	r.exec(`INSERT INTO engine_rtf VALUES('acestep15','turbo','NVIDIA RTX A5000',90,12,3,?, 'canary:c1','c1')`, "2026-10-14T12:00:00.000Z")
	rtf, load, ok, err = r.s.EngineRTF(ctx, r.st.R, "acestep15", "turbo", []string{"RTX A5000"})
	if err != nil || !ok || rtf != 90 || load != 12 {
		t.Fatalf("measured row (stored under the raw name) must win over the seed: %d %d %v %v", rtf, load, ok, err)
	}
	r.exec(`INSERT INTO engine_rtf VALUES('acestep15','turbo','H100',200,5,1,?, 'human:op',NULL)`, "2026-10-14T12:00:00.000Z")
	if _, _, ok, _ := r.s.EngineRTF(ctx, r.st.R, "acestep15", "turbo", []string{"H100"}); !ok {
		t.Error("measured-only class is ok")
	}
}

func TestEngineRTF_MaxOverClasses(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	for _, c := range []struct {
		class   string
		gpus    []string
		rtf, ld int
	}{
		{"turbo", []string{"RTX 4090", "A5000"}, 150, 10},
		{"turbo", []string{"RTX 4090", "A5000", "L4"}, 300, 10},
		{"base", []string{"RTX 4090", "RTX A5000"}, 1200, 20},
		{"turbo", nil, 300, 10},
		{"base", nil, 2400, 20},
	} {
		rtf, load, ok, err := r.s.EngineRTF(ctx, r.st.R, "acestep15", c.class, c.gpus)
		if err != nil || !ok || rtf != c.rtf || load != c.ld {
			t.Errorf("%s %v: %d %d %v %v, want %d %d", c.class, c.gpus, rtf, load, ok, err, c.rtf, c.ld)
		}
	}
}

func TestEngineRTFRows(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.exec(`INSERT INTO engine_rtf VALUES('acestep15','turbo','A5000',99,11,2,?, 'job:j1',NULL)`, "2026-10-14T12:00:00.000Z")
	rows, err := r.s.EngineRTFRows(context.Background(), r.st.R, "acestep15")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 {
		t.Fatalf("%d rows, want 6 (5 seeds + the measured A5000 replacing its seed): %+v", len(rows), rows)
	}
	var got EngineRTFRow
	for _, x := range rows {
		if x.TaskClass == "turbo" && x.GPUClass == "A5000" {
			got = x
		}
	}
	want := EngineRTFRow{Engine: "acestep15", TaskClass: "turbo", GPUClass: "A5000", RTFMilli: 99, LoadS: 11, Samples: 2, Source: "measured"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Error(diff)
	}
	if none, _ := r.s.EngineRTFRows(context.Background(), r.st.R, "yue2"); len(none) != 0 {
		t.Error("yue2 has no engine_rtf rows")
	}
}

func TestRecordEngineRTF_EWMA(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	rec := func(by string, e2e, audio int64) error {
		return r.tx(func(tx *sql.Tx) error {
			return r.s.RecordEngineRTF(ctx, tx, "acestep15", "turbo", "NVIDIA RTX A5000", by, e2e, audio)
		})
	}
	if err := rec("job:j1", 20000, 100000); err != nil { // obs 200
		t.Fatal(err)
	}
	rtf, load, n, by, canary := r.rtfRow("acestep15", "turbo", "A5000")
	if rtf != 200 || load != 10 || n != 1 || by != "job:j1" || canary.Valid {
		t.Fatalf("first sample: %d %d %d %s %v", rtf, load, n, by, canary)
	}
	if err := rec("canary:c9", 10000, 100000); err != nil { // obs 100 -> (800+100+2)/5 = 180
		t.Fatal(err)
	}
	rtf, _, n, by, canary = r.rtfRow("acestep15", "turbo", "A5000")
	if rtf != NextRTF(200, 100) || rtf != 180 || n != 2 || by != "canary:c9" || canary.String != "c9" {
		t.Fatalf("second sample: %d %d %s %v", rtf, n, by, canary)
	}
	if err := rec("job:j2", 1, 100000); err != nil { // clamped to RTFMin
		t.Fatal(err)
	}
	if rtf, _, _, _, canary = r.rtfRow("acestep15", "turbo", "A5000"); rtf != NextRTF(180, RTFMin) || canary.Valid {
		t.Errorf("clamped: %d canary %v", rtf, canary)
	}
	if n := r.count(`SELECT count(*) FROM rtf_stats WHERE samples > 0`); n != 0 {
		t.Errorf("rtf_stats was touched: %d learned rows", n)
	}
}

func TestRecordEngineRTF_Refusals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	for _, c := range []struct{ engine, class, gpu, by string }{
		{"yue2", "turbo", "A5000", "job:1"}, {"", "turbo", "A5000", "job:1"}, {"acestep15", "xl", "A5000", "job:1"},
		{"acestep15", "turbo", "", "job:1"}, {"acestep15", "turbo", "A5000", "system:x"}, {"acestep15", "turbo", "A5000", "job:"},
		{"acestep15", "turbo", "A5000", "canary:"}, {"acestep15", "turbo", "A5000", ""},
	} {
		err := r.tx(func(tx *sql.Tx) error {
			return r.s.RecordEngineRTF(ctx, tx, c.engine, c.class, c.gpu, c.by, 1000, 1000)
		})
		if err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	if err := r.tx(func(tx *sql.Tx) error {
		return r.s.RecordEngineRTF(ctx, tx, "acestep15", "turbo", "A5000", "job:1", 1000, 0)
	}); err == nil {
		t.Error("zero audio accepted")
	}
	if r.count(`SELECT count(*) FROM engine_rtf`) != 0 {
		t.Error("refused writes left rows")
	}
	// the migration CHECK is the second line of defence
	if _, err := r.st.W.Exec(`INSERT INTO engine_rtf VALUES('acestep15','turbo','L4',100,10,1,'2026-10-14T12:00:00.000Z','system:x',NULL)`); err == nil || !strings.Contains(err.Error(), "CHECK") {
		t.Errorf("CHECK did not refuse a system writer: %v", err)
	}
}

func TestGlobalWorkerBound_Table(t *testing.T) {
	t.Parallel()
	y := BackendBound{Backend: "runpod-serverless", WorkersMax: 2, PricePPH: 690000, MaxRuntimeS: 900, ColdStartS: 240, KillSlackS: 15}
	a := BackendBound{Backend: "runpod-serverless-acestep", WorkersMax: 2, PricePPH: 1000000, MaxRuntimeS: 900, ColdStartS: 300, KillSlackS: 15}
	x := func(b BackendBound) money.MicroUSD {
		return money.MicroUSD(b.WorkersMax) * money.ForSeconds(int64(b.MaxRuntimeS+b.ColdStartS+b.KillSlackS), b.PricePPH)
	}
	off := func(b BackendBound) BackendBound { b.WorkersMax = 0; return b }
	one := func(b BackendBound) BackendBound { b.WorkersMax = 1; return b }
	for _, c := range []struct {
		name string
		in   []BackendBound
		g, x money.MicroUSD
		bs   []string
	}{
		{"empty", nil, 0, 0, nil},
		{"one backend", []BackendBound{y}, 1380000, x(y), []string{y.Backend}},
		{"two at max", []BackendBound{y, a}, 1380000 + 2000000, x(y) + x(a), []string{y.Backend, a.Backend}},
		{"disarmed contributes 0", []BackendBound{y, off(a)}, 1380000, x(y), []string{y.Backend}},
		{"both disarmed", []BackendBound{off(y), off(a)}, 0, 0, nil},
		{"different prices and one worker", []BackendBound{one(y), one(a)}, 690000 + 1000000, x(one(y)) + x(one(a)), []string{y.Backend, a.Backend}},
		{"negative workers ignored", []BackendBound{{Backend: "z", WorkersMax: -1, PricePPH: 5}}, 0, 0, nil},
		{"zero price", []BackendBound{{Backend: "free", WorkersMax: 2, MaxRuntimeS: 10}}, 0, 0, []string{"free"}},
	} {
		got := ComputeGlobalBound(c.in)
		if got.GMicroPerHour != c.g || got.XMicro != c.x || !cmp.Equal(got.Backends, c.bs) {
			t.Errorf("%s: %+v, want G %d X %d %v", c.name, got, c.g, c.x, c.bs)
		}
	}
	// pinned absolute numbers for the two-at-max row: 2 x ForSeconds(1155, 690000) + 2 x ForSeconds(1215, 1000000)
	if got := ComputeGlobalBound([]BackendBound{y, a}); got.XMicro != 2*221375+2*337500 {
		t.Errorf("X = %d", got.XMicro)
	}
}

func TestParseSeeds_Strict(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"garbage":       `{`,
		"unknown field": `{"version":1,"sourced":false,"engines":{},"x":1}`,
		"bad version":   `{"version":2,"sourced":false,"engines":{}}`,
		"raw gpu name":  `{"version":1,"sourced":false,"engines":{"e":{"turbo":{"NVIDIA RTX A5000":{"rtf_milli":100,"load_s":1}}}}}`,
		"rtf range":     `{"version":1,"sourced":false,"engines":{"e":{"turbo":{"A5000":{"rtf_milli":1,"load_s":1}}}}}`,
		"load range":    `{"version":1,"sourced":false,"engines":{"e":{"turbo":{"A5000":{"rtf_milli":100,"load_s":601}}}}}`,
	} {
		if _, err := parseSeeds([]byte(raw)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := parseSeeds(engineRTFSeedJSON); err != nil {
		t.Errorf("committed seed: %v", err)
	}
}

// TestEngineRTF_StoreErrors: a failing store is an error, never a silent seed fallback or an ok answer.
func TestEngineRTF_StoreErrors(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, ok, err := r.s.EngineRTF(ctx, r.st.R, "acestep15", "turbo", []string{"A5000"}); err == nil || ok {
		t.Errorf("EngineRTF: %v %v", ok, err)
	}
	if _, err := r.s.EngineRTFRows(ctx, r.st.R, "acestep15"); err == nil {
		t.Error("EngineRTFRows swallowed the error")
	}
	err := r.tx(func(tx *sql.Tx) error {
		return r.s.RecordEngineRTF(ctx, tx, "acestep15", "turbo", "A5000", "job:1", 1000, 1000)
	})
	if err == nil {
		t.Error("RecordEngineRTF swallowed the error")
	}
}

func TestEngineRTF_NoClassesKnown(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	if _, _, ok, err := r.s.EngineRTF(ctx, r.st.R, "other", "turbo", nil); ok || err != nil {
		t.Errorf("unknown engine, no classes: %v %v", ok, err)
	}
	r.exec(`INSERT INTO engine_rtf VALUES('other','base','L4',700,15,1,'2026-10-14T12:00:00.000Z','job:j',NULL)`)
	if rtf, load, ok, _ := r.s.EngineRTF(ctx, r.st.R, "other", "base", nil); !ok || rtf != 700 || load != 15 {
		t.Errorf("measured-only engine: %d %d %v", rtf, load, ok)
	}
}

// TestRecordEngineRTF_RowCheckFailure: a failing write (here a missing table) surfaces as an error.
func TestRecordEngineRTF_RowCheckFailure(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.exec(`DROP TRIGGER engine_rtf_no_delete`)
	r.exec(`DROP TABLE engine_rtf`)
	err := r.tx(func(tx *sql.Tx) error {
		return r.s.RecordEngineRTF(context.Background(), tx, "acestep15", "turbo", "A5000", "job:1", 1000, 1000)
	})
	if err == nil {
		t.Error("write into a missing table succeeded")
	}
}
