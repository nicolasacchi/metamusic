package generation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

var (
	bobEditor = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleEditor, ID: "bob", Name: "bob", User: "bob", Via: "proxy", Scopes: auth.ScopesFor(auth.RoleEditor)}
	bobPhone  = auth.Principal{Kind: auth.KindDevice, Role: auth.RoleEditor, ID: "dev-bob", Name: "bob phone", User: "bob", Via: "device", Scopes: auth.ScopesFor(auth.RoleEditor)}
)

func problemCode(err error) string {
	var pe *problem.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func problemDetails(err error) map[string]any {
	var pe *problem.Error
	if errors.As(err, &pe) {
		return pe.Details
	}
	return nil
}

// TestEditorBatch_AlwaysAwaitsOwner (D-U1-11, C310.1/2): an Editor's create waits for the Owner whatever the path claimed
// (human_create is ignored) and whatever the auto-approve limits are; the estimate says so up front.
func TestEditorBatch_AlwaysAwaitsOwner(t *testing.T) {
	t.Parallel()
	caps := spend.DefaultCaps
	caps.AutoApproveTakes, caps.AutoApproveMicro = 50, 1_000_000_000 // far over anything the batch needs
	r := newRig(t, withCaps(caps))
	for _, p := range []auth.Principal{bobEditor, bobPhone} {
		e := r.est(p, newSeedItem(lt.TrackLuna, 1))
		if !e.RequiresApproval || !contains(e.ApprovalReasons, "editor_batch") {
			t.Errorf("%s estimate: requires %v reasons %v", p.Kind, e.RequiresApproval, e.ApprovalReasons)
		}
		for _, via := range []string{spend.ViaHumanCreate, spend.ViaAPI} {
			e := r.est(p, newSeedItem(lt.TrackLuna, 1))
			c := r.mustCreate(p, e, via)
			if c.State != "awaiting_confirmation" || c.ApprovalState != "pending" || c.Via != "api" || !contains(c.Approval.Reasons, "editor_batch") {
				t.Errorf("%s via %s: state %s approval %s via %s reasons %v", p.Kind, via, c.State, c.ApprovalState, c.Via, c.Approval.Reasons)
			}
			if got := r.str(`SELECT DISTINCT state FROM jobs WHERE batch_id = ?`, c.BatchID); got != "awaiting_confirmation" {
				t.Errorf("%s via %s: job state %s", p.Kind, via, got)
			}
		}
	}
	// the Owner's own flow is unchanged next to it
	c := r.mustCreate(human, r.est(human, newSeedItem(lt.TrackLuna, 1)), spend.ViaHumanCreate)
	if c.State != "active" || c.ApprovalState != "approved" {
		t.Errorf("owner human_create: %s %s", c.State, c.ApprovalState)
	}
}

// TestEditorBatch_OnlyOwnerDecides (C310.3): an Editor can neither approve nor reject, even their own batch; the Owner can.
func TestEditorBatch_OnlyOwnerDecides(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	c := r.mustCreate(bobEditor, r.est(bobEditor, newSeedItem(lt.TrackLuna, 1)), spend.ViaAPI)
	if _, err := r.svc.Approve(ctx, bobEditor, c.BatchID, "self"); problemCode(err) != "OWNER_ONLY" {
		t.Errorf("editor approve: %v", err)
	}
	if _, err := r.svc.Reject(ctx, bobEditor, c.BatchID, "self"); problemCode(err) != "OWNER_ONLY" {
		t.Errorf("editor reject: %v", err)
	}
	if got := r.str(`SELECT state FROM approvals WHERE batch_id = ?`, c.BatchID); got != "pending" {
		t.Fatalf("approval moved to %s", got)
	}
	// only the creator asks for approval
	if _, err := r.svc.RequestApproval(ctx, auth.Principal{Kind: auth.KindHuman, Role: auth.RoleEditor, ID: "dave", Scopes: auth.ScopesFor(auth.RoleEditor)}, c.BatchID); problemCode(err) != "NOT_BATCH_OWNER" {
		t.Errorf("another editor asks: %v", err)
	}
	if _, err := r.svc.RequestApproval(ctx, bobEditor, c.BatchID); err != nil {
		t.Errorf("creator asks: %v", err)
	}
	got, err := r.svc.Approve(ctx, human, c.BatchID, "ok")
	if err != nil || got.State != "active" || got.Approval.DecidedBy != human.Key() {
		t.Errorf("owner approve: %+v %v", got.State, err)
	}
}

// TestEditorBatch_CapRefusalIsReduced (C310.5): the BUDGET_EXCEEDED an Editor gets carries a code and a message and no
// amounts; the Owner's keeps its details.
func TestEditorBatch_CapRefusalIsReduced(t *testing.T) {
	t.Parallel()
	caps := spend.DefaultCaps
	caps.HumanDailyMicro = 1
	r := newRig(t, withCaps(caps))
	_, errEd := r.create(bobEditor, r.est(bobEditor, newSeedItem(lt.TrackLuna, 1)), spend.ViaAPI)
	if problemCode(errEd) != "BUDGET_EXCEEDED" {
		t.Fatalf("editor: %v", errEd)
	}
	if d := problemDetails(errEd); len(d) != 0 {
		t.Errorf("editor details leak: %v", d)
	}
	_, errOwner := r.create(human, r.est(human, newSeedItem(lt.TrackLuna, 1)), spend.ViaAPI)
	if problemCode(errOwner) != "BUDGET_EXCEEDED" {
		t.Fatalf("owner: %v", errOwner)
	}
	if d := problemDetails(errOwner); d["limit_micro_usd"] == nil || d["cap"] == nil {
		t.Errorf("owner details lost: %v", d)
	}
}

// TestEditorBrief (C305): the Editor's budget view is the approval flag and the provider's enabled state, nothing else.
func TestEditorBrief(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	b, err := r.svc.Brief(context.Background(), bobEditor)
	if err != nil {
		t.Fatal(err)
	}
	if !b.ApprovalRequired || !b.Provider.Enabled {
		t.Errorf("brief = %+v", b)
	}
}

type fixedBacklog struct{ n, limit int }

func (f fixedBacklog) Backlog(context.Context, lineage.Querier, string) (int, error) { return f.n, nil }
func (f fixedBacklog) Limit() int                                                    { return f.limit }

// TestEditorBacklogGate (C303 IsAgent table): the callbacks backlog gate binds Editors like agents; the Owner passes.
func TestEditorBacklogGate(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.opts.BacklogGate = fixedBacklog{n: 6, limit: 6}
	svc := generation.New(r.opts)
	est := func(p auth.Principal) error {
		_, err := svc.Estimate(context.Background(), p, generation.EstimateIn{Items: []generation.Item{newSeedItem(lt.TrackLuna, 1)}, Via: spend.ViaAPI})
		return err
	}
	if c := problemCode(est(bobEditor)); c != "CALLBACKS_BACKLOG" {
		t.Errorf("editor: %q", c)
	}
	if c := problemCode(est(agent)); c != "CALLBACKS_BACKLOG" {
		t.Errorf("agent: %q", c)
	}
	if err := est(human); err != nil {
		t.Errorf("owner: %v", err)
	}
}

// TestEditorApprovalReadIsOwn (C305): get_approval_status shows an Editor its own batches' approvals; another's is NOT_FOUND.
func TestEditorApprovalReadIsOwn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := context.Background()
	mine := r.mustCreate(bobEditor, r.est(bobEditor, newSeedItem(lt.TrackLuna, 1)), spend.ViaAPI)
	theirs := r.mustCreate(agent, r.est(agent, newSeedItem(lt.TrackCitta, 6)), spend.ViaAPI)
	if v, err := r.svc.Approval(ctx, bobEditor, mine.BatchID); err != nil || v.State != "pending" {
		t.Errorf("own: %+v %v", v, err)
	}
	if _, err := r.svc.Approval(ctx, bobEditor, theirs.BatchID); problemCode(err) != "NOT_FOUND" {
		t.Errorf("another's: %v", err)
	}
	if v, err := r.svc.Approval(ctx, human, mine.BatchID); err != nil || v.State != "pending" {
		t.Errorf("owner reads an Editor's: %+v %v", v, err)
	}
}
