package acceptance_test

import (
	"strings"
	"testing"

	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// seedMorningCrateM6N writes a ready, unrevealed Morning Crate over one take, so the crate reads of the scripted agent session have a
// crate to read (the rig has no media tools, so nothing builds one; a scripted build_morning_crate only starts a build).
func seedMorningCrateM6N(t *testing.T, r *servertest.Rig, track string) string {
	t.Helper()
	const id = "01900000-0000-7000-8000-00000000ef01"
	ts := store.FormatTime(lt.FixedNow)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO morning_crates(id,trigger,state,seed,options_json,created_by,created_at,built_at,takes,anchors,duration_ms)
			VALUES(?,'scheduled','ready',7,'{}','system:morning',?,?,1,0,6500)`, []any{id, ts, ts}},
		{`INSERT INTO morning_crate_items(crate_id,n,side_index,track_id,anchor,source,start_frame,audio_start_frame,end_frame,gain_cdb,limited)
			VALUES(?,1,0,?,0,'job',0,72000,312000,-120,0)`, []any{id, track}},
		{`INSERT INTO morning_crate_sides(crate_id,side_index,sha256,path,bytes,samples) VALUES(?,0,?,?,100,312000)`,
			[]any{id, strings.Repeat("cd", 32), "derived/morning/cd/" + strings.Repeat("cd", 32) + ".flac"}},
	} {
		if _, err := r.Store.W.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	return id
}
