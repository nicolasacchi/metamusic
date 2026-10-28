package api_test

import (
	"slices"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/crates"
)

// TestCrateVocabulary_EqualsListOps is the scaffold of D-L2-13's drift guard (lane K adds the behavioural half: a fixture query
// through the op and through the stored filter returns the same ids). The key sets of the vocabulary are compared with the query
// parameters of list_songs (subject song) and search_tracks (subject take) as the generated OpenAPI document declares them,
// minus the non-saved set: after the freeze every key is a real parameter, so adding a parameter to an op without adding it
// to the vocabulary (or the reverse) fails here by name.
func TestCrateVocabulary_EqualsListOps(t *testing.T) {
	t.Parallel()
	a, _ := newTestAPI(t)
	byID := map[string]api.OpInfo{}
	for _, op := range api.Operations(a) {
		byID[op.ID] = op
	}
	for subject, opID := range map[string]string{"song": "list_songs", "take": "search_tracks"} {
		var params []string
		for _, p := range pathOperation(a, byID[opID]).Parameters {
			if p.In == "query" && !crates.IsNonSaved(p.Name) {
				params = append(params, p.Name)
			}
		}
		var keys []string
		for _, k := range crates.Keys(subject) {
			keys = append(keys, k.Name)
		}
		slices.Sort(params)
		slices.Sort(keys)
		if !slices.Equal(params, keys) {
			t.Errorf("%s: %s parameters minus the non-saved set = %v, vocabulary keys = %v", subject, opID, params, keys)
		}
	}
}
