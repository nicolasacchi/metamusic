package synth

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// SpecialKind selects a purpose-built defect signal (C55).
type SpecialKind int

const (
	// SpecialClipBursts is a 1 kHz stereo sine at -12 dBFS whose amplitude is
	// 2.0 and hard-clipped at +-1.0 in [2.0, 4.0) s.
	SpecialClipBursts SpecialKind = iota + 1
	// SpecialGap is a 440 Hz stereo sine at -14 dBFS with exact zeros in [4.0, 6.5) s.
	SpecialGap
)

// Special renders a 24-bit/48 kHz stereo FLAC of the given kind to dst
// (absolute path).
func Special(ctx context.Context, t mediatool.Tools, dst string, kind SpecialKind, seconds float64) error {
	if err := needAbs(dst); err != nil {
		return err
	}
	if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return errors.New("synth: special signal needs a positive duration")
	}
	var expr string
	switch kind {
	case SpecialClipBursts:
		expr = fmt.Sprintf("clip(if(between(t,2,4-1/%d),2.0,%.6f)*sin(2*PI*1000*t),-1,1)", sampleRate, math.Pow(10, -12.0/20))
	case SpecialGap:
		expr = fmt.Sprintf("if(between(t,4,6.5-1/%d),0,%.6f*sin(2*PI*440*t))", sampleRate, math.Pow(10, -14.0/20))
	default:
		return errors.New("synth: unknown special kind")
	}
	graph := fmt.Sprintf("aevalsrc='%s|%s':s=%d:d=%s", expr, expr, sampleRate, strconv.FormatFloat(seconds, 'f', 6, 64))
	tmp := dst + ".tmp"
	return publish(tmp, dst, render(ctx, t.FFmpeg, graph, flacOut, tmp))
}
