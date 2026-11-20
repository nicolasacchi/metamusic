package audit

import (
	"strings"
	"testing"
	"time"
)

func TestAddSystemOps_Validation(t *testing.T) {
	t.Parallel()
	r := &opRegistry{set: map[string]bool{}}
	r.add([]string{"night:run", "night:run", "reconciler:trip", "BadOp", "night:", "Night:x"})
	if len(r.ops) != 1 || r.ops[0] != "night:run" {
		t.Errorf("accepted %v, want only night:run", r.ops)
	}
	if len(r.errs) != 5 {
		t.Fatalf("errors = %v, want 5 (duplicate, base duplicate, three malformed)", r.errs)
	}
	for _, e := range r.errs {
		if !strings.HasPrefix(e.Error(), "audit: system op") {
			t.Errorf("error text %q", e)
		}
	}
}

func TestAddSystemOps_BaseListUntouchedAndEntryBuilds(t *testing.T) {
	// not parallel: it swaps the package registry
	if len(SystemOps) != 24 {
		t.Fatalf("SystemOps changed: %d", len(SystemOps))
	}
	if err := ValidateSystemOps(); err != nil {
		t.Fatalf("no track has registered a bad op: %v", err)
	}
	// a registered op is a system op and builds an entry; the actor decides the principal
	local := &opRegistry{set: map[string]bool{}}
	local.add([]string{"night:run"})
	prev := extra
	extra = local
	defer func() { extra = prev }()
	if !IsSystemOp("night:run") || len(AllSystemOps()) != 25 {
		t.Fatal("registered op not visible")
	}
	e, err := SystemEntry("night:run", time.Unix(0, 0), map[string]any{"a": 1}, 0, 0, false)
	if err != nil || e.Op != "night:run" || e.Method != SystemMethod {
		t.Fatalf("entry = %+v, %v", e, err)
	}
}
