package tape_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// insertTake adds a minimal take row (foreign keys are off for the one writer connection; only the columns Get reads matter).
func (r *rig) insertTake(id, status string) {
	r.t.Helper()
	r.exec(`PRAGMA foreign_keys = OFF`)
	r.exec(`INSERT INTO tracks(id, slug, title, recipe_id, root_track_id, generation, status, status_set_by, origin, duration_s, sample_rate,
		bits_per_sample, channels, total_samples, master_path, master_sha256, master_bytes, source_sha256, frames_sha256, audio_md5,
		import_rule, recorded_at, created_at)
		VALUES (?1, ?1, ?1, 'r', ?1, 0, ?2, 'import', 'import', 1, 48000, 24, 2, 48000, 'p/'||?1, 's'||?1, 1, 'x', 'f'||?1, 'm', 'job',
		'2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, id, status)
}

// TestTape_GetHidesDiscardedFromListeners: a tape whose plan names a discarded take is not found for a Listener (C308.2); the
// Owner reads it as before, and a Listener still reads a tape of live takes.
func TestTape_GetHidesDiscardedFromListeners(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	tp := r.mustExport(tape.ExportInput{})
	if len(tp.Items) != 3 {
		t.Fatalf("items = %d", len(tp.Items))
	}
	for i := 1; i <= 3; i++ {
		r.insertTake(fmt.Sprintf("take-%d", i), "final")
	}
	as := func(role auth.Role) context.Context {
		return auth.WithPrincipal(context.Background(), auth.Principal{Kind: auth.KindHuman, ID: "u", User: "u", Name: "u", Role: role, Scopes: auth.ScopesFor(role)})
	}
	owner, listener := as(auth.RoleOwner), as(auth.RoleListener)
	for name, ctx := range map[string]context.Context{"owner": owner, "listener": listener, "bare": context.Background()} {
		if got, err := r.svc.Get(ctx, tp.ID); err != nil || got.ID != tp.ID {
			t.Errorf("%s, live takes: %+v %v", name, got.ID, err)
		}
	}
	r.exec(`UPDATE tracks SET status = 'discarded' WHERE id = 'take-2'`)
	for name, ctx := range map[string]context.Context{"owner": owner, "bare": context.Background()} {
		if got, err := r.svc.Get(ctx, tp.ID); err != nil || len(got.Items) != 3 {
			t.Errorf("%s, discarded take: %d items, %v", name, len(got.Items), err)
		}
	}
	_, err := r.svc.Get(listener, tp.ID)
	wantProblem(t, err, 404, "NOT_FOUND")
	// restoring the take brings the tape back
	r.exec(`UPDATE tracks SET status = 'final' WHERE id = 'take-2'`)
	if _, err := r.svc.Get(listener, tp.ID); err != nil {
		t.Errorf("listener after the take is restored: %v", err)
	}
}
