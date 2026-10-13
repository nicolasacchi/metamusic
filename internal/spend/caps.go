package spend

import "github.com/nicolasacchi/metamusic/internal/money"

// Caps are the global spend limits (C72.3). Every amount is integer micro-USD.
type Caps struct {
	MonthlyMicro, BatchMicro, HumanDailyMicro, AgentDailyMicro money.MicroUSD
	AutoApproveTakes                                           int
	AutoApproveMicro                                           money.MicroUSD
}

// DefaultCaps are the migration defaults (D-M3-7): $10 a month, $5 a batch, $5 a day for the operator,
// $1 a day for agents, auto-approve up to 5 takes and $0.50.
var DefaultCaps = Caps{MonthlyMicro: 10_000_000, BatchMicro: 5_000_000, HumanDailyMicro: 5_000_000, AgentDailyMicro: 1_000_000,
	AutoApproveTakes: 5, AutoApproveMicro: 500_000}
