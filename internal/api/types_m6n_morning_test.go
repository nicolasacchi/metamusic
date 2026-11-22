package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/morning"
)

func TestMorningTypes_CrateOutKeepsBlindNulls(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 22, 7, 0, 0, 0, time.UTC)
	stars := 4
	c := morning.Crate{ID: "c1", State: morning.StateReady, Trigger: morning.TriggerScheduled, CreatedAt: at, Takes: 2,
		Sides:    []morning.SideView{{Side: 0, Label: "Side A", DurationMS: 5, Bytes: 9, Chapters: 2, SHA256: "ab"}},
		Chapters: []morning.ChapterView{{N: 1, Verdict: morning.VerdictUnheard, Stars: &stars}, {N: 2, Verdict: morning.VerdictHeard}}}
	out := MorningCrateOut(c)
	b, _ := json.Marshal(out)
	js := string(b)
	for _, want := range []string{`"track":null`, `"anchor":null`, `"gain_db":null`, `"limited":null`, `"lufs_after":null`, `"playlist_id":null`, `"url":null`, `"stars":4`} {
		if !strings.Contains(js, want) {
			t.Errorf("%s missing from %s", want, js)
		}
	}
	// revealed: the track appears
	root := "r1"
	c.Chapters[0].Track = &morning.TrackRef{ID: "t1", Title: "Title", RootTrackID: root}
	b, _ = json.Marshal(MorningCrateOut(c))
	if !strings.Contains(string(b), `"track":{"id":"t1","title":"Title","root_track_id":"r1"}`) {
		t.Errorf("revealed track missing from %s", b)
	}
	// empty crates serialise empty arrays, never null
	b, _ = json.Marshal(MorningCrateOut(morning.Crate{ID: "e", State: morning.StateEmpty}))
	if !strings.Contains(string(b), `"sides":[]`) || !strings.Contains(string(b), `"chapters":[]`) {
		t.Errorf("empty crate: %s", b)
	}
}

func TestMorningTypes_SettingsAndPage(t *testing.T) {
	t.Parallel()
	owner := auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice", Role: auth.RoleOwner, User: "alice"}
	s := MorningSettingsOut(owner, morning.Settings{Enabled: true, At: "07:00", Minutes: 40, SideMinutes: 15, MaxTakes: 14, Anchors: 1, KeepDays: 7, WaitMinutes: 45, UpdatedBy: "system:migration"})
	if s.UpdatedBy.Kind != "system" || s.At != "07:00" || s.Minutes != 40 {
		t.Errorf("settings %+v", s)
	}
	on, mins := true, 30
	p := MorningSettingsPatch{Enabled: &on, Minutes: &mins}.Patch()
	if p.Enabled != &on || p.Minutes != &mins || p.At != nil {
		t.Errorf("patch %+v", p)
	}
	in := BuildMorningCrateBody{IncludeTrackIDs: []string{"a"}, Minutes: 20}.In()
	if len(in.IncludeTrackIDs) != 1 || in.Minutes != 20 {
		t.Errorf("build in %+v", in)
	}
	pg := MorningCratePageOut(morning.Page{Items: []morning.CrateSummary{{ID: "c1", State: "ready", Takes: 3}}, NextCursor: "x|y"})
	if len(pg.Items) != 1 || pg.Items[0].Takes != 3 || pg.NextCursor != "x|y" {
		t.Errorf("page %+v", pg)
	}
	if e := MorningCratePageOut(morning.Page{}); e.Items == nil {
		t.Error("an empty page must not marshal null")
	}
}
