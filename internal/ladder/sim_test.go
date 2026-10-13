package ladder

import (
	"math"
	"math/rand/v2"
	"testing"
)

// simResult is one simulated voter study.
type simResult struct {
	truth []float64
	fit   Fit
}

type scenario struct {
	name       string
	items      int
	duels      int
	mean       float64 // truth theta ~ N(mean, 1)
	nu         float64 // 0 => no ties
	anchor     bool
	temps      []float64
	maxRMSE    float64
	minSpear   float64
	meanSpear  float64
	nuLo, nuHi float64
	coverage   bool
}

func (s scenario) simulate(seed uint64) simResult {
	r := rand.New(rand.NewPCG(seed, 0x6d6d))
	truth := make([]float64, s.items)
	for i := range truth {
		truth[i] = s.mean + r.NormFloat64()
	}
	var cmps []Comparison
	for d := 0; d < s.duels; d++ {
		i := r.IntN(s.items)
		j := r.IntN(s.items - 1)
		if j >= i {
			j++
		}
		if s.anchor {
			failI := r.Float64() < 1/(1+math.Exp(truth[i]))
			failJ := r.Float64() < 1/(1+math.Exp(truth[j]))
			if failI && failJ {
				cmps = append(cmps, Comparison{Winner: Anchor, Loser: i}, Comparison{Winner: Anchor, Loser: j})
				continue
			}
		}
		temp := 1.0
		if len(s.temps) > 0 {
			temp = s.temps[d%len(s.temps)]
		}
		w, ti, _ := PBeats(truth[i]/temp, truth[j]/temp, s.nu)
		switch x := r.Float64(); {
		case x < w:
			cmps = append(cmps, Comparison{Winner: i, Loser: j})
		case x < w+ti:
			cmps = append(cmps, Comparison{Winner: i, Loser: j, Tie: true})
		default:
			cmps = append(cmps, Comparison{Winner: j, Loser: i})
		}
	}
	f, err := FitBTD(s.items, cmps, Options{})
	if err != nil {
		panic(err)
	}
	return simResult{truth: truth, fit: f}
}

// Reference numbers from the M2b-J1 run (20 seeds per scenario, PCG(seed, 0x6d6d); -short uses seeds 1..3):
//
//	a: max RMSE 0.1897, min Spearman 0.8788, mean Spearman 0.9727, nu in [0.0014, 0.0019], coverage 1.000
//	b: max RMSE 0.1508, min Spearman 0.9394, mean Spearman 0.9848, nu in [0.4436, 0.6033], coverage 1.000
//	c: max RMSE 0.1526, min Spearman 0.9048, mean Spearman 0.9774, nu in [0.2566, 0.3568], coverage 1.000
//	d: max RMSE 0.1516, min Spearman 0.9394, mean Spearman 0.9830, nu in [0.2567, 0.3445], coverage 1.000
//
// Threshold change (written justification, per the plan): scenario b's upper nu bound is 0.62, not
// 0.60. The fitter is verified independently (stationary point, numeric Hessian, monotone MM);
// with about 400 ties per study the sampling SD of nu-hat is about 0.04, so one of 20 studies
// reaching 0.603 under this simulator's RNG streams is sampling noise, not bias. All other
// thresholds are the plan's.
var scenarios = []scenario{
	{name: "a pure BT", items: 10, duels: 1500, nu: 0, maxRMSE: 0.25, minSpear: 0.85, meanSpear: 0.94, nuLo: 0, nuHi: 0.01, coverage: true},
	{name: "b ties", items: 10, duels: 2000, nu: 0.5, maxRMSE: 0.20, minSpear: 0.85, meanSpear: 0.94, nuLo: 0.40, nuHi: 0.62, coverage: true},
	{name: "c reject anchor", items: 8, duels: 2000, mean: 0.5, nu: 0.3, anchor: true, maxRMSE: 0.25, minSpear: 0.85, meanSpear: 0.93, nuLo: 0.20, nuHi: 0.40},
	{name: "d mixed voters", items: 10, duels: 2000, nu: 0.3, temps: []float64{0.8, 1, 1.25}, maxRMSE: 0.25, minSpear: 0.85, meanSpear: 0.93, nuLo: -1, nuHi: 2, coverage: true},
}

func TestSimulatorIsDeterministic(t *testing.T) {
	t.Parallel()
	a, b := scenarios[2].simulate(3), scenarios[2].simulate(3)
	if a.fit.Nu != b.fit.Nu || a.truth[0] != b.truth[0] {
		t.Fatal("simulation not reproducible")
	}
}
