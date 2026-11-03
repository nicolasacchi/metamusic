package acestep

import (
	"slices"
	"strings"
)

// Weights pins the Hugging Face revisions of the ACE-Step weights (C148.8, D-E2-5): the revisions the runner
// sends in `expect.weights`, `runpod provision` writes into the endpoint environment and the worker-contract
// job loads. Changing one is a deliberate, reviewed edit; a worker whose weights differ answers
// WEIGHTS_MISMATCH, which fails closed.
//
// The values are the real Hugging Face commit shas (the `sha` field of https://huggingface.co/api/models/<repo>),
// read on 2026-11-03 (both repositories declare licence mit; the verbatim model-card sentences are the E2-P0a
// licence reading, not repeated here).
var Weights = map[string]string{
	WeightsRepoMain: "19671f406d603126926c1b7e2adc169acbcade22",
	WeightsRepoBase: "e432212fec32b8965a14ffa57ae653438d6abd14",
}

// Hugging Face repositories of the weights.
const (
	WeightsRepoMain = "ACE-Step/Ace-Step1.5"
	WeightsRepoBase = "ACE-Step/acestep-v15-base"
)

// Environment variable names `runpod provision` sets on the endpoint (C147.3).
const (
	EnvMainRevision = "MM_ACESTEP_MAIN_REVISION"
	EnvBaseRevision = "MM_ACESTEP_BASE_REVISION"
	EnvTasks        = "MM_ACESTEP_TASKS"
)

// Task classes an endpoint can be provisioned for (MM_ACESTEP_TASKS).
const (
	TasksTurbo     = "turbo"
	TasksTurboBase = "turbo,base"
)

// NormalizeTasks returns the canonical MM_ACESTEP_TASKS value ("turbo" or "turbo,base") for a comma list in
// any order, or "" when the list is not one of the two.
func NormalizeTasks(tasks string) string {
	var parts []string
	for _, p := range strings.Split(tasks, ",") {
		parts = append(parts, strings.TrimSpace(p))
	}
	slices.Sort(parts)
	switch strings.Join(parts, ",") {
	case "turbo":
		return TasksTurbo
	case "base,turbo":
		return TasksTurboBase
	}
	return ""
}

// WeightsFor is the pinned repositories an endpoint with the given task classes loads: "turbo" is the main
// bundle only, "turbo,base" adds the base DiT. An invalid list yields nil.
func WeightsFor(tasks string) map[string]string {
	switch NormalizeTasks(tasks) {
	case TasksTurbo:
		return map[string]string{WeightsRepoMain: Weights[WeightsRepoMain]}
	case TasksTurboBase:
		return map[string]string{WeightsRepoMain: Weights[WeightsRepoMain], WeightsRepoBase: Weights[WeightsRepoBase]}
	}
	return nil
}

// WeightsEnv is the endpoint environment derived from Weights and the task list (nil for an invalid list).
func WeightsEnv(tasks string) map[string]string {
	t := NormalizeTasks(tasks)
	if t == "" {
		return nil
	}
	env := map[string]string{EnvMainRevision: Weights[WeightsRepoMain], EnvTasks: t}
	if t == TasksTurboBase {
		env[EnvBaseRevision] = Weights[WeightsRepoBase]
	}
	return env
}
