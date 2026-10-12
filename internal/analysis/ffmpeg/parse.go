package ffmpeg

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/analysis"
)

const (
	floorLUFS   = -70.0
	floorDB     = -120.0
	defaultStep = 0.1
)

type stSample struct{ t, s float64 }

// ParseLoudness parses the ametadata stream printed by the C14 loudness
// invocation. The final values come from the last frame (ffmpeg reports
// cumulative values); the 1 Hz short-term curve takes, for every whole second
// k up to floor(duration), S from the frame with the largest pts_time <= k.
// Exact pts_time matches are never required, so other framings give the same
// curve. The duration is the last pts_time plus the median frame step.
func ParseLoudness(r io.Reader) (analysis.Loudness, error) {
	var (
		samples []stSample
		last    map[string]float64
		curT    float64
		curS    = math.NaN()
		inFrame bool
		maxS    = math.Inf(-1)
	)
	flush := func() {
		if inFrame && !math.IsNaN(curS) {
			samples = append(samples, stSample{curT, curS})
			maxS = math.Max(maxS, curS)
		}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "frame:"):
			flush()
			f := strings.Fields(line)
			if len(f) < 3 || !strings.HasPrefix(f[2], "pts_time:") {
				return analysis.Loudness{}, fmt.Errorf("ffmpeg: malformed frame line")
			}
			t, err := strconv.ParseFloat(strings.TrimPrefix(f[2], "pts_time:"), 64)
			if err != nil || math.IsNaN(t) || math.IsInf(t, 0) {
				return analysis.Loudness{}, fmt.Errorf("ffmpeg: malformed pts_time")
			}
			curT, curS, inFrame = t, math.NaN(), true
			last = map[string]float64{}
		case inFrame && strings.HasPrefix(line, "lavfi.r128."):
			k, v, _ := strings.Cut(strings.TrimPrefix(line, "lavfi.r128."), "=")
			x, err := strconv.ParseFloat(v, 64)
			if err != nil || math.IsNaN(x) {
				return analysis.Loudness{}, fmt.Errorf("ffmpeg: malformed value for %s", k)
			}
			last[k] = x
			if k == "S" {
				curS = x
			}
		}
	}
	if err := sc.Err(); err != nil {
		return analysis.Loudness{}, fmt.Errorf("ffmpeg: reading loudness output: %w", err)
	}
	flush()
	if len(samples) == 0 || last == nil {
		return analysis.Loudness{}, errors.New("ffmpeg: loudness output has no frames")
	}
	for _, k := range []string{"I", "LRA", "LRA.low", "LRA.high", "true_peak", "sample_peak"} {
		if _, ok := last[k]; !ok {
			return analysis.Loudness{}, fmt.Errorf("ffmpeg: loudness output is missing %s", k)
		}
	}
	l := analysis.Loudness{
		IntegratedLUFS:   last["I"],
		LRA:              last["LRA"],
		LRALowLUFS:       last["LRA.low"],
		LRAHighLUFS:      last["LRA.high"],
		TruePeakDBTP:     linToDB(last["true_peak"]),
		SamplePeakDBFS:   linToDB(last["sample_peak"]),
		ShortTermMaxLUFS: maxS,
		ShortTerm1Hz:     curve(samples),
	}
	return l, nil
}

func linToDB(v float64) float64 {
	if v <= 0 {
		return floorDB
	}
	return math.Max(floorDB, 20*math.Log10(v))
}

func curve(s []stSample) []float64 {
	step := defaultStep
	if len(s) > 1 {
		d := make([]float64, 0, len(s)-1)
		for i := 1; i < len(s); i++ {
			d = append(d, s[i].t-s[i-1].t)
		}
		slices.Sort(d)
		step = d[len(d)/2]
	}
	dur := s[len(s)-1].t + step
	n := int(math.Floor(dur + 1e-6))
	out := make([]float64, 0, max(n, 0))
	j := 0
	for k := 1; k <= n; k++ {
		for j+1 < len(s) && s[j+1].t <= float64(k)+1e-6 {
			j++
		}
		v := s[j].s
		if s[j].t > float64(k)+1e-6 {
			v = floorLUFS // no frame at or before k
		}
		out = append(out, math.Round(math.Max(v, floorLUFS)*10)/10)
	}
	return out
}

// parseVersion extracts the raw version token from `ffmpeg -version` output.
func parseVersion(out []byte) (string, error) {
	line, _, _ := strings.Cut(string(out), "\n")
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ffmpeg version ")
	if !ok {
		return "", errors.New("ffmpeg: unrecognised -version output")
	}
	f := strings.Fields(rest)
	if len(f) == 0 {
		return "", errors.New("ffmpeg: empty version token")
	}
	return f[0], nil
}
