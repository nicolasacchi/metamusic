package features_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestFeatures_Yue2DigestsUnchanged pins every take digest of FixtureL3 (all yue2 or reference takes): the ACE-Step
// engine branch (C164) must not move them, so feature_def stays and no recompute is triggered. Regenerate only
// deliberately with -update, together with a Def bump.
func TestFeatures_Yue2DigestsUnchanged(t *testing.T) {
	t.Parallel()
	w, _ := newL3World(t)
	rows, err := w.st.R.Query(`SELECT track_id, digest, feature_def FROM take_features ORDER BY track_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct{ Track, Digest, Def string }
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Track, &r.Digest, &r.Def); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) < 10 {
		t.Fatalf("only %d take_features rows", len(got))
	}
	b, _ := json.MarshalIndent(got, "", " ")
	b = append(b, '\n')
	p := filepath.Join("testdata", "yue2_digests.golden.json")
	if *update {
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, b) {
		t.Errorf("take digests differ from %s: a yue2 digest moved (needs a Def bump, C164)", p)
	}
}
