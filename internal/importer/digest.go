package importer

import (
	"encoding/json"
	"sort"
)

// digestOf is the hex sha256 of the canonical JSON of the sorted plan entries
// (C16.7). Timestamps and generated ids never enter an entry, so an unchanged
// inbox and library give an unchanged digest.
func digestOf(entries []string) string {
	e := append([]string{}, entries...)
	sort.Strings(e)
	b, _ := json.Marshal(e)
	return hexSHA(b)
}

// diffEntries lists entries only in cur (added) and only in old (removed).
func diffEntries(old, cur []string) (added, removed []string) {
	in := map[string]bool{}
	for _, e := range old {
		in[e] = true
	}
	seen := map[string]bool{}
	for _, e := range cur {
		seen[e] = true
		if !in[e] {
			added = append(added, e)
		}
	}
	for _, e := range old {
		if !seen[e] {
			removed = append(removed, e)
		}
	}
	return added, removed
}

// DiffPlans compares a reviewed report's plan entries with the current ones.
func DiffPlans(reviewed, current []string) (added, removed []string) {
	return diffEntries(reviewed, current)
}
