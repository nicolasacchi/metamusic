package runpod

import (
	"fmt"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// IllustrativePricePPH is the price the dry run uses when no live price is given: 690 000 micro-USD per hour. It is an
// illustration for the worst-case arithmetic, never a quote; the figure the operator approves comes from a dry run
// that names the live price.
const IllustrativePricePPH money.MicroUSD = 690_000

// BudgetParams are the inputs of the worst-case arithmetic (C162.4).
type BudgetParams struct {
	PricePPH    money.MicroUSD // per worker hour, with margin (default IllustrativePricePPH)
	ColdStartS  int            // MM_ACESTEP_COLD_START_S (default 300)
	IdleS       int            // endpoint idle timeout (default 20)
	KillSlackS  int            // MM_KILL_SLACK_S (default 15)
	MaxRuntimeS int            // MM_ACESTEP_MAX_RUNTIME in seconds (default 900)
	Pools       []string       // default acestep.DefaultPools()
	MaxS        int            // t2m-long candidate maximum (default 480)
	// InitFailWindow is the init-fail case's start window (default DefaultCanaryStartTimeout).
	InitFailWindow time.Duration
	// NoSource prices the source takes of the edit cases (the default; --source-track removes them).
	SourceTrack bool
}

func (p BudgetParams) withDefaults() BudgetParams {
	if p.PricePPH <= 0 {
		p.PricePPH = IllustrativePricePPH
	}
	if p.ColdStartS <= 0 {
		p.ColdStartS = 300
	}
	if p.IdleS <= 0 {
		p.IdleS = provisionIdleS
	}
	if p.KillSlackS <= 0 {
		p.KillSlackS = 15
	}
	if p.MaxRuntimeS <= 0 {
		p.MaxRuntimeS = 900
	}
	if len(p.Pools) == 0 {
		p.Pools = acestep.DefaultPools()
	}
	if p.MaxS <= 0 {
		p.MaxS = defaultLongS
	}
	if p.InitFailWindow <= 0 {
		p.InitFailWindow = DefaultCanaryStartTimeout
	}
	return p
}

// placeholderRTF are the committed unsourced seeds of internal/spend/engine_rtf.seed.json (TestBudget_PlaceholdersMatchSeed
// pins the equality): the real-time factor (milli) and fixed load seconds per class and GPU class until a canary
// measures them.
var placeholderRTF = map[string]map[string]struct{ RTFMilli, LoadS int }{
	acestep.ClassTurbo: {"RTX 4090": {120, 10}, "A5000": {150, 10}, "L4": {300, 10}},
	acestep.ClassBase:  {"RTX 4090": {900, 20}, "A5000": {1200, 20}, "L4": {2400, 20}},
}

// poolClasses are the GPU classes of the pools the placeholders know. L4 is excluded from every pool (C155.1).
var poolClasses = map[string][]string{"AMPERE_24": {"A5000"}, "ADA_24": {"RTX 4090"}}

// classRTF is the maximum RTF and load over the pools' classes (the quote's rule); a pool with no placeholder counts
// every class of the seed, the conservative choice.
func classRTF(class string, pools []string) (rtf, load int) {
	for _, pl := range pools {
		cls, ok := poolClasses[pl]
		if !ok {
			cls = nil
			for k := range placeholderRTF[class] {
				cls = append(cls, k)
			}
		}
		for _, k := range cls {
			v := placeholderRTF[class][k]
			rtf, load = max(rtf, v.RTFMilli), max(load, v.LoadS)
		}
	}
	return rtf, load
}

// BudgetTake is one priced take.
type BudgetTake struct {
	Case, Stage  string
	AudioS       int
	Class        string
	TimeoutS     int
	TakeMicro    money.MicroUSD
	MaxMicro     money.MicroUSD // the take reserve plus one attempt overhead
	CeilingMicro money.MicroUSD // MaxMicro plus the retry allowance: what the estimate's ceiling is compared with
}

// BudgetCase is one case's worst case.
type BudgetCase struct {
	Case     CanaryCase
	Takes    []BudgetTake
	MaxMicro money.MicroUSD
	// CeilingMicro is what --max-usd is compared with, summed over the case's stages (estimate ceilings).
	CeilingMicro money.MicroUSD
}

// BudgetReport is the dry run: Σ max per case plus the init-fail window; no first-pull term (unbilled, D-E2-18).
type BudgetReport struct {
	Part          string
	Params        BudgetParams
	Cases         []BudgetCase
	InitFailMicro money.MicroUSD
	WorstMicro    money.MicroUSD
}

// CanaryWorstCase prices a part ("a" or "b") of the ACE-Step canary at the placeholder RTFs. It spends nothing and
// reads nothing.
func CanaryWorstCase(partName string, p BudgetParams) (BudgetReport, error) {
	if partName != "a" && partName != "b" {
		return BudgetReport{}, fmt.Errorf("runpod: canary: --part must be a or b, got %q", partName)
	}
	p = p.withDefaults()
	rep := BudgetReport{Part: partName, Params: p}
	opts := CanaryOptions{MaxS: p.MaxS, Pools: p.Pools}
	if p.SourceTrack {
		opts.SourceTrack = "operator-supplied"
	}
	for _, d := range aceCases {
		if string(d.part) != partName {
			continue
		}
		bc := BudgetCase{Case: d.name}
		for _, sg := range d.stages(opts) {
			if sg.skipWithSource && p.SourceTrack {
				continue
			}
			rtf, load := classRTF(sg.class, p.Pools)
			if sg.class == "" {
				return BudgetReport{}, fmt.Errorf("runpod: canary: stage %s/%s has no task class", d.name, sg.label)
			}
			maxRT := p.MaxRuntimeS
			if sg.maxRuntimeS > 0 {
				maxRT = sg.maxRuntimeS
			}
			q := spend.QuoteTakes(spend.QuoteParams{PricePPH: p.PricePPH, RTFMilli: rtf, ColdStartS: p.ColdStartS, IdleS: p.IdleS, WorkersMax: 1,
				MaxRuntimeS: maxRT, KillSlackS: p.KillSlackS}, []spend.TakeIn{{AudioS: sg.audioS, RTFMilli: rtf, LoadS: load + sg.extraLoadS}})
			t := q.Takes[0]
			bc.Takes = append(bc.Takes, BudgetTake{Case: string(d.name), Stage: sg.label, AudioS: sg.audioS, Class: sg.class,
				TimeoutS: t.TimeoutS, TakeMicro: t.TakeMicro, MaxMicro: t.MaxMicro, CeilingMicro: q.CeilingMicro})
			bc.MaxMicro += t.MaxMicro
			bc.CeilingMicro += q.CeilingMicro
		}
		rep.Cases = append(rep.Cases, bc)
		rep.WorstMicro += bc.MaxMicro
	}
	if partName == "a" {
		rep.InitFailMicro = money.ForSeconds(int64(p.InitFailWindow/time.Second), p.PricePPH)
		rep.WorstMicro += rep.InitFailMicro
	}
	return rep, nil
}

// Format renders the dry run (the golden of TestCanary_WorstCaseDryRun_Golden).
func (r BudgetReport) Format() string {
	var b strings.Builder
	price := "the given price"
	if r.Params.PricePPH == IllustrativePricePPH {
		price = "the ILLUSTRATIVE price, not a quote"
	}
	fmt.Fprintf(&b, "acestep15 canary, part %s: worst case at %s %d micro-USD/h (cold start %d s, idle %d s, kill slack %d s, max runtime %d s)\n",
		r.Part, price, r.Params.PricePPH, r.Params.ColdStartS, r.Params.IdleS, r.Params.KillSlackS, r.Params.MaxRuntimeS)
	fmt.Fprintf(&b, "placeholder RTFs (unsourced seeds), pools %s; no first-pull term (not billed)\n", strings.Join(r.Params.Pools, ","))
	for _, c := range r.Cases {
		fmt.Fprintf(&b, "case %-10s max %8d micro-USD (%s), --max-usd at least %s\n", c.Case, c.MaxMicro, money.MicroUSD(c.MaxMicro).USD(), money.MicroUSD(c.CeilingMicro).USD())
		for _, t := range c.Takes {
			fmt.Fprintf(&b, "  %-12s %4d s %-5s timeout %4d s reserve %7d + overhead %7d = %7d\n", t.Stage, t.AudioS, t.Class, t.TimeoutS,
				t.TakeMicro, t.MaxMicro-t.TakeMicro, t.MaxMicro)
		}
	}
	if r.InitFailMicro > 0 {
		fmt.Fprintf(&b, "init-fail window (%s) %d micro-USD\n", r.Params.InitFailWindow, r.InitFailMicro)
	}
	fmt.Fprintf(&b, "WORST CASE part %s: %d micro-USD (%s) reserved, not expected spend; approve it, then pass it as --max-usd per case\n",
		r.Part, r.WorstMicro, money.MicroUSD(r.WorstMicro).USD())
	return b.String()
}

// PlaceholderRTF returns {rtf_milli, load_s} of a class on a GPU class (tests pin it to the spend seed file).
func PlaceholderRTF(class, gpu string) [2]int {
	v := placeholderRTF[class][gpu]
	return [2]int{v.RTFMilli, v.LoadS}
}

// Only narrows a report to one case (the init-fail window stays with the part, and only when that case is init-fail).
func (r BudgetReport) Only(c CanaryCase) BudgetReport {
	var out BudgetReport = r
	out.Cases, out.WorstMicro, out.InitFailMicro = nil, 0, 0
	for _, bc := range r.Cases {
		if bc.Case == c {
			out.Cases = append(out.Cases, bc)
			out.WorstMicro += bc.MaxMicro
			if c == CaseInitFail {
				out.InitFailMicro = r.InitFailMicro
				out.WorstMicro += r.InitFailMicro
			}
		}
	}
	return out
}
