package synth

import (
	"context"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

type seg struct{ dBFS, seconds float64 }

// ebuCases are the EBU TECH 3341 minimum requirements (cases 1-4): a 1 kHz
// stereo sine in segments; all expect an integrated loudness of the listed value.
var ebuCases = map[int]struct {
	segs     []seg
	expected float64
}{
	1: {[]seg{{-23, 20}}, -23.0},
	2: {[]seg{{-33, 20}}, -33.0},
	3: {[]seg{{-36, 10}, {-23, 60}, {-36, 10}}, -23.0},
	4: {[]seg{{-72, 10}, {-36, 10}, {-23, 60}, {-36, 10}, {-72, 10}}, -23.0},
}

// EBU3341 writes the signal for the given case and returns the expected
// integrated loudness. Each channel carries amplitude 10^(dBFS/20) (no -ac
// upmix: it attenuates 3 dB).
func EBU3341(ctx context.Context, t mediatool.Tools, dst string, caseN int) (float64, error) {
	c, ok := ebuCases[caseN]
	if !ok {
		return 0, fmt.Errorf("synth: unknown EBU 3341 case %d", caseN)
	}
	if err := needAbs(dst); err != nil {
		return 0, err
	}
	var graph strings.Builder
	for i, s := range c.segs {
		a := strconv.FormatFloat(math.Pow(10, s.dBFS/20), 'f', 9, 64)
		ex := a + "*sin(2*PI*1000*t)"
		fmt.Fprintf(&graph, "aevalsrc='%s|%s':s=%d:d=%s[a%d];", ex, ex, sampleRate, strconv.FormatFloat(s.seconds, 'f', 3, 64), i)
	}
	for i := range c.segs {
		fmt.Fprintf(&graph, "[a%d]", i)
	}
	fmt.Fprintf(&graph, "concat=n=%d:v=0:a=1[out]", len(c.segs))
	tmp := dst + ".tmp"
	args := ffmpegArgs("-filter_complex", graph.String(), "-map", "[out]")
	args = append(args, flacOut...)
	args = append(args, "-f", "flac", "file:"+tmp)
	err := run(ctx, t.FFmpeg, args...)
	if err := publish(tmp, dst, err); err != nil {
		return 0, err
	}
	_ = os.Remove(tmp)
	return c.expected, nil
}
