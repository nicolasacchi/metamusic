package acestep

import (
	"regexp"
	"slices"
)

// The canary-only capability switch (MM_ACESTEP_CANARY_CAPS). The real-GPU canary is the only way to verify a capability, so
// a canary request may use one that features.verified.json does not list yet. Nothing here marks anything verified: the
// switch yields a per-request copy of the features, and read-only surfaces keep reading the verified file.

var canaryIDRe = regexp.MustCompile(`^mm-canary-[0-9]{8}-`)

// IsCanaryID reports whether a request id is one the canary minted: "mm-canary-" and a YYYYMMDD day, then "-". It is the
// only place that decides what a canary request is (see canary.go and canary_e2.go in the runpod package for the ids).
func IsCanaryID(id string) bool { return canaryIDRe.MatchString(id) }

// WithCanary returns f with the named capabilities switched on, for one canary batch. The bounds (repaint, extend,
// reference_max_s, lrc_fixed_s) stay those of f. complete and extract need the base models: they are left off unless
// baseTasks is set (MM_ACESTEP_TASKS has base) and, as always, Caps() still requires models_mode to deliver them. Verified
// facts (verified_at, canary_commit, the reproducibility flags) are untouched. f is not modified.
func (f Features) WithCanary(names []string, baseTasks bool) Features {
	out := f
	out.Tasks = slices.Clone(f.Tasks)
	for _, n := range names {
		switch n {
		case "reference":
			out.Reference = true
		case "lrc":
			out.LRC = true
		case "repaint":
			out.Tasks = appendTask(out.Tasks, TaskRepaint)
		case "extend":
			out.Tasks = appendTask(out.Tasks, TaskExtend)
		case "complete":
			if baseTasks {
				out.Tasks = appendTask(out.Tasks, TaskComplete)
			}
		case "extract":
			if baseTasks {
				out.Tasks = appendTask(out.Tasks, TaskExtract)
			}
		}
	}
	return out
}

func appendTask(tasks []string, t string) []string {
	if slices.Contains(tasks, t) {
		return tasks
	}
	return append(tasks, t)
}

// CanaryCapsUsed names the switched-on capabilities (on) that r uses and f does not verify, in the order of on.
func (f Features) CanaryCapsUsed(r Request, on []string) []string {
	var used []string
	for _, n := range on {
		var uses bool
		switch n {
		case "reference":
			uses = r.Ref != nil && !f.Reference
		case "lrc":
			uses = r.LRC && !f.LRC
		case "repaint":
			uses = r.Task == TaskRepaint && !f.has(TaskRepaint)
		case "extend":
			uses = r.Task == TaskExtend && !f.has(TaskExtend)
		case "complete":
			uses = r.Task == TaskComplete && !f.has(TaskComplete)
		case "extract":
			uses = r.Task == TaskExtract && !f.has(TaskExtract)
		}
		if uses {
			used = append(used, n)
		}
	}
	return used
}

// CanaryCapNames is the vocabulary WithCanary understands; config.ACEStepCanaryCapNames mirrors it.
func CanaryCapNames() []string {
	return []string{"reference", "repaint", "extend", "complete", "extract", "lrc"}
}
