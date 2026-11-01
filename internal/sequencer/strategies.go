package sequencer

// Spec is one strategy: a target curve and a weight row (extension move 4, D-L3-40). A new strategy is a new row here
// and in Strategies, plus the golden.
type Spec struct {
	Strategy Strategy `json:"strategy"`
	Curve    string   `json:"curve"`
	Weights  Weights  `json:"weights"`
}

// Target curve ids.
const (
	CurveArc    = "arc_peak_70"
	CurveLinear = "linear_up"
	CurveNone   = "none"
)

var steadySpec = Spec{StrategySteady, CurveNone, Weights{Tempo: 100, Key: 80, Energy: 80, Shape: 0}}

// Specs holds the specs in the order of Strategies (the default first).
var Specs = []Spec{
	{StrategyArc, CurveArc, Weights{Tempo: 100, Key: 80, Energy: 50, Shape: 150}},
	{StrategyRampUp, CurveLinear, Weights{Tempo: 100, Key: 80, Energy: 30, Shape: 150}},
	steadySpec,
}

// SpecOf returns the spec of a strategy.
func SpecOf(s Strategy) (Spec, bool) {
	for _, sp := range Specs {
		if sp.Strategy == s {
			return sp, true
		}
	}
	return Spec{}, false
}
