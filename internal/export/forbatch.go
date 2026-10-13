package export

import (
	"strconv"
	"strings"
)

const maxIDLen = 180

// withSuffix appends suffix to base, truncating base so the id stays within maxIDLen.
func withSuffix(base, suffix string) string {
	if len(base)+len(suffix) > maxIDLen {
		base = base[:maxIDLen-len(suffix)]
	}
	return base + suffix
}

// applyUniqueIDs implements the unique-id rule (D-M2a-20): every row whose id occurs more than once
// gets "<id>-s<seed>", remaining collisions "-2", "-3", ... in row order. It sets cand.newID on the
// renamed rows and returns their indexes; rows with unique ids are untouched.
func applyUniqueIDs(rows []cand) map[int]bool {
	count := map[string]int{}
	for _, c := range rows {
		count[c.id]++
	}
	used := map[string]bool{}
	for _, c := range rows {
		if count[c.id] == 1 {
			used[c.id] = true
		}
	}
	renamed := map[int]bool{}
	for i := range rows {
		c := &rows[i]
		if count[c.id] < 2 {
			continue
		}
		req, ok := c.request()
		if !ok {
			continue // cannot regenerate; Validation will report the duplicate
		}
		cand := withSuffix(c.id, "-s"+strconv.FormatInt(req.Seed, 10))
		for n := 2; used[cand]; n++ {
			cand = withSuffix(withSuffix(c.id, "-s"+strconv.FormatInt(req.Seed, 10)), "-"+strconv.Itoa(n))
		}
		used[cand] = true
		c.newID = strings.TrimSpace(cand)
		renamed[i] = true
	}
	return renamed
}
