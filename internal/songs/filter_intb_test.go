package songs_test

import (
	"context"
	"sort"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/songs"
)

// TestSongs_InRecordListener (U1-INT-B): for a Listener in_record counts the published, non-hidden Records only, so the
// contents of a draft Record are no oracle; the Owner's filter is unchanged.
func TestSongs_InRecordListener(t *testing.T) {
	t.Parallel()
	w := newL2w(t)
	yes, no := true, false
	carol := auth.WithPrincipal(context.Background(), auth.Principal{Kind: auth.KindHuman, ID: "carol", User: "carol", Role: auth.RoleListener})
	labels := func(ctx context.Context, f songs.ListFilter) []string {
		f.Limit = 100
		p, err := w.svc.List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		byID := map[string]string{}
		for l, id := range w.fx.Songs {
			byID[id] = l
		}
		var out []string
		for _, it := range p.Items {
			if l, ok := byID[it.ID]; ok {
				out = append(out, l)
			}
		}
		sort.Strings(out)
		return out
	}
	// R1 (published): S2 S3 S4 S5 S7; R2 (draft): S1 S6
	eq(t, "owner in_record=true", labels(context.Background(), songs.ListFilter{InRecord: &yes}), "S1", "S2", "S3", "S4", "S5", "S6", "S7")
	eq(t, "listener in_record=true", labels(carol, songs.ListFilter{InRecord: &yes}), "S2", "S3", "S4", "S5", "S7")
	eq(t, "listener in_record=false", labels(carol, songs.ListFilter{InRecord: &no}), "S1", "S6")
}
