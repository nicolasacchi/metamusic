package library_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// TestSearchTracks_InRecordListener (U1-INT-B): for a Listener in_record counts the Takes of published, non-hidden Records
// only; a draft Record's contents are no oracle. The Owner's filter is unchanged.
func TestSearchTracks_InRecordListener(t *testing.T) {
	t.Parallel()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "l2.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fx := lt.FixtureL2(t, st, lt.FixedNow)
	svc := library.New(st, nil, func() time.Time { return lt.FixedNow })
	yes, no := true, false
	mine := map[string]string{}
	for l, id := range fx.Takes {
		mine[id] = l
	}
	carol := auth.WithPrincipal(context.Background(), auth.Principal{Kind: auth.KindHuman, ID: "carol", User: "carol", Role: auth.RoleListener})
	labels := func(ctx context.Context, in *bool) []string {
		t.Helper()
		page, err := svc.SearchTracks(ctx, library.SearchParams{InRecord: in, Status: allStatuses, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, id := range ids(page) {
			if l, ok := mine[id]; ok {
				out = append(out, l)
			}
		}
		return out
	}
	// R1 (published): a1 b1 c2 e1; R2 (draft): s1a s6a
	sameSet(t, "owner", labels(context.Background(), &yes), "a1", "b1", "c2", "e1", "s1a", "s6a")
	sameSet(t, "listener true", labels(carol, &yes), "a1", "b1", "c2", "e1")
	sameSet(t, "listener false", labels(carol, &no), "b2", "c1", "s1a", "s6a")
}
