package ladder

import (
	"math"
	"testing"
)

func TestAcceptance_M2b_BTParameterRecovery(t *testing.T) {
	t.Parallel()
	seeds := 20
	if testing.Short() {
		seeds = 3
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			var maxRMSE, minSp, sumSp = 0.0, 2.0, 0.0
			nuMin, nuMax := math.Inf(1), math.Inf(-1)
			cov, tot := 0, 0
			for seed := 1; seed <= seeds; seed++ {
				res := sc.simulate(uint64(seed))
				tc, ec := center(res.truth), center(res.fit.Theta)
				e := rmse(tc, ec)
				sp := spearman(res.truth, res.fit.Theta)
				maxRMSE = math.Max(maxRMSE, e)
				minSp = math.Min(minSp, sp)
				sumSp += sp
				nuMin, nuMax = math.Min(nuMin, res.fit.Nu), math.Max(nuMax, res.fit.Nu)
				if e > sc.maxRMSE {
					t.Errorf("seed %d: RMSE %.4f > %.2f", seed, e, sc.maxRMSE)
				}
				if sp < sc.minSpear {
					t.Errorf("seed %d: Spearman %.4f < %.2f", seed, sp, sc.minSpear)
				}
				if !res.fit.Converged {
					t.Errorf("seed %d: not converged", seed)
				}
				if res.fit.Nu < sc.nuLo || res.fit.Nu > sc.nuHi {
					t.Errorf("seed %d: nu %.4f outside [%g, %g]", seed, res.fit.Nu, sc.nuLo, sc.nuHi)
				}
				for i := range tc {
					tot++
					if math.Abs(tc[i]-ec[i]) <= 1.96*res.fit.SD[i] {
						cov++
					}
				}
			}
			mean := sumSp / float64(seeds)
			if mean < sc.meanSpear {
				t.Errorf("mean Spearman %.4f < %.2f", mean, sc.meanSpear)
			}
			t.Logf("%s: seeds=%d max RMSE %.4f, min Spearman %.4f, mean Spearman %.4f, nu in [%.4f, %.4f], coverage %.3f",
				sc.name, seeds, maxRMSE, minSp, mean, nuMin, nuMax, float64(cov)/float64(tot))
			if sc.coverage {
				if share := float64(cov) / float64(tot); share < 0.90 {
					t.Errorf("coverage %.3f < 0.90", share)
				}
			}
		})
	}
}
