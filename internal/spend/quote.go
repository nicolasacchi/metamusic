package spend

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// QuoteParams are the frozen inputs of a batch quote (C72.1).
type QuoteParams struct {
	PricePPH    money.MicroUSD // WithMarginPPM(max serverless list price over the allowed GPU types)
	RTFMilli    int            // max rtf_milli over the allowed GPU classes
	ColdStartS  int            // MM_COLD_START_S
	IdleS       int            // endpoint workers.idleTimeout
	WorkersMax  int            // endpoint workers.max
	MaxRuntimeS int            // MM_JOB_MAX_RUNTIME in seconds
	KillSlackS  int            // MM_KILL_SLACK_S
}

// TakeCost is the quote of one take.
type TakeCost struct {
	AudioS, ExpectedS, TimeoutS        int
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

// QuoteBatch computes the quote with integer arithmetic only (C72.1).
func QuoteBatch(p QuoteParams, audioS []int) BatchQuote {
	q := BatchQuote{Takes: make([]TakeCost, 0, len(audioS))}
	q.AttemptOverheadS = p.ColdStartS + p.IdleS
	q.AttemptOverheadMicro = money.ForSeconds(int64(q.AttemptOverheadS), p.PricePPH)
	var maxOne money.MicroUSD
	var maxExpectedS int
	var sumExpected money.MicroUSD
	for _, a := range audioS {
		exp := int(ceilDiv(int64(p.RTFMilli)*int64(a), 1000))
		to := max(ceilDiv(3*int64(exp), 2), int64(exp)+90)
		to = min(int64(p.MaxRuntimeS), to)
		tc := TakeCost{AudioS: a, ExpectedS: exp, TimeoutS: int(to)}
		tc.TakeMicro = money.ForSeconds(to+int64(p.KillSlackS), p.PricePPH)
		tc.MaxMicro = tc.TakeMicro + q.AttemptOverheadMicro
		tc.ExpectedMicro = money.ForSeconds(int64(min(exp, int(to))), p.PricePPH)
		q.Takes = append(q.Takes, tc)
		q.MaxMicro += tc.MaxMicro
		sumExpected += tc.ExpectedMicro
		maxOne = max(maxOne, tc.MaxMicro)
		maxExpectedS = max(maxExpectedS, exp)
	}
	n := len(audioS)
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

// NextRTF is the EWMA step rtf' = (4 rtf + obs + 2) / 5 (alpha 0.2, integer division); obs is clamped.
func NextRTF(rtf, obs int) int { return (4*rtf + ClampRTF(obs) + 2) / 5 }

// RecordRTF updates the class's factor after a completed take; an unknown class is inserted with the
// observation. It runs in the caller's transaction.
func (s *Service) RecordRTF(ctx context.Context, tx *sql.Tx, gpuClass string, e2eMS, audioMS int64) error {
	if gpuClass == "" {
		return errors.New("spend: rtf: empty gpu class")
	}
	obs := ObservedRTF(e2eMS, audioMS)
	var cur int
	err := tx.QueryRowContext(ctx, `SELECT rtf_milli FROM rtf_stats WHERE gpu_class = ?`, gpuClass).Scan(&cur)
	now := store.FormatTime(s.now())
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO rtf_stats(gpu_class, rtf_milli, samples, updated_at) VALUES (?, ?, 1, ?)`, gpuClass, obs, now)
	case err == nil:
		_, err = tx.ExecContext(ctx, `UPDATE rtf_stats SET rtf_milli = ?, samples = samples + 1, updated_at = ? WHERE gpu_class = ?`,
			NextRTF(cur, obs), now, gpuClass)
	}
	if err != nil {
		return fmt.Errorf("spend: record rtf: %w", err)
	}
	return nil
}

// RTFMilli is the largest factor over the given classes known to rtf_stats; none known (or none
// given) falls back to the maximum over all rows. No rows at all returns 0 and ok false.
func (s *Service) RTFMilli(ctx context.Context, q Querier, classes []string) (int, bool, error) {
	var v sql.NullInt64
	if len(classes) > 0 {
		args := make([]any, len(classes))
		ph := ""
		for i, c := range classes {
			args[i] = c
			if i > 0 {
				ph += ","
			}
			ph += "?"
		}
		if err := q.QueryRowContext(ctx, `SELECT MAX(rtf_milli) FROM rtf_stats WHERE gpu_class IN (`+ph+`)`, args...).Scan(&v); err != nil {
			return 0, false, err
		}
	}
	if !v.Valid {
		if err := q.QueryRowContext(ctx, `SELECT MAX(rtf_milli) FROM rtf_stats`).Scan(&v); err != nil {
			return 0, false, err
		}
	}
	return int(v.Int64), v.Valid, nil
}
