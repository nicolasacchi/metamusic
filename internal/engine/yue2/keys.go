package yue2

import "regexp"

var groupRe = regexp.MustCompile(`(?i)[-_](v\d+|s\d+|seed\d+)$`)
var seedFamilyRe = regexp.MustCompile(`(?i)[-_](s\d+|seed\d+)$`)

// GroupKey is for grouping ONLY (draft group_key, backlog grouping, unmatched-audio candidates),
// never lineage. It strips ONE trailing variant or seed suffix.
func GroupKey(requestID string) string { return groupRe.ReplaceAllString(requestID, "") }

// SeedFamily is the lineage key (C16.5): it strips ONE trailing seed suffix only; variants
// ("x_001_v2") are independent recipes.
func SeedFamily(requestID string) string { return seedFamilyRe.ReplaceAllString(requestID, "") }
