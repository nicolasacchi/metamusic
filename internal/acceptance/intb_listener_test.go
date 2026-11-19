package acceptance_test

import (
	"slices"
	"strings"
	"testing"
)

// TestIntBListenerLeakSweep is the U1-INT-B safety net for the per-operation deny-lists of V2 (C308.3, C314.5): every
// operation a Listener may call (read from the registry and Permitted, so a new one is swept without anyone listing it) is
// called with realistic ids as the human Listener and as a paired Listener phone, and no sensitive VALUE collected from the
// database (hashes, seeds, request ids, job ids, Muse text, private notes, other users' names and verdicts, discarded take
// ids, every money amount) and no banned key appears in any body.
func TestIntBListenerLeakSweep(t *testing.T) {
	t.Parallel()
	w := newIntbWorld(t)
	ops := intbOps(t)
	lsec, _ := w.secrets(t)
	banned := append(slices.Clone(listenerBannedKeys), "*money*")
	human := intbRole{Name: "carol", Client: w.Carol, P: intbListener, Secrets: lsec, Banned: banned, HideDiscarded: true}
	phone := intbRole{Name: "carol-phone", Client: w.R.DeviceFor(t, "carol"), P: intbListenerDevice, Secrets: lsec, Banned: banned, HideDiscarded: true}
	hres := w.run(t, human, ops, false, false)
	dres := w.run(t, phone, ops, true, false)
	for id, rs := range w.run(t, human, ops, false, true) {
		hres[id] = rs
	}
	w.run(t, phone, ops, true, true)
	// a sweep that called nothing proves nothing
	for _, id := range []string{"get_track", "search_tracks", "list_songs", "get_song", "get_record", "get_tape", "get_media_links", "rate_track", "log_listen_events", "get_lineage", "get_song_atlas"} {
		if len(hres[id]) == 0 {
			t.Errorf("the Listener sweep never called %s", id)
		}
	}
	// the phone answers like the browser: same status and same keys for every read both may call
	for id, hs := range hres {
		ds, ok := dres[id]
		if !ok || len(ds) != len(hs) || id == "whoami" {
			continue
		}
		for i := range hs {
			if hs[i].Method != "GET" || hs[i].Status != ds[i].Status || (hs[i].Status/100 == 2 && !sameKeys(hs[i].Body, ds[i].Body)) {
				t.Errorf("%s %s: the Listener's phone answers %d with other keys than the browser (%d)", id, hs[i].Path, ds[i].Status, hs[i].Status)
			}
		}
	}
}

func sameKeys(a, b []byte) bool {
	ka, kb := keysOf(a), keysOf(b)
	if len(ka) != len(kb) {
		return false
	}
	for k := range ka {
		if !kb[k] {
			return false
		}
	}
	return true
}

// TestIntBEditorLeakSweep is the Editor half (C314.5, plan 2): every operation an Editor may call is called as the human
// Editor and as an Editor's paired phone, and no money amount (by value or by key), no cost canary, no other user's verdict
// and no Muse text in the context and stats blocks (C305) appears.
func TestIntBEditorLeakSweep(t *testing.T) {
	t.Parallel()
	w := newIntbWorld(t)
	ops := intbOps(t)
	_, esec := w.secrets(t)
	banned := append(slices.Clone(editorBannedKeys), "*money*", "others")
	human := intbRole{Name: "bob", Client: w.Bob, P: intbEditor, Secrets: esec, Banned: banned, MoneyOK: editorMoneyOps}
	phone := intbRole{Name: "bob-phone", Client: w.R.DeviceFor(t, "bob"), P: intbEditorDevice, Secrets: esec, Banned: banned, MoneyOK: editorMoneyOps}
	for _, r := range []intbRole{human, phone} {
		called := w.run(t, r, ops, r.Name == "bob-phone", false)
		for _, id := range []string{"get_track", "search_tracks", "get_budget", "get_stats", "get_track_context", "list_batches"} {
			if r.Name == "bob" && len(called[id]) == 0 {
				t.Errorf("the Editor sweep never called %s", id)
			}
		}
		w.run(t, r, ops, r.Name == "bob-phone", true)
	}
}

// TestIntBSweepIsNotVacuous is the negative control of the sweeps: the Owner, who may see everything, answers the same
// requests, and the scan must find every kind of secret the Listener sweep claims to keep out (and every banned key). A kind
// the Owner never shows would make the Listener's clean result prove nothing.
func TestIntBSweepIsNotVacuous(t *testing.T) {
	t.Parallel()
	w := newIntbWorld(t)
	ops := intbOps(t)
	lsec, esec := w.secrets(t)
	hit := map[string]bool{}
	banned := append(slices.Clone(listenerBannedKeys), "*money*")
	owner := intbRole{Name: "alice", Client: w.Own, P: intbOwner, Secrets: lsec, Banned: banned, Collect: hit}
	w.run(t, owner, ops, false, false)
	kinds := map[string]bool{}
	for what := range hit {
		kinds[strings.SplitN(what, " ", 2)[0]] = true
		if strings.HasPrefix(what, "banned") {
			kinds[what] = true
		}
	}
	for _, want := range []string{"hash", "seed", "request", "recipe", "job", "muse", "comment", "note", "liner", "other", "username", "money", "discarded", "hidden", "banned key seed", "banned key request_id", "banned key cost", "banned key muse_id", "banned key others"} {
		if !kinds[want] {
			t.Errorf("the Owner's answers never show a %q value or key: the sweeps prove nothing about it (saw %v)", want, kinds)
		}
	}
	// the Editor's secret set is just as real
	ed := map[string]bool{}
	bob := intbRole{Name: "alice-as-bob-secrets", Client: w.Own, P: intbOwner, Secrets: esec, Banned: []string{"*money*"}, Collect: ed}
	w.run(t, bob, ops, false, false)
	for _, want := range []string{"money", "other verdict"} {
		found := false
		for what := range ed {
			found = found || strings.HasPrefix(what, want)
		}
		if !found {
			t.Errorf("the Owner's answers never show an Editor secret of kind %q (saw %v)", want, ed)
		}
	}
}

// editorMoneyOps are the operations whose answers carry money keys for an Editor by design (C310.5): the estimate, the
// approval state, the job states and the cost of its OWN batches. The amounts of anyone else's batch, the caps, the ledger
// and every other money value stay secret by value in the sweep.
var editorMoneyOps = map[string]string{
	"list_batches": "own batches", "get_batch": "own batch", "cancel_batch": "own batch", "list_jobs": "own jobs", "get_job": "own job", "wait_for_job": "own job", "cancel_job": "own job",
	"get_approval_status": "own batch", "get_budget": "the reduced budget", "estimate_generation": "own estimate", "estimate_variant": "own estimate",
	"create_generation": "own batch", "create_variant": "own batch", "get_stats": "no money is shown to an Editor, but the generation block key is",
}
