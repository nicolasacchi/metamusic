package transform_test

import (
	"context"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/transform"
)

var (
	bobEditor = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleEditor, ID: "bob", Name: "bob", User: "bob", Via: "proxy", Scopes: auth.ScopesFor(auth.RoleEditor)}
	aliceOwn  = auth.Principal{Kind: auth.KindHuman, Role: auth.RoleOwner, ID: "alice", Name: "alice", User: "alice", Via: "proxy", Scopes: auth.ScopesFor(auth.RoleOwner)}
)

func withUsers(o *transform.Options) {
	o.Config.Users = map[string]config.Role{"alice": config.RoleOwner, "bob": config.RoleEditor}
}

// TestCancel_EditorOnlyOwn (C305, D-U1-18): an Editor cancels only a transform it created; the Owner cancels any.
func TestCancel_EditorOnlyOwn(t *testing.T) {
	r := newRig(t, false, withUsers)
	ctx := context.Background()
	other := r.queue(qspec{principal: agentA, tag: "a"})
	owners := r.queue(qspec{principal: aliceOwn, tag: "o"})
	mine := r.queue(qspec{principal: bobEditor, tag: "m"})
	for _, id := range []string{other, owners} {
		pe := problemOf(t, r.svc.Cancel(ctx, bobEditor, id))
		if pe.Status != 403 || pe.Code != "FORBIDDEN" {
			t.Fatalf("editor cancelling another's transform: %+v", pe)
		}
		if r.row(id).State != "queued" {
			t.Fatal("a refused cancel changed the row")
		}
	}
	if err := r.svc.Cancel(ctx, bobEditor, mine); err != nil || r.row(mine).State != "cancelled" {
		t.Fatalf("own cancel: %v %+v", err, r.row(mine))
	}
	if err := r.svc.Cancel(ctx, aliceOwn, owners); err != nil || r.row(owners).State != "cancelled" {
		t.Fatalf("the Owner's cancel: %v", err)
	}
}

// TestQuota_EditorPoolIsItsOwn (D-U1-18): Editors draw from a pool of their own: an Editor's reservation never reduces the
// Owner's human pool (and the other way round), and the agent pool is untouched.
func TestQuota_EditorPoolIsItsOwn(t *testing.T) {
	r := newRig(t, false, withUsers)
	ctx := context.Background()
	if k := transform.QuotaKind(bobEditor); k != "editor" {
		t.Fatalf("QuotaKind(editor) = %q", k)
	}
	if k := transform.QuotaKind(aliceOwn); k != "human" {
		t.Fatalf("QuotaKind(owner) = %q", k)
	}
	r.queue(qspec{principal: bobEditor, tag: "e", maxCPU: 14_000_000})
	if q := quotaOf(t, r, "editor"); q.ReservedMS != 14_000_000 || q.CapMS != 14_400_000 {
		t.Errorf("editor pool = %+v", q)
	}
	if q := quotaOf(t, r, "human"); q.ReservedMS != 0 {
		t.Errorf("the Owner's pool was charged for an Editor: %+v", q)
	}
	if err := r.svc.CheckQuota(ctx, r.st.R, transform.QuotaKind(aliceOwn), 14_000_000); err != nil {
		t.Errorf("the Owner is refused while only an Editor used a pool: %v", err)
	}
	if err := r.svc.CheckQuota(ctx, r.st.R, transform.QuotaKind(bobEditor), 1_000_000); err == nil {
		t.Error("the Editor pool has no cap")
	}
	r.queue(qspec{principal: aliceOwn, tag: "o", maxCPU: 10_000_000})
	if q := quotaOf(t, r, "editor"); q.ReservedMS != 14_000_000 {
		t.Errorf("the Editor pool was charged for the Owner: %+v", q)
	}
	if q := quotaOf(t, r, "agent"); q.ReservedMS != 0 {
		t.Errorf("agent pool = %+v", q)
	}
}
