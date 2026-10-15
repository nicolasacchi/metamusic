package provenance_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/library/librarytest"
)

// TestProperty_ProvenanceInvariants drives seeded random sequences of source-table writes (drafts and refs, mothers set
// and cleared, roots and children, drafts fulfilled, ratings, status changes, duel verdicts) with a Settle every few steps
// and at the end. Picks and Sounds are enabled. The oracle: Verify is empty and a further Settle changes nothing. MM_PROPERTY_SEQS overrides the 60
// sequences (the weekly job runs 300); -short runs 6 so make check stays fast.
func TestProperty_ProvenanceInvariants(t *testing.T) {
	t.Parallel()
	seqs := 60
	if testing.Short() {
		seqs = 6
	}
	if v, err := strconv.Atoi(os.Getenv("MM_PROPERTY_SEQS")); err == nil && v > 0 {
		seqs = v
	}
	for seed := range seqs {
		t.Run(fmt.Sprintf("seq%03d", seed), func(t *testing.T) {
			t.Parallel()
			runSequence(t, uint64(seed))
		})
	}
}

type draftState struct {
	id, recipe string
	mother     string // "" = motherless
	fulfilled  bool
}

func runSequence(t *testing.T, seed uint64) {
	rng := rand.New(rand.NewPCG(seed, seed*7919+1))
	w := newWorld(t, realSteps(t)) // picks and sounds enabled: Verify also checks the event chain and Sound freshness
	sc := w.sc
	var drafts []*draftState
	var roots, tracks []string
	lineageChildren := map[string][]string{} // root -> children
	n := 0
	next := func() int { n++; return n }
	titles := []string{"Synthetic Muse 001", "Synthetic Muse 002", "Synthetic Muse 003"}
	artists := []string{"Synthetic Artist 001", "", "Synthetic Artist 003"}
	newRoot := func(recipe string) string {
		if recipe == "" {
			recipe = sc.Recipe(fmt.Sprintf("rt%d_v%d", next(), 1+rng.IntN(3)), "pop")
		}
		r := sc.Track(recipe, fmt.Sprintf("Root %d", n), "", sc.At(next()))
		roots, tracks = append(roots, r), append(tracks, r)
		return r
	}
	steps := 25 + rng.IntN(20)
	for step := 0; step < steps; step++ {
		switch op := rng.IntN(11); op {
		case 0, 1: // a draft, half with an index row
			rid := ""
			if rng.IntN(2) == 0 {
				rid = sc.Recipe(fmt.Sprintf("dr%d_v%d", next(), 1+rng.IntN(3)), "reggae, male vocal")
				if rng.IntN(4) != 0 {
					m := rng.IntN(len(titles))
					sc.Ref(rid, "i.csv", titles[m], artists[m], fmt.Sprintf("variant-%c", 'a'+rng.IntN(3)))
				}
			}
			id := sc.Draft(fmt.Sprintf("dq%d_v%d", next(), 1+rng.IntN(3)), librarytest.DraftOpts{Recipe: rid, Group: "g", Label: "variant-a"})
			drafts = append(drafts, &draftState{id: id, recipe: rid})
		case 2: // a mother
			if d := pick(rng, drafts, func(d *draftState) bool { return !d.fulfilled }); d != nil && len(roots) > 0 {
				d.mother = roots[rng.IntN(len(roots))]
				sc.SetMother(d.id, d.mother)
			}
		case 3: // the mother is removed
			if d := pick(rng, drafts, func(d *draftState) bool { return !d.fulfilled && d.mother != "" }); d != nil {
				d.mother = ""
				sc.SetMother(d.id, "")
			}
		case 4: // a root, sometimes with an index row on its recipe
			rec := sc.Recipe(fmt.Sprintf("rr%d_v%d", next(), 1+rng.IntN(3)), "pop")
			if rng.IntN(3) == 0 {
				sc.Ref(rec, "i.csv", titles[rng.IntN(len(titles))], "", "variant-b")
			}
			newRoot(rec)
		case 5: // a child of any take
			if len(tracks) > 0 {
				parent := tracks[rng.IntN(len(tracks))]
				c := sc.Track(sc.Recipe(fmt.Sprintf("ch%d_v1", next()), "pop"), fmt.Sprintf("Child %d", n), parent, sc.At(next()))
				root := w.str(`SELECT root_track_id FROM tracks WHERE id = ?`, c)
				lineageChildren[root] = append(lineageChildren[root], c)
				tracks = append(tracks, c)
			}
		case 6: // a draft is fulfilled
			if d := pick(rng, drafts, func(d *draftState) bool { return !d.fulfilled }); d != nil {
				switch {
				case d.mother != "":
					if kids := lineageChildren[d.mother]; len(kids) > 0 {
						sc.Fulfil(d.id, kids[rng.IntN(len(kids))])
						d.fulfilled = true
					}
				default:
					sc.Fulfil(d.id, newRoot(d.recipe))
					d.fulfilled = true
				}
			}
		case 7:
			if len(tracks) > 0 {
				sc.Rate(tracks[rng.IntN(len(tracks))], 1+rng.IntN(5))
			}
		case 8:
			if len(tracks) > 0 {
				sc.Status(tracks[rng.IntN(len(tracks))], []string{"draft", "to_relisten", "final", "discarded"}[rng.IntN(4)])
			}
		case 9: // a duel verdict between two takes
			if len(tracks) > 1 {
				a, b := rng.IntN(len(tracks)), rng.IntN(len(tracks))
				if a != b {
					sc.Verdict(sc.Duel(tracks[a], tracks[b]), []string{"a", "b", "tie"}[rng.IntN(3)])
				}
			}
		case 10:
			w.settle()
		}
		if rng.IntN(5) == 0 {
			w.settle()
		}
	}
	w.settle()
	w.verifyEmpty()
	h := w.contentHash()
	if rep := w.settle(); rep.KeysProcessed != 0 || w.contentHash() != h {
		t.Errorf("seed %d: a further Settle was not a no-op: %+v", seed, rep)
	}
}

func pick(rng *rand.Rand, ds []*draftState, ok func(*draftState) bool) *draftState {
	var cand []*draftState
	for _, d := range ds {
		if ok(d) {
			cand = append(cand, d)
		}
	}
	if len(cand) == 0 {
		return nil
	}
	return cand[rng.IntN(len(cand))]
}
