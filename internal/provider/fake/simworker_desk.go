package fake

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/nicolasacchi/metamusic/internal/provider"
)

// The fake's plan kind (C361.5): per seed a deterministic two-voice yue2 score built from the request's lyric sections and the seed
// (with chord symbols for cot=full), bundled as the plan-NN.tar a real worker uploads, with the plan.json of C361.2. Fault tokens
// in the recipe's style, like the other fakes, select the failure modes:
//
//	fake:plan_fail_seed=<i>  the seed at index i reports status error (PLAN_FAILED) and uploads no bundle
//	fake:plan_truncated      every ok plan reports truncated (plan.json and the output)
//	fake:plans_fail          every seed fails, so the run fails PLANS_FAILED
//
// It is registered by internal/app (desk.go), not at init, so the fake package's own tests still see an image without the stage.

// Plan fault tokens.
const (
	planFaultFailSeed  = "fake:plan_fail_seed="
	planFaultTruncated = "fake:plan_truncated"
	planFaultAllFail   = "fake:plans_fail"
)

type planFaults struct {
	failSeed  map[int]bool
	truncated bool
	all       bool
}

func parsePlanFaults(style string) planFaults {
	f := planFaults{failSeed: map[int]bool{}}
	for _, tok := range strings.FieldsFunc(style, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		switch {
		case tok == planFaultTruncated:
			f.truncated = true
		case tok == planFaultAllFail:
			f.all = true
		case strings.HasPrefix(tok, planFaultFailSeed):
			if i, err := strconv.Atoi(strings.TrimPrefix(tok, planFaultFailSeed)); err == nil {
				f.failSeed[i] = true
			}
		}
	}
	return f
}

// planScore is the deterministic score of one seed: one block of two bars per lyric section (a comment label, as the real planner
// writes them), two voices, a chord symbol on the melody's first note when cot is full. The seed picks the starting pitch.
func planScore(labels []string, seed int64, cot string) []byte {
	var b strings.Builder
	b.WriteString("X:1\nT:synthetic plan\nM:4/4\nL:1/8\nQ:1/4=120\nK:C\n")
	scale := []string{"C", "D", "E", "F", "G", "A", "B", "c"}
	at := func(k int) string { return scale[(int(seed%8)+k+8)%8] }
	voice := func(id string, melody bool) {
		b.WriteString("V:" + id + "\n")
		for i, l := range labels {
			b.WriteString("% " + l + "\n")
			for bar := 0; bar < barsPerSection; bar++ {
				chord := ""
				if melody && cot == "full" && bar == 0 {
					chord = `"` + []string{"C", "F", "G", "Am"}[(i+int(seed%4))%4] + `"`
				}
				var bar8 string
				if melody {
					bar8 = chord + at(0) + at(1) + at(2) + at(3) + at(4) + at(3) + at(2) + at(1)
				} else {
					bar8 = at(0) + ",4" + at(4) + ",4"
				}
				end := "|"
				if i == len(labels)-1 && bar == barsPerSection-1 {
					end = "|]"
				}
				b.WriteString(bar8 + end)
			}
			b.WriteString("\n")
		}
	}
	voice("1", true)
	voice("2", false)
	return []byte(b.String())
}

// PlanHook is the SimKindHook of the "plan" kind.
func PlanHook(_ context.Context, _ *SimWorker, in provider.WorkerInput) (SimKindResult, error) {
	var req struct {
		Style  string `json:"style"`
		Lyrics string `json:"lyrics"`
		Cot    string `json:"cot"`
	}
	if in.Plan == nil {
		return SimKindResult{}, errors.New("fake: a plan document has a plan block")
	}
	if err := json.Unmarshal(in.Request, &req); err != nil {
		return SimKindResult{}, fmt.Errorf("fake: plan request: %w", err)
	}
	f := parsePlanFaults(req.Style)
	labels := scoreLabels(req.Lyrics)
	if len(labels) == 0 {
		labels = []string{"verse"}
	}
	var res SimKindResult
	for i, seed := range in.Plan.Seeds {
		if f.all || f.failSeed[i] {
			res.Plans = append(res.Plans, provider.PlanOut{Index: i, Seed: seed, Status: "error", Code: "PLAN_FAILED"})
			continue
		}
		score := planScore(labels, seed, req.Cot)
		meta, err := json.Marshal(map[string]any{"seed": seed, "cot": req.Cot, "truncated": f.truncated, "abc_tokens": len(score) / 2,
			"seconds": 1.5, "yue2": "fake"})
		if err != nil {
			return SimKindResult{}, err
		}
		body, err := tarOf([][2][]byte{{[]byte("score.abc"), score}, {[]byte("plan.json"), meta}})
		if err != nil {
			return SimKindResult{}, err
		}
		name := provider.PlanBundleName(i)
		res.Artifacts = append(res.Artifacts, SimArtifact{Name: name, Body: body})
		out := provider.PlanOut{Index: i, Seed: seed, Status: "ok", Bundle: name}
		if f.truncated {
			out.Truncated = map[string]bool{"abc": true, "semantic": false}
		}
		res.Plans = append(res.Plans, out)
	}
	return res, nil
}

// tarOf writes an uncompressed POSIX tar of regular files (mode 0644, time zero: deterministic).
func tarOf(files [][2][]byte) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: string(f[0]), Mode: 0o644, Size: int64(len(f[1])), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f[1]); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
