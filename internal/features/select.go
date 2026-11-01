package features

import "strings"

// AnalysisRow is the part of an analyses row that selection reads.
type AnalysisRow struct {
	Version     string // analyses.version: "<definition> <tool token>"
	MetricsJSON string
}

// SelectAnalysis picks the analysis row DeriveTake may read (C135.2): only rows whose version text before the first
// space equals def count (so "mm-key/10 x" is not "mm-key/1"), and the greatest version string wins. A row of
// another definition is ignored. Loudness rows never feed energy, so callers pass only the analyzer's own rows.
func SelectAnalysis(rows []AnalysisRow, def string) (AnalysisRow, bool) {
	var best AnalysisRow
	found := false
	for _, r := range rows {
		head, _, _ := strings.Cut(r.Version, " ")
		if head != def {
			continue
		}
		if !found || r.Version > best.Version {
			best, found = r, true
		}
	}
	return best, found
}
