package acceptance_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestTrackContext_IncludeScoreReturnsABC: get_track_context include=score answers the take's score.abc text (D-DESK-16). Before the
// reader was wired into trackctx this was always null.
func TestTrackContext_IncludeScoreReturnsABC(t *testing.T) {
	r := genRig(t, archivetest.SyntheticArchive(t, "acceptance"), servertest.Options{})
	tok, _, err := r.Services.Tokens.Create(context.Background(), "score-agent", []auth.Scope{auth.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	sess := mcpWithToken(t, r, tok)
	var id string
	if err := r.Store.R.QueryRow(`SELECT track_id FROM scores ORDER BY track_id LIMIT 1`).Scan(&id); err != nil {
		t.Skipf("the synthetic archive has no score: %v", err)
	}
	plain := obj(obj(callTool(t, sess, "get_track_context", map[string]any{"id": id}, false))["score"])
	if _, has := plain["abc"]; has {
		t.Error("score.abc without include=score")
	}
	withABC := obj(obj(callTool(t, sess, "get_track_context", map[string]any{"id": id, "include": []string{"score"}}, false))["score"])
	abc, _ := withABC["abc"].(string)
	if !strings.Contains(abc, "X:") && !strings.Contains(abc, "K:") {
		t.Errorf("score.abc = %v, want the ABC text", withABC["abc"])
	}
}
