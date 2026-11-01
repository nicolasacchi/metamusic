package sequencer

// Constants of the sequencer (L3 C133). Costs are integer micro-units; M is one whole unit.
const (
	MaxSongs            = 60
	ExactMax            = 12
	BeamWidth           = 8
	MaxLocalPasses      = 50
	TempoSaturateMicro  = 200_000
	EnergySaturateMicro = 400_000
	NeutralMicro        = 500_000
	ShapeNeutralMicro   = 250_000
	M                   = 1_000_000
	ExplanationDef      = "mm.sequence/1"
	CostUnitsPerWholeJ  = 10_000_000_000 // J is reported as J / 1e10 with 4 decimals
	NoEnergy            = -1             // Song.Energyx10000 for an unknown energy
)

// Strategies is the registry of allowed strategies (extension move 4, D-L3-40); the default is first.
var Strategies = []Strategy{StrategyArc, StrategyRampUp, StrategySteady}
