package drafts_test

import (
	"context"
	"errors"
	"hash/fnv"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/drafts"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

func recoveredRig(t *testing.T, id string) (*servertest.Rig, *drafts.Service) {
	t.Helper()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, Files: true})
	svc := drafts.New(r.Store, r.Services.Deps.Library, nil, func() string { return id })
	return r, svc
}

func recoveredIn(mod func(*drafts.RecoveredDraft)) drafts.RecoveredDraft {
	in := drafts.RecoveredDraft{ReferenceTrackID: lt.TrackRefFlac, ReferenceTitle: "Città di Notte", Style: "italian vocals, 100 BPM", StyleSource: "placeholder",
		Lyrics: "[verse]\nla luna cammina sul prato\nil vento racconta una storia\n", AnalysisSHA256: strings.Repeat("c", 64), Language: "it", Words: 10, Confidence: 0.8}
	if mod != nil {
		mod(&in)
	}
	return in
}

func draftRows(t *testing.T, r *servertest.Rig) int {
	t.Helper()
	var n int
	if err := r.Store.R.QueryRow(`SELECT (SELECT COUNT(*) FROM recipe_drafts) + (SELECT COUNT(*) FROM draft_recoveries)`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestCreateRecovered_Request: the request is {id: rec-<slug>-<6 hex>, style, lyrics, cot, seed} with the seed the FNV-1a of the draft
// id mod 2^31, the draft has origin recovered and no parent, the label is cut to 40 characters, and the recovery row is written with it.
func TestCreateRecovered_Request(t *testing.T) {
	t.Parallel()
	const id = "01900000-0000-7000-8000-00000000d001"
	r, svc := recoveredRig(t, id)
	d, err := svc.CreateRecovered(context.Background(), auth.Principal{}, recoveredIn(func(in *drafts.RecoveredDraft) {
		in.Title = strings.Repeat("é", 60)
		in.Rationale = "from the recording"
	}))
	if err != nil {
		t.Fatal(err)
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	wantSeed := int64(h.Sum32() % (1 << 31))
	if d.ID != id || d.Origin != "recovered" || d.ParentTrackID != nil || d.Status != "backlog" || d.Revision != 1 {
		t.Errorf("draft = %+v", d)
	}
	if !strings.HasPrefix(d.RequestID, "rec-citta_di_notte-") || len(d.RequestID) != len("rec-citta_di_notte-")+6 {
		t.Errorf("request id = %q", d.RequestID)
	}
	if d.Request.Seed != wantSeed || d.Request.Cot != "melody" || d.Request.Style != "italian vocals, 100 BPM" {
		t.Errorf("request = %+v, want seed %d, cot melody", d.Request, wantSeed)
	}
	if d.Label == nil || len([]rune(*d.Label)) != 40 {
		t.Errorf("label = %v, want 40 characters", d.Label)
	}
	var track, style string
	var words int
	if err := r.Store.R.QueryRow(`SELECT track_id, style_source, words FROM draft_recoveries WHERE draft_id = ?`, id).Scan(&track, &style, &words); err != nil ||
		track != lt.TrackRefFlac || style != "placeholder" || words != 10 {
		t.Errorf("recovery row = %q %q %d (%v)", track, style, words, err)
	}
	// the label falls back to the reference's title; a title that slugs to nothing gives the slug "reference"
	_, svc2 := recoveredRig(t, "01900000-0000-7000-8000-00000000d002")
	d2, err := svc2.CreateRecovered(context.Background(), auth.Principal{}, recoveredIn(func(in *drafts.RecoveredDraft) { in.ReferenceTitle = "—" }))
	if err != nil || !strings.HasPrefix(d2.RequestID, "rec-reference-") || d2.Label == nil || *d2.Label != "—" {
		t.Errorf("fallback: %+v (%v)", d2, err)
	}
}

// TestCreateRecovered_Refusals: every failure leaves no draft and no recovery row behind (one transaction).
func TestCreateRecovered_Refusals(t *testing.T) {
	t.Parallel()
	for name, mod := range map[string]func(*drafts.RecoveredDraft){
		"style source":        func(in *drafts.RecoveredDraft) { in.StyleSource = "guess" },
		"protocol error":      func(in *drafts.RecoveredDraft) { in.Cot = "loud" },
		"not a reference":     func(in *drafts.RecoveredDraft) { in.ReferenceTrackID = lt.TrackLuna },
		"unknown reference":   func(in *drafts.RecoveredDraft) { in.ReferenceTrackID = "no-such-track" },
		"bad analysis sha256": func(in *drafts.RecoveredDraft) { in.AnalysisSHA256 = "short" },
	} {
		r, svc := recoveredRig(t, "01900000-0000-7000-8000-00000000d003")
		before := draftRows(t, r)
		if _, err := svc.CreateRecovered(context.Background(), auth.Principal{}, recoveredIn(mod)); err == nil {
			t.Errorf("%s was accepted", name)
		}
		if after := draftRows(t, r); after != before {
			t.Errorf("%s left %d rows behind", name, after-before)
		}
	}
	// a protocol error is the editor's LINT_FAILED answer
	_, svc := recoveredRig(t, "01900000-0000-7000-8000-00000000d004")
	_, err := svc.CreateRecovered(context.Background(), auth.Principal{}, recoveredIn(func(in *drafts.RecoveredDraft) { in.Cot = "loud" }))
	var de *drafts.Error
	if !errors.As(err, &de) || de.Code != "LINT_FAILED" {
		t.Errorf("err = %v, want LINT_FAILED", err)
	}
}

// TestUpdate_RecoveredDraftTakesNoParent: update_draft refuses a parent on a recovered draft; clearing stays allowed.
func TestUpdate_RecoveredDraftTakesNoParent(t *testing.T) {
	t.Parallel()
	const id = "01900000-0000-7000-8000-00000000d003"
	_, svc := recoveredRig(t, id)
	d, err := svc.CreateRecovered(context.Background(), auth.Principal{}, recoveredIn(nil))
	if err != nil {
		t.Fatal(err)
	}
	parent := lt.TrackLuna
	if _, err := svc.Update(context.Background(), auth.Principal{}, id, drafts.DraftPatch{Revision: d.Revision, ParentTrackID: &parent}); err == nil {
		t.Fatal("a parent on a recovered draft was accepted")
	}
	empty := ""
	if _, err := svc.Update(context.Background(), auth.Principal{}, id, drafts.DraftPatch{Revision: d.Revision, ParentTrackID: &empty}); err != nil {
		t.Errorf("clearing the parent: %v", err)
	}
}
