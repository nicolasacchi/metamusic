package sounds_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/sounds"
)

// proposeThree stores a proposal of three mappings by agentP over a library with a skank Song.
func proposeThree(t *testing.T, w *world) (sounds.Proposal, string) {
	t.Helper()
	d := w.draft("skank, wobble 3, 90bpm")
	w.settle()
	p, err := w.sv.Propose(context.Background(), agentP, "synthetic rationale", []sounds.Mapping{
		me("skank", "genre_family", "reggae", "add"), me("zouk love", "genre_family", "world", "add"), me("wobble 3", "mood", "chill", "add"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return p, w.songOfDraft(d)
}

func TestDecide_Accept_All(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	p, song := proposeThree(t, w)
	res, err := w.sv.Decide(ctx, humanP, p.ID, sounds.Decision{Decision: "accept"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied == nil || res.Applied.Version == nil || *res.Applied.Version != 2 || res.Applied.MappingsApplied != 3 || res.Applied.RecomputedSongs != 1 {
		t.Errorf("applied %+v", res.Applied)
	}
	if res.Proposal.State != "accepted" || res.Proposal.AppliedVersion == nil || *res.Proposal.AppliedVersion != 2 || res.Proposal.DecidedBy == nil || *res.Proposal.DecidedBy != "human:op" {
		t.Errorf("proposal %+v", res.Proposal)
	}
	if string(res.Proposal.Accepted) != "[0,1,2]" || string(res.Proposal.Skipped) != "[]" || len(res.Skipped) != 0 {
		t.Errorf("accepted %s skipped %s", res.Proposal.Accepted, res.Proposal.Skipped)
	}
	if got := w.facetValues("song_sounds", "song_id", song, "genre_family"); len(got) != 1 || got[0] != "reggae" {
		t.Errorf("song genre_family %v", got)
	}
	// the stored row reads back as returned
	page, err := w.sv.ListProposals(ctx, sounds.ProposalFilter{States: []string{"accepted"}})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != p.ID || page.Items[0].State != "accepted" {
		t.Errorf("list %+v %v", page, err)
	}
}

func TestDecide_Accept_Subset(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	p, song := proposeThree(t, w)
	res, err := w.sv.Decide(ctx, humanP, p.ID, sounds.Decision{Decision: "accept", AcceptMappings: []int{2, 0}, Note: "only these"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied.MappingsApplied != 2 || string(res.Proposal.Accepted) != "[0,2]" {
		t.Errorf("applied %+v accepted %s", res.Applied, res.Proposal.Accepted)
	}
	if w.count(`SELECT COUNT(*) FROM sound_terms WHERE version = 2`) != 2 || w.count(`SELECT COUNT(*) FROM sound_terms WHERE term = 'zouk love'`) != 0 {
		t.Errorf("the unaccepted mapping was applied")
	}
	if got := w.facetValues("song_sounds", "song_id", song, "mood"); len(got) != 1 || got[0] != "chill" {
		t.Errorf("song mood %v", got)
	}
	if res.Proposal.DecidedNote == nil || *res.Proposal.DecidedNote != "only these" {
		t.Errorf("note %v", res.Proposal.DecidedNote)
	}
	// the version note is the proposal id, not the human's note
	if got := w.str(`SELECT note FROM sound_taxonomy_versions WHERE version = 2`); got != "proposal "+p.ID {
		t.Errorf("version note %q", got)
	}
}

func TestDecide_Validation(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	p, _ := proposeThree(t, w)
	long := make([]byte, 301)
	for i := range long {
		long[i] = 'n'
	}
	for _, c := range []struct {
		name   string
		id     string
		d      sounds.Decision
		status int
		code   string
	}{
		{"unknown id", "nope", sounds.Decision{Decision: "reject"}, 404, "NOT_FOUND"},
		{"bad decision", p.ID, sounds.Decision{Decision: "maybe"}, 422, "VALIDATION_FAILED"},
		{"index out of range", p.ID, sounds.Decision{Decision: "accept", AcceptMappings: []int{3}}, 422, "VALIDATION_FAILED"},
		{"negative index", p.ID, sounds.Decision{Decision: "accept", AcceptMappings: []int{-1}}, 422, "VALIDATION_FAILED"},
		{"duplicate index", p.ID, sounds.Decision{Decision: "accept", AcceptMappings: []int{1, 1}}, 422, "VALIDATION_FAILED"},
		{"empty subset", p.ID, sounds.Decision{Decision: "accept", AcceptMappings: []int{}}, 422, "VALIDATION_FAILED"},
		{"long note", p.ID, sounds.Decision{Decision: "reject", Note: string(long)}, 422, "VALIDATION_FAILED"},
	} {
		_, err := w.sv.Decide(ctx, humanP, c.id, c.d)
		wantProblem(t, c.name, err, c.status, c.code)
	}
	if w.str(`SELECT state FROM taxonomy_proposals WHERE id = ?`, p.ID) != "open" || w.count(`SELECT COUNT(*) FROM sound_taxonomy_versions`) != 1 {
		t.Errorf("a refused decision changed state")
	}
}

func TestDecide_Stale(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	p, _ := proposeThree(t, w)
	// the first mapping becomes effective meanwhile
	if _, err := w.sv.Map(ctx, humanP, sounds.MapRequest{Mappings: []sounds.MapEntry{me("skank", "genre_family", "reggae", "add")}}); err != nil {
		t.Fatal(err)
	}
	res, err := w.sv.Decide(ctx, humanP, p.ID, sounds.Decision{Decision: "accept"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != (sounds.Skipped{Index: 0, Reason: "stale"}) || res.Applied.MappingsApplied != 2 {
		t.Errorf("skipped %+v applied %+v", res.Skipped, res.Applied)
	}
	var sk []sounds.Skipped
	if err := json.Unmarshal(res.Proposal.Skipped, &sk); err != nil || len(sk) != 1 || string(res.Proposal.Accepted) != "[1,2]" {
		t.Errorf("stored skipped %s accepted %s", res.Proposal.Skipped, res.Proposal.Accepted)
	}

	// every accepted mapping stale: expired, ErrTaxonomyStale, no version
	w2 := newWorld(t, true)
	p2, _ := proposeThree(t, w2)
	if _, err := w2.sv.Map(ctx, humanP, sounds.MapRequest{Mappings: []sounds.MapEntry{me("skank", "genre_family", "reggae", "add")}}); err != nil {
		t.Fatal(err)
	}
	versions := w2.count(`SELECT COUNT(*) FROM sound_taxonomy_versions`)
	res2, err := w2.sv.Decide(ctx, humanP, p2.ID, sounds.Decision{Decision: "accept", AcceptMappings: []int{0}})
	if !errors.Is(err, sounds.ErrTaxonomyStale) {
		t.Fatalf("err %v, want ErrTaxonomyStale", err)
	}
	if res2.Proposal.State != "expired" || res2.Proposal.DecidedBy == nil || *res2.Proposal.DecidedBy != "system:sounds" || res2.Applied != nil || len(res2.Skipped) != 1 {
		t.Errorf("result %+v", res2)
	}
	if w2.count(`SELECT COUNT(*) FROM sound_taxonomy_versions`) != versions {
		t.Errorf("a stale decision wrote a version")
	}
}

func TestDecide_ExpiredRowPersists(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	p, _ := proposeThree(t, w)
	if _, err := w.sv.Map(ctx, humanP, sounds.MapRequest{Mappings: []sounds.MapEntry{me("skank", "genre_family", "reggae", "add")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.sv.Decide(ctx, humanP, p.ID, sounds.Decision{Decision: "accept", AcceptMappings: []int{0}}); !errors.Is(err, sounds.ErrTaxonomyStale) {
		t.Fatal(err)
	}
	if got := w.str(`SELECT state || '/' || decided_by || '/' || COALESCE(accepted_json,'null') || '/' || skipped_json FROM taxonomy_proposals WHERE id = ?`, p.ID); got != `expired/system:sounds/null/[{"index":0,"reason":"stale"}]` {
		t.Errorf("row %q", got)
	}
	_, err := w.sv.Decide(ctx, humanP, p.ID, sounds.Decision{Decision: "reject"})
	wantProblem(t, "decide an expired proposal", err, 409, "TAXONOMY_PROPOSAL_CLOSED")
}

func TestDecide_Reject(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	p, _ := proposeThree(t, w)
	res, err := w.sv.Decide(ctx, deviceP, p.ID, sounds.Decision{Decision: "reject", Note: "no thanks"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Proposal.State != "rejected" || res.Applied != nil || res.Proposal.AppliedVersion != nil || res.Proposal.DecidedAt == nil || res.Proposal.Accepted != nil || res.Proposal.Skipped != nil ||
		res.Proposal.DecidedBy == nil || *res.Proposal.DecidedBy != "device:dev-1" || res.Proposal.DecidedNote == nil || *res.Proposal.DecidedNote != "no thanks" {
		t.Errorf("result %+v", res)
	}
	if w.count(`SELECT COUNT(*) FROM sound_taxonomy_versions`) != 1 {
		t.Errorf("a rejection wrote a version")
	}
}

func TestDecide_Closed(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	p, _ := proposeThree(t, w)
	if _, err := w.sv.Decide(ctx, humanP, p.ID, sounds.Decision{Decision: "reject"}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"accept", "reject"} {
		_, err := w.sv.Decide(ctx, humanP, p.ID, sounds.Decision{Decision: d})
		wantProblem(t, d+" a rejected proposal", err, 409, "TAXONOMY_PROPOSAL_CLOSED")
	}
}

func TestDecide_HumanOnly(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	ctx := context.Background()
	p, _ := proposeThree(t, w)
	connector := agentN(3)
	connector.Via = "connector"
	// the refusal comes before any read: an unknown id is still 403 for an agent
	for _, id := range []string{p.ID, "nope"} {
		_, err := w.sv.Decide(ctx, agentP, id, sounds.Decision{Decision: "reject"})
		pe := wantProblem(t, "agent decide "+id, err, 403, "HUMAN_ONLY")
		if pe != nil && pe.Details["hint"] == nil {
			t.Errorf("no hint: %+v", pe.Details)
		}
		_, err = w.sv.Decide(ctx, connector, id, sounds.Decision{Decision: "reject"})
		wantProblem(t, "connector decide "+id, err, 403, "HUMAN_ONLY")
	}
	if w.str(`SELECT state FROM taxonomy_proposals WHERE id = ?`, p.ID) != "open" {
		t.Errorf("an agent decided")
	}
	if _, err := w.sv.Decide(ctx, deviceP, p.ID, sounds.Decision{Decision: "reject"}); err != nil {
		t.Errorf("paired device: %v", err)
	}
}

func TestDecide_VersionNote(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	p, _ := proposeThree(t, w)
	if _, err := w.sv.Decide(context.Background(), humanP, p.ID, sounds.Decision{Decision: "accept"}); err != nil {
		t.Fatal(err)
	}
	if got := w.str(`SELECT kind || '/' || created_by || '/' || note FROM sound_taxonomy_versions WHERE version = 2`); got != "extension/human:op/proposal "+p.ID {
		t.Errorf("version row %q", got)
	}
}

func TestDecide_Atomic(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	p, _ := proposeThree(t, w)
	w.exec(`CREATE TRIGGER fault_proposal_update BEFORE UPDATE ON taxonomy_proposals BEGIN SELECT RAISE(ABORT, 'injected fault'); END`)
	if _, err := w.sv.Decide(context.Background(), humanP, p.ID, sounds.Decision{Decision: "accept"}); err == nil {
		t.Fatal("expected the injected fault")
	}
	if w.count(`SELECT COUNT(*) FROM sound_taxonomy_versions`) != 1 || w.count(`SELECT COUNT(*) FROM sound_terms WHERE version = 2`) != 0 {
		t.Errorf("the version survived a failed proposal update")
	}
	if w.str(`SELECT state FROM taxonomy_proposals WHERE id = ?`, p.ID) != "open" {
		t.Errorf("proposal state changed")
	}
}

func TestDecide_RefreshesFeaturesInPlace(t *testing.T) {
	t.Parallel()
	w := newWorld(t, true)
	d := w.draft("skank, 90bpm")
	w.settle()
	song := w.songOfDraft(d)
	p, err := w.sv.Propose(context.Background(), agentP, "why", []sounds.Mapping{me("skank", "genre_family", "reggae", "add")})
	if err != nil {
		t.Fatal(err)
	}
	_, hook := recordingHook(t, w, 2, 0)
	w.sv.SetRecomputeHook(hook)
	if _, err := w.sv.Decide(context.Background(), humanP, p.ID, sounds.Decision{Decision: "accept"}); err != nil {
		t.Fatal(err)
	}
	if got := w.str(`SELECT family FROM hook_log WHERE song_id = ?`, song); got != "reggae" {
		t.Errorf("features-shaped row family = %q, want reggae (written before Decide returned)", got)
	}
}
