package songpr_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/generation"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/songpr"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// scoreFor is a synthetic score aligned with lt.M5Lyrics: verse 1 (bars 0-3), chorus (4-7), verse 2 (8-11), chorus (12-15),
// outro (16). Two voices.
func scoreFor() string {
	var b strings.Builder
	b.WriteString("X:1\nT:synthetic\nM:4/4\nL:1/8\nQ:1/4=90\nK:C\n%%MIDI program 53\nV:1 name=\"vocal\"\n")
	secs := []struct {
		label string
		bars  int
	}{{"verse 1", 4}, {"chorus", 4}, {"verse 2", 4}, {"chorus", 4}, {"outro", 1}}
	for _, s := range secs {
		b.WriteString("% " + s.label + "\n" + strings.Repeat("c2 d2 e2 f2 |", s.bars) + "\n")
	}
	b.WriteString("V:2 name=\"bass\"\n")
	for _, s := range secs {
		b.WriteString("% " + s.label + "\n" + strings.Repeat("C,8 |", s.bars) + "\n")
	}
	return b.String()
}

// withScore gives the rig a service that reads scoreFor() as the score of track and marks it aligned (or not).
func (r *rig) withScore(track string, aligned int) {
	r.t.Helper()
	text := scoreFor()
	r.exec(`INSERT INTO scores(track_id, abc_path, abc_sha256, bars, meter, q, bpm, seconds_per_bar, aligned, source, created_at)
		VALUES (?, 'takes/x/score.abc', 'x', 17, '4/4', '1/4=90', 90, 2.6, ?, 'import', '2026-10-01T00:00:00Z')`, track, aligned)
	r.svc = songpr.New(songpr.Options{Store: r.st, Now: func() time.Time { return lt.FixedNow }, NewID: r.id,
		ScoreABC: func(_ context.Context, id string) ([]byte, string, bool, error) {
			return []byte(text), "x", id == track, nil
		}})
}

func bars(voice string, from, to int, abc string) songpr.OpJSON {
	return songpr.OpJSON{Op: songpr.OpReplaceBars, Voice: sp(voice), BarFrom: ip(from), BarTo: ip(to), ABC: sp(abc)}
}

func TestPropose_ReplaceBars(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	g2, cm := r.fx.Lineage[2], r.fx.Comments
	r.withScore(g2, 1)
	got, err := r.svc.Propose(context.Background(), agent, songpr.ProposeInput{BaseTrackID: g2, AddressesCommentIDs: []string{cm["fix_chorus"]},
		Rationale: "lift the hook", Ops: []songpr.OpJSON{bars("1", 4, 5, "g2 a2 b2 c'2 | g8")}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Operator != songpr.OperatorABCEdit || got.Compiled.ABC == nil || got.Compiled.Lyrics != nil || got.Compiled.Style != nil {
		t.Fatalf("operator %q compiled %+v", got.Operator, got.Compiled)
	}
	if len(got.Diff.ABC) != 1 || got.Diff.ABC[0].After != "g2 a2 b2 c'2 | g8" || got.Bounds.Bounds.BarsChanged != 2 {
		t.Errorf("diff %+v bounds %+v", got.Diff.ABC, got.Bounds.Bounds)
	}
	back, err := r.svc.Get(context.Background(), human, got.ID)
	if err != nil || back.Compiled.ABC == nil || *back.Compiled.ABC != *got.Compiled.ABC || back.Ops[0].Voice == nil || *back.Ops[0].Voice != "1" {
		t.Errorf("Get = %+v %v", back.Compiled, err)
	}
}

func TestPropose_ReplaceBars_Refusals(t *testing.T) {
	t.Parallel()
	g := func(r *rig) (string, []string) { return r.fx.Lineage[2], []string{r.fx.Comments["fix_chorus"]} }
	ctx := context.Background()
	for _, c := range []struct {
		name, rule string
		code       string
		setup      func(r *rig)
		op         songpr.OpJSON
	}{
		{"take without a score", "no_score", "PATCH_INVALID", func(r *rig) { r.withScore(r.fx.Lineage[1], 1) }, bars("1", 4, 4, "g8")},
		{"no score reader", "no_score", "PATCH_INVALID", func(*rig) {}, bars("1", 4, 4, "g8")},
		{"unaligned score", "score_unaligned", "PATCH_OUT_OF_BOUNDS", func(r *rig) { r.withScore(r.fx.Lineage[2], 0) }, bars("1", 4, 4, "g8")},
		{"outside the scoped sections", "bars_out_of_scope", "PATCH_OUT_OF_BOUNDS", func(r *rig) { r.withScore(r.fx.Lineage[2], 1) }, bars("1", 8, 8, "g8")},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			c.setup(r)
			base, ids := g(r)
			_, err := r.svc.Propose(ctx, agent, songpr.ProposeInput{BaseTrackID: base, AddressesCommentIDs: ids, Rationale: "r", Ops: []songpr.OpJSON{c.op}})
			pe := wantProblem(t, err, 422, c.code)
			if vs, _ := pe.Details["violations"].([]songpr.Violation); len(vs) == 0 || vs[0].Rule != c.rule {
				t.Errorf("violations = %+v", pe.Details["violations"])
			}
			if n := r.count(`SELECT COUNT(*) FROM patches`); n != 0 {
				t.Errorf("%d patches stored", n)
			}
		})
	}
}

func TestSongPR_RenderCarriesABC(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	g2, cm := r.fx.Lineage[2], r.fx.Comments
	r.withScore(g2, 1)
	p := r.propose(agent, g2, []string{cm["fix_chorus"]}, bars("1", 4, 4, "g8"))
	gen := r.genRig()
	ctx := context.Background()
	e, err := gen.Estimate(ctx, agent, estimateIn(p.ID, 2))
	if err != nil {
		t.Fatalf("estimate: %v", err)
	}
	for i, tk := range e.Items[0].Takes {
		if tk.Operator != songpr.OperatorABCEdit {
			t.Errorf("take %d operator %q", i, tk.Operator)
		}
	}
	c, err := createFrom(ctx, gen, agent, e)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var mut, op string
	if err := r.st.R.QueryRow(`SELECT operator, mutation_json FROM jobs WHERE patch_id = ? AND batch_id = ? LIMIT 1`, p.ID, c.BatchID).Scan(&op, &mut); err != nil {
		t.Fatal(err)
	}
	var m struct {
		Operator string `json:"operator"`
		Patch    struct {
			ABC    string  `json:"abc"`
			Lyrics *string `json:"lyrics"`
		} `json:"recipe_patch"`
	}
	if err := json.Unmarshal([]byte(mut), &m); err != nil {
		t.Fatal(err)
	}
	if op != "abc_edit" || m.Operator != "abc_edit" || m.Patch.ABC != *p.Compiled.ABC || m.Patch.Lyrics != nil {
		t.Errorf("job %s: operator %q abc-equal %v lyrics %v", op, m.Operator, m.Patch.ABC == *p.Compiled.ABC, m.Patch.Lyrics)
	}
	// the take's recipe carries the patched score and unchanged lyrics
	var reqJSON string
	if err := r.st.R.QueryRow(`SELECT r.request_json FROM jobs j JOIN recipes r ON r.id = j.recipe_id WHERE j.patch_id = ? AND j.batch_id = ? LIMIT 1`, p.ID, c.BatchID).Scan(&reqJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reqJSON, `"abc":"X:1`) || !strings.Contains(reqJSON, "g8") {
		t.Errorf("recipe lacks the patched score: %.200s", reqJSON)
	}
}

func estimateIn(patch string, n int) generation.EstimateIn {
	return generation.EstimateIn{Items: []generation.Item{songPRItem(patch, n)}, Via: spend.ViaAPI}
}

func createFrom(ctx context.Context, gen *generation.Service, who auth.Principal, e generation.Estimate) (generation.Created, error) {
	return gen.Create(ctx, who, generation.CreateIn{Token: e.Token, Via: spend.ViaAPI, Rationale: "test"}, encodeStub)
}
