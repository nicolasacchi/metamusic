package synth

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/analysis/ffmpeg"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// Tone describes a synthetic track.
type Tone struct {
	LUFS        float64 // target integrated loudness
	Seconds     float64
	BandLimitHz int // 0 = full band (sines + clicks); otherwise band-limited noise up to this frequency
	BPM         int // click track; 0 = none
	Variant     int // shifts the partials (or the noise seed), giving different frames
}

// FLACStyle selects the metadata shape of the generated FLAC.
type FLACStyle int

const (
	// StyleFFmpeg is ffmpeg's default output: an encoder tag and PADDING.
	StyleFFmpeg FLACStyle = iota
	// StyleYuE2 mirrors the shape YuE2 writes: a VORBIS_COMMENT block holding the
	// libFLAC vendor string and zero comments, no PADDING, no SEEKTABLE.
	StyleYuE2
)

const (
	baseGainDB   = 0.0
	calTolerance = 0.05
)

// source returns the lavfi graph rendering the tone at unit calibration gain.
func (t Tone) source(gainDB float64) string {
	total := int64(math.Round(t.Seconds * sampleRate))
	vol := "volume=" + strconv.FormatFloat(gainDB, 'f', 6, 64) + "dB"
	if t.BandLimitHz > 0 {
		return fmt.Sprintf("anoisesrc=r=%d:d=%s:a=0.5:c=white:seed=%d,aresample=%d:resampler=swr:filter_size=512:cutoff=0.99:kaiser_beta=9,pan=stereo|c0=c0|c1=c0,apad,atrim=end_sample=%d,%s",
			2*t.BandLimitHz, strconv.FormatFloat(t.Seconds+1, 'f', 3, 64), t.Variant+1, sampleRate, total, vol)
	}
	f1 := 220 * (1 + 0.07*float64(t.Variant))
	f2 := 331 * (1 + 0.05*float64(t.Variant))
	click := "0"
	if t.BPM > 0 {
		click = fmt.Sprintf("if(lt(mod(t,%.6f),0.012),0.3*sin(2*PI*1500*t),0)", 60/float64(t.BPM))
	}
	l := fmt.Sprintf("0.25*sin(2*PI*%.3f*t)+0.15*sin(2*PI*%.3f*t)+%s", f1, f2, click)
	r := fmt.Sprintf("0.2*sin(2*PI*%.3f*t)+0.18*sin(2*PI*%.3f*t)+%s", f1, f2, click)
	return fmt.Sprintf("aevalsrc='%s|%s':s=%d:d=%s,%s", l, r, sampleRate, strconv.FormatFloat(t.Seconds, 'f', 6, 64), vol)
}

func render(ctx context.Context, ff string, graph string, outArgs []string, dst string) error {
	args := ffmpegArgs("-f", "lavfi", "-i", graph)
	args = append(args, outArgs...)
	args = append(args, "-f", outFormat(outArgs), "file:"+dst)
	return run(ctx, ff, args...)
}

func outFormat(outArgs []string) string {
	for i, a := range outArgs {
		if a == "-c:a" && i+1 < len(outArgs) && outArgs[i+1] == "pcm_s24le" {
			return "wav"
		}
	}
	return "flac"
}

var flacOut = []string{"-c:a", "flac", "-sample_fmt", "s32", "-bits_per_raw_sample", "24"}
var wavOut = []string{"-c:a", "pcm_s24le"}

// FLAC renders the tone to dst (absolute path), calibrated in two passes
// (render, measure with ebur128, correct, re-render) to |I - LUFS| <= 0.05.
func FLAC(ctx context.Context, t mediatool.Tools, dst string, tone Tone, style FLACStyle) error {
	if err := needAbs(dst); err != nil {
		return err
	}
	if math.IsNaN(tone.LUFS) || math.IsInf(tone.LUFS, 0) {
		return errors.New("synth: tone needs a finite target loudness")
	}
	if tone.Seconds <= 0 || math.IsNaN(tone.Seconds) || math.IsInf(tone.Seconds, 0) {
		return errors.New("synth: tone needs a positive duration")
	}
	if tone.BandLimitHz < 0 || tone.BandLimitHz > sampleRate/2 {
		return errors.New("synth: band limit out of range")
	}
	an, err := ffmpeg.New(ctx, t)
	if err != nil {
		return err
	}
	probe := dst + ".cal.tmp"
	defer func() { _ = os.Remove(probe) }()
	if err := render(ctx, t.FFmpeg, tone.source(baseGainDB), flacOut, probe); err != nil {
		return err
	}
	l, err := an.Loudness(ctx, probe)
	if err != nil {
		return err
	}
	if l.IntegratedLUFS <= -69 {
		return errors.New("synth: tone is silent")
	}
	gain := tone.LUFS - l.IntegratedLUFS
	graph := tone.source(baseGainDB + gain)
	tmp := dst + ".tmp"
	switch style {
	case StyleYuE2:
		wav := dst + ".wav.tmp"
		defer func() { _ = os.Remove(wav) }()
		if err := render(ctx, t.FFmpeg, graph, wavOut, wav); err != nil {
			return err
		}
		err = run(ctx, t.FLAC, "-s", "-f", "--no-seektable", "--no-padding", "-o", tmp, wav)
		if err == nil {
			err = run(ctx, t.Metaflac, "--remove-all-tags", "--dont-use-padding", tmp)
		}
	default:
		err = render(ctx, t.FFmpeg, graph, flacOut, tmp)
	}
	if err == nil {
		var m analysis.Loudness
		if m, err = an.Loudness(ctx, tmp); err == nil && math.Abs(m.IntegratedLUFS-tone.LUFS) > calTolerance {
			err = fmt.Errorf("synth: calibration missed the target (%.2f LUFS measured, %.2f wanted; the target may clip)", m.IntegratedLUFS, tone.LUFS)
		}
	}
	return publish(tmp, dst, err)
}

// FLACUncalibrated renders the tone to dst (absolute path) in ONE render pass, without loudness
// calibration (Tone.LUFS is ignored): the fake worker and the E2E rigs need distinct audio quickly,
// not a measured level (M3 C78.3). The output has the requested metadata style and the exact length
// of Tone.Seconds at 48 kHz/24 bit stereo; distinct Variants give distinct frames.
func FLACUncalibrated(ctx context.Context, t mediatool.Tools, dst string, tone Tone, style FLACStyle) error {
	if err := needAbs(dst); err != nil {
		return err
	}
	if tone.Seconds <= 0 || math.IsNaN(tone.Seconds) || math.IsInf(tone.Seconds, 0) {
		return errors.New("synth: tone needs a positive duration")
	}
	if tone.BandLimitHz < 0 || tone.BandLimitHz > sampleRate/2 {
		return errors.New("synth: band limit out of range")
	}
	graph := tone.source(baseGainDB)
	tmp := dst + ".tmp"
	var err error
	switch style {
	case StyleYuE2:
		wav := dst + ".wav.tmp"
		defer func() { _ = os.Remove(wav) }()
		if err = render(ctx, t.FFmpeg, graph, wavOut, wav); err != nil {
			return err
		}
		err = run(ctx, t.FLAC, "-s", "-f", "--no-seektable", "--no-padding", "-o", tmp, wav)
		if err == nil {
			err = run(ctx, t.Metaflac, "--remove-all-tags", "--dont-use-padding", tmp)
		}
	default:
		err = render(ctx, t.FFmpeg, graph, flacOut, tmp)
	}
	return publish(tmp, dst, err)
}
