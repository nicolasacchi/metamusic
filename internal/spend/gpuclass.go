package spend

import (
	"regexp"
	"strings"
)

var (
	gpuPro       = regexp.MustCompile(`(?i)\bPRO\s*(\d{4})\b`)
	gpuMIG       = regexp.MustCompile(`(?i)\bMIG\b`)
	gpuGB        = regexp.MustCompile(`(?i)(\d+)\s*GB`)
	gpuAmpereA   = regexp.MustCompile(`(?i)^(?:RTX\s+)?(A\d{3,4})$`)
	gpuGeForce   = regexp.MustCompile(`(?i)^RTX\s+(\d{4})(?:\s+(Ti|SUPER))?$`)
	gpuNvidia    = regexp.MustCompile(`(?i)^NVIDIA\s+`)
	gpuGeForceNm = regexp.MustCompile(`(?i)^GeForce\s+`)
)

// GPUClassKey maps any spelling of a GPU, the catalog's serverless type name ("RTX A5000", "PRO 6000 MIG
// 24GB") or the name the worker's torch reports ("NVIDIA RTX A5000", "RTX PRO 6000 Blackwell Server Edition
// MIG 1g.24gb"), to one canonical class key, so rtf_stats rows learned from workers apply to the classes
// the quote allows. It is a pure function and idempotent. Unknown names keep their text minus the
// "NVIDIA " and "GeForce " prefixes.
func GPUClassKey(name string) string {
	n := strings.Join(strings.Fields(name), " ")
	n = gpuNvidia.ReplaceAllString(n, "")
	n = gpuGeForceNm.ReplaceAllString(n, "")
	if n == "" {
		return ""
	}
	if m := gpuPro.FindStringSubmatch(n); m != nil {
		key := "PRO " + m[1]
		if gpuMIG.MatchString(n) {
			key += " MIG"
			if all := gpuGB.FindAllStringSubmatch(n, -1); len(all) > 0 {
				key += " " + all[len(all)-1][1] + "GB"
			}
		}
		return key
	}
	if m := gpuAmpereA.FindStringSubmatch(n); m != nil {
		return strings.ToUpper(m[1])
	}
	if m := gpuGeForce.FindStringSubmatch(n); m != nil {
		key := "RTX " + m[1]
		switch {
		case strings.EqualFold(m[2], "SUPER"):
			key += " SUPER"
		case m[2] != "":
			key += " Ti"
		}
		return key
	}
	return n
}
