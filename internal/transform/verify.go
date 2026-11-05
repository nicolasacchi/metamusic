package transform

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/problem"
)

// Toolchain is the toolchain record of transform.json (D-T1-3): the facts a bit-exact re-render depends on.
type Toolchain struct {
	GoVersion string `json:"go_version"`
	GOARCH    string `json:"goarch"`
	GOAMD64   string `json:"goamd64"`
}

// RunningToolchain reports the toolchain of this binary. GOAMD64 is read from the build info and defaults to v1 on amd64
// (the pinned build level); it is empty on other architectures.
func RunningToolchain() Toolchain {
	t := Toolchain{GoVersion: runtime.Version(), GOARCH: runtime.GOARCH}
	if runtime.GOARCH == "amd64" {
		t.GOAMD64 = "v1"
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, kv := range bi.Settings {
				if kv.Key == "GOAMD64" && kv.Value != "" {
					t.GOAMD64 = kv.Value
				}
			}
		}
	}
	return t
}

func (s *Service) toolchain() Toolchain {
	if s.o.Toolchain != nil {
		return s.o.Toolchain()
	}
	return RunningToolchain()
}

// VerifyReport is the answer of Verify (C210.9). Inconclusive is never reported as Match=false: a different toolchain can
// legitimately change the PCM.
type VerifyReport struct {
	Match        bool
	Inconclusive bool
	Reason       string
	Expected     string // the recorded pcm_sha256
	Got          string // the re-rendered one; empty when Inconclusive
}

// recorded is the part of transform.json Verify reads back.
type recorded struct {
	DSPVersion string    `json:"dsp_version"`
	Toolchain  Toolchain `json:"toolchain"`
	Synth      *synthDoc `json:"synth"`
}

// Verify re-renders a done transform from its stored spec and its parents' masters into a scratch directory (no ingest, no queue
// row, no database write) and compares pcm_sha256 (C210.9, D-T1-18: CLI only). It answers Inconclusive, without rendering,
// when the toolchain recorded in the take's transform.json differs from the running one: go_version, goarch, goamd64 or
// dsp_version, and for a score render the synth. ffmpeg is not part of the rule: decode is exact (D-T1-19).
func (s *Service) Verify(ctx context.Context, takeID string) (VerifyReport, error) {
	var rep VerifyReport
	if !s.runnable() {
		return rep, problem.E(503, "UNAVAILABLE", "transforms are not available", map[string]any{"component": "transform"})
	}
	var row tRow
	var state string
	var sha *string
	err := row.scan(s.o.Store.R.QueryRowContext(ctx, `SELECT `+rowCols+`, state, pcm_sha256 FROM transforms WHERE id = ?`, takeID), &state, &sha)
	if errors.Is(err, sql.ErrNoRows) {
		return rep, problem.E(404, "NOT_FOUND", "no such transform", nil)
	}
	if err != nil {
		return rep, fmt.Errorf("transform: verify: %w", err)
	}
	if state != StateDone || sha == nil {
		return rep, fmt.Errorf("transform: verify: the transform is %s, only a done transform has a result to verify", state)
	}
	rep.Expected = *sha
	b, err := s.o.Data.ReadFile(path.Join("takes", takeID, "transform.json"))
	if err != nil {
		return rep, fmt.Errorf("transform: verify: the take has no transform.json")
	}
	var rec recorded
	if err := json.Unmarshal(b, &rec); err != nil {
		return rep, fmt.Errorf("transform: verify: transform.json is malformed")
	}
	if reason := s.toolchainDiff(rec); reason != "" {
		rep.Inconclusive, rep.Reason = true, reason
		return rep, nil
	}
	var acct cpuAcct
	st, f := s.stage(ctx, row, "verify-"+takeID, false, &acct)
	if f != nil {
		return rep, fmt.Errorf("transform: verify: %s: %s", f.code, s.clean(f.msg))
	}
	defer func() { _ = s.o.Data.RemoveAll(st.rel) }()
	if f := s.render(ctx, row, st, &acct, nil); f != nil {
		return rep, fmt.Errorf("transform: verify: %s: %s", f.code, s.clean(f.msg))
	}
	out, f := s.readReport(st)
	if f != nil {
		return rep, fmt.Errorf("transform: verify: %s: %s", f.code, s.clean(f.msg))
	}
	rep.Got = out.PCMSHA256
	rep.Match = rep.Got == rep.Expected
	return rep, nil
}

// toolchainDiff names the first recorded toolchain field that differs from the running one ("" when none does).
func (s *Service) toolchainDiff(rec recorded) string {
	run := s.toolchain()
	switch {
	case rec.Toolchain.GoVersion != run.GoVersion:
		return fmt.Sprintf("go_version differs (recorded %s, running %s)", rec.Toolchain.GoVersion, run.GoVersion)
	case rec.Toolchain.GOARCH != run.GOARCH:
		return fmt.Sprintf("goarch differs (recorded %s, running %s)", rec.Toolchain.GOARCH, run.GOARCH)
	case rec.Toolchain.GOAMD64 != run.GOAMD64:
		return fmt.Sprintf("goamd64 differs (recorded %s, running %s)", rec.Toolchain.GOAMD64, run.GOAMD64)
	case rec.DSPVersion != DSPVersion:
		return fmt.Sprintf("dsp_version differs (recorded %s, running %s)", rec.DSPVersion, DSPVersion)
	case rec.Synth != nil:
		// The synth of a score render (T1b) is reproducible only for the same fluidsynth major.minor and the same soundfont;
		// this build carries no synth, so a recorded one can never be confirmed.
		return fmt.Sprintf("the recorded synth %s %s is not available in this build", rec.Synth.Name, majorMinor(rec.Synth.Version))
	}
	return ""
}

// majorMinor keeps the first two dot-separated numbers of a version token ("fluidsynth 2.3.4" -> "2.3").
func majorMinor(v string) string {
	for _, f := range strings.Fields(v) {
		if len(f) > 0 && f[0] >= '0' && f[0] <= '9' {
			parts := strings.Split(f, ".")
			return strings.Join(parts[:min(2, len(parts))], ".")
		}
	}
	return v
}
