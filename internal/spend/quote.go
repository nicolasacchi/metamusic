package spend

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// QuoteParams are the frozen inputs of a batch quote (C72.1).
type QuoteParams struct {
	PricePPH    money.MicroUSD // WithMarginPPM(max serverless list price over the allowed GPU types)
	RTFMilli    int            // base cost per audio second ×1000 (no planning): max over the allowed GPU classes, see RTFMilli()
	ColdStartS  int            // MM_COLD_START_S
	IdleS       int            // endpoint workers.idleTimeout
	WorkersMax  int            // endpoint workers.max
	MaxRuntimeS int            // MM_JOB_MAX_RUNTIME in seconds
	KillSlackS  int            // MM_KILL_SLACK_S
}

// TakeCost is the quote of one take.
type TakeCost struct {
	AudioS, ExpectedS, TimeoutS        int
	GenS, GenBudgetS                   int            // expected generation seconds; the worker's generation budget (timeout - finish margin - upload budget)
	ExpectedMicro, TakeMicro, MaxMicro money.MicroUSD // TakeMicro = the `take` reserve; MaxMicro = TakeMicro + one attempt overhead
}

// BatchQuote is the quote of a whole batch.
type BatchQuote struct {
	Takes                                                                            []TakeCost
	AttemptOverheadS                                                                 int
	AttemptOverheadMicro, ExpectedMicro, MaxMicro, RetryAllowanceMicro, CeilingMicro money.MicroUSD
	ETAS                                                                             int
}

func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }

// Phase model of one take's time on the worker (M3-X4 calibration, 24 GB-class GPUs, 2026-10-15).
//
// Measured, melody canary (61.5 s of audio): generation 35.0 s of which weights resolve and integrity
// 6.2 s, semantic 20.5 s (0.33 s per audio second), NAR 6.4 s, VAE 3.5 s; upload 8.2 s; handler 43.9 s.
// Measured, cot=full demo (181 s of audio, estimated 225 s): model load 5 s, ABC planning 2359 tokens at
// 70 tok/s = 33.8 s (0.19 s per audio second), semantic 4525 tokens at 66 tok/s = 68.8 s (0.38 s per
// audio second), NAR 32 steps at 0.83 s = 26.6 s (0.15 s per audio second), VAE about 0.06 s per audio
// second. It was cut at 119.9 s, 119 s after the quote's generation budget began, in the NAR stage.
//
// Model: generation seconds = LoadS + (rtf + plan) × audio, with
//   - LoadS = 10: 5 s model load plus 6.2 s weights resolve, rounded;
//   - rtf (melody and off, no planning): 0.38 + 0.15 + 0.06 = 0.59 s per audio second from the 181 s demo;
//     the 61.5 s canary shows (35.0 - 10) / 61.5 = 0.41 because its NAR stage is cheaper. DefaultRTFMilli = 680
//     adds about 15 % headroom to 0.59 for the older Ampere cards of the allowed set (A5000, 3090), which
//     are unmeasured;
//   - plan = PlanMilli = 220 for cot=full only: 0.19 measured plus the same headroom.
//
// A 300 s cot=full song: 10 + (0.68 + 0.22) × 300 = 280 s, above the 270 s the incident calls for.
// The incident song: 10 + 0.9 × 225 = 213 s expected generation against the 99 s quoted.
const (
	LoadS           = 10  // fixed seconds per take: model load and weights resolve
	DefaultRTFMilli = 680 // base cost for a GPU class without learned samples
	PlanMilli       = 220 // extra cost per audio second of cot=full (ABC planning)
	ExpectedUploadS = 10  // measured 8.2 s for a 61 s take

	// WorkerFinishMarginS and UploadBudgetS mirror worker/src/mmworker/handler.py (_FINISH_MARGIN_S and the
	// upload_budget_s of the job input): the worker's generation deadline is timeout_s - 10 - upload_budget_s
	// (the other bound, deadline_unix - 60, is hours later: the attempt window includes the queue TTL).
	WorkerFinishMarginS = 10
	UploadBudgetS       = 60
	// TimeoutSlackS is spent on top of the headroom: start handshake and clock skew between the app and
	// the worker.
	TimeoutSlackS = 10
	// headroom 8/5: the worker's generation budget is at least 1.6 × the expected generation seconds.
	headroomNum, headroomDen = 8, 5
)

// TakeIn is one take to quote.
type TakeIn struct {
	AudioS int  // expected audio seconds
	Full   bool // cot=full: the model plans the ABC score first
}

// QuoteBatch quotes takes without ABC planning (cot melody or off); see QuoteTakes.
func QuoteBatch(p QuoteParams, audioS []int) BatchQuote {
	takes := make([]TakeIn, len(audioS))
	for i, a := range audioS {
		takes[i] = TakeIn{AudioS: a}
	}
	return QuoteTakes(p, takes)
}

// QuoteTakes computes the quote with integer arithmetic only (C72.1, phase model above):
//
//	gen     = LoadS + ceil((rtf + plan) × audio / 1000)
//	expect  = gen + ExpectedUploadS
//	timeout = min(MaxRuntimeS, ceil(1.6 × gen) + WorkerFinishMarginS + UploadBudgetS + TimeoutSlackS)
//
// so the worker's generation budget (timeout - 10 - 60) is at least 1.6 × gen unless the runtime cap binds.
func QuoteTakes(p QuoteParams, takes []TakeIn) BatchQuote {
	q := BatchQuote{Takes: make([]TakeCost, 0, len(takes))}
	q.AttemptOverheadS = p.ColdStartS + p.IdleS
	q.AttemptOverheadMicro = money.ForSeconds(int64(q.AttemptOverheadS), p.PricePPH)
	var maxOne money.MicroUSD
	var maxExpectedS int
	var sumExpected money.MicroUSD
	for _, t := range takes {
		rate := int64(p.RTFMilli)
		if t.Full {
			rate += PlanMilli
		}
		gen := LoadS + ceilDiv(rate*int64(t.AudioS), 1000)
		exp := int(gen) + ExpectedUploadS
		to := ceilDiv(headroomNum*gen, headroomDen) + WorkerFinishMarginS + UploadBudgetS + TimeoutSlackS
		to = min(int64(p.MaxRuntimeS), to)
		tc := TakeCost{AudioS: t.AudioS, ExpectedS: exp, TimeoutS: int(to), GenS: int(gen), GenBudgetS: int(to) - WorkerFinishMarginS - UploadBudgetS}
		tc.TakeMicro = money.ForSeconds(to+int64(p.KillSlackS), p.PricePPH)
		tc.MaxMicro = tc.TakeMicro + q.AttemptOverheadMicro
		tc.ExpectedMicro = money.ForSeconds(int64(min(exp, int(to))), p.PricePPH)
		q.Takes = append(q.Takes, tc)
		q.MaxMicro += tc.MaxMicro
		sumExpected += tc.ExpectedMicro
		maxOne = max(maxOne, tc.MaxMicro)
		maxExpectedS = max(maxExpectedS, exp)
	}
	n := len(takes)
	q.ExpectedMicro = sumExpected + q.AttemptOverheadMicro
	q.RetryAllowanceMicro = money.MicroUSD(min(n, p.WorkersMax)) * maxOne
	q.CeilingMicro = q.MaxMicro + q.RetryAllowanceMicro
	if p.WorkersMax > 0 {
		q.ETAS = p.ColdStartS + int(ceilDiv(int64(n), int64(p.WorkersMax)))*maxExpectedS
	}
	return q
}

// EffectivePrice is the list price with the safety margin (C72.1).
func EffectivePrice(q provider.Quote, marginPPM int64) money.MicroUSD {
	return money.WithMarginPPM(q.ListPricePPH, marginPPM)
}

// RTF bounds (C72.1).
const (
	RTFMin = 50
	RTFMax = 5000
)

// ClampRTF bounds an observed real-time factor.
func ClampRTF(obs int) int { return min(RTFMax, max(RTFMin, obs)) }

// ObservedRTF is round(1000 × e2e / audio) from millisecond durations, clamped.
func ObservedRTF(e2eMS, audioMS int64) int {
	if audioMS <= 0 || e2eMS < 0 {
		return RTFMin
	}
	return ClampRTF(int((1000*e2eMS + audioMS/2) / audioMS))
}

// ObservedBaseRTF is the base factor one completed take shows: the generation seconds (e2e) minus the fixed
// load, per audio second, minus the planning constant for cot=full, so the learned value is comparable across
// cots and planning is never counted twice (the quote adds PlanMilli itself). Clamped.
func ObservedBaseRTF(e2eMS, audioMS int64, full bool) int {
	if audioMS <= 0 || e2eMS < 0 {
		return RTFMin
	}
	v := (1000*(e2eMS-LoadS*1000) + audioMS/2) / audioMS
	if full {
		v -= PlanMilli
	}
	return ClampRTF(int(v))
}

// NextRTF is the EWMA step rtf' = (4 rtf + obs + 2) / 5 (alpha 0.2, integer division); obs is clamped.
func NextRTF(rtf, obs int) int { return (4*rtf + ClampRTF(obs) + 2) / 5 }

type rtfRow struct {
	class   string
	rtf     int
	samples int
}

func readRTF(ctx context.Context, q Querier) ([]rtfRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT gpu_class, rtf_milli, samples FROM rtf_stats ORDER BY gpu_class`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rtfRow
	for rows.Next() {
		var r rtfRow
		if err := rows.Scan(&r.class, &r.rtf, &r.samples); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordRTF updates the class's factor after a completed take, from the worker-reported GPU name or any
// class spelling: rows are matched through GPUClassKey, so a row stored under the raw worker name is updated
// in place and a new class is stored under its normalised key. The first real sample of a class replaces the
// seeded prior (samples = 0). full is the take's cot=full flag (see ObservedBaseRTF). It runs in the
// caller's transaction.
func (s *Service) RecordRTF(ctx context.Context, tx *sql.Tx, gpuClass string, e2eMS, audioMS int64, full bool) error {
	key := GPUClassKey(gpuClass)
	if key == "" {
		return errors.New("spend: rtf: empty gpu class")
	}
	obs := ObservedBaseRTF(e2eMS, audioMS, full)
	rows, err := readRTF(ctx, tx)
	if err != nil {
		return fmt.Errorf("spend: record rtf: %w", err)
	}
	var hit *rtfRow
	for i := range rows {
		if GPUClassKey(rows[i].class) == key && (hit == nil || rows[i].class == key) {
			hit = &rows[i]
		}
	}
	now := store.FormatTime(s.now())
	switch {
	case hit == nil:
		_, err = tx.ExecContext(ctx, `INSERT INTO rtf_stats(gpu_class, rtf_milli, samples, updated_at) VALUES (?, ?, 1, ?)`, key, obs, now)
	case hit.samples == 0:
		_, err = tx.ExecContext(ctx, `UPDATE rtf_stats SET rtf_milli = ?, samples = 1, updated_at = ? WHERE gpu_class = ?`, obs, now, hit.class)
	default:
		_, err = tx.ExecContext(ctx, `UPDATE rtf_stats SET rtf_milli = ?, samples = samples + 1, updated_at = ? WHERE gpu_class = ?`,
			NextRTF(hit.rtf, obs), now, hit.class)
	}
	if err != nil {
		return fmt.Errorf("spend: record rtf: %w", err)
	}
	return nil
}

// RTFMilli is the base factor a quote uses for the allowed GPU classes: the largest of the learned values
// (rows with samples > 0, matched through GPUClassKey) and, when some allowed class has no learned row,
// DefaultRTFMilli: an unmeasured class never lowers the estimate below what is known. Seeded priors
// (samples = 0) are not measurements. ok is true when any allowed class has a learned row. With no allowed
// classes the largest learned value over all rows (floored at DefaultRTFMilli) is used.
func (s *Service) RTFMilli(ctx context.Context, q Querier, classes []string) (int, bool, error) {
	rows, err := readRTF(ctx, q)
	if err != nil {
		return 0, false, err
	}
	learned := map[string]int{}
	for _, r := range rows {
		if r.samples > 0 {
			k := strings.ToUpper(GPUClassKey(r.class))
			learned[k] = max(learned[k], r.rtf)
		}
	}
	best, ok, unknown := 0, false, len(classes) == 0
	if len(classes) == 0 {
		for _, v := range learned {
			best, ok = max(best, v), true
		}
	}
	for _, c := range classes {
		if v, has := learned[strings.ToUpper(GPUClassKey(c))]; has {
			best, ok = max(best, v), true
		} else {
			unknown = true
		}
	}
	if unknown {
		best = max(best, DefaultRTFMilli)
	}
	return best, ok, nil
}
