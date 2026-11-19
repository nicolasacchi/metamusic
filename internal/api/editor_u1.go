package api

import (
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
)

// Editor money rules (C305, C310.5). An Editor sees its own batches and jobs with their cost and everyone else's without cost
// or creator; its estimate shows no price, pool or cap; its budget is the approval rule and whether generation is on. The Owner
// side (the Owner, their devices, agents, connector grants, system) sees what it saw before U1: the fields are present, never
// zeroed, so the Owner's wire shape is unchanged. The omitted fields are nil pointers.

// ownsRow reports whether the Editor v created the batch whose creator key is owner.
func ownsRow(v auth.Principal, owner string) bool { return owner != "" && owner == v.Key() }

func redactBatchSummary(v auth.Principal, owner string, b *BatchSummary) {
	if v.IsOwnerSide() || ownsRow(v, owner) {
		return
	}
	b.Cost, b.CreatedBy = nil, nil
}

func redactBatch(v auth.Principal, owner string, b *Batch) {
	redactBatchSummary(v, owner, &b.BatchSummary)
	for i := range b.Jobs {
		redactJobSummary(v, owner, &b.Jobs[i])
	}
}

func redactJobSummary(v auth.Principal, owner string, j *JobSummary) {
	if v.IsOwnerSide() || ownsRow(v, owner) {
		return
	}
	j.Cost = nil
}

// redactJob also drops the run environment (GPU class, image digest, backend) for a viewer that is neither Owner-side nor the
// job's creator (C310.5: an Editor never sees GPU pools).
func redactJob(v auth.Principal, owner string, j *Job) {
	redactJobSummary(v, owner, &j.JobSummary)
	if !v.IsOwnerSide() && !ownsRow(v, owner) {
		j.Env = nil
	}
}

// redactEstimate drops what an Editor never sees of a quote: the price, the GPU pools, the month and day pots, the batch cap and
// the provider's armed and tripped flags. Its own expected, max and ceiling costs stay (C310.5).
func redactEstimate(v auth.Principal, e Estimate) Estimate {
	if v.IsOwnerSide() {
		return e
	}
	e.Price = nil
	e.Budget.Month, e.Budget.Day, e.Budget.BatchCapMicroUSD, e.Budget.BatchCapUSD = nil, nil, nil, nil
	e.Provider.Armed, e.Provider.Tripped = nil, nil
	e.Budget.WouldExceed = []string{} // which pot a quote would overrun is the pot's state; Fits alone tells the Editor
	return e
}

// budgetBrief is get_budget for an Editor.
func budgetBrief(b generation.Brief) Budget {
	p := providerBrief(b.Provider)
	p.Armed, p.Tripped = nil, nil
	req := b.ApprovalRequired
	return Budget{Provider: p, ApprovalRequired: &req}
}
