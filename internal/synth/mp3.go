package synth

import (
	"context"
	"errors"
	"os"
	"strconv"

	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// MP3Options shapes the MP3 produced by MP3. Zero values mean 44100 Hz,
// stereo, 128 kbps, with an ID3v2 tag.
type MP3Options struct {
	SampleRate int
	Channels   int
	BitrateK   int
	NoID3      bool // omit the leading ID3v2 tag (the file starts with a frame sync)
}

// MP3 renders the tone exactly as FLAC does (48 kHz, loudness-calibrated), then
// resamples and encodes it with libmp3lame (Xing header written) and publishes
// it atomically at dst (absolute path).
func MP3(ctx context.Context, t mediatool.Tools, dst string, tone Tone, o MP3Options) error {
	if err := needAbs(dst); err != nil {
		return err
	}
	if o.SampleRate == 0 {
		o.SampleRate = 44100
	}
	if o.Channels == 0 {
		o.Channels = 2
	}
	if o.BitrateK == 0 {
		o.BitrateK = 128
	}
	if o.Channels != 1 && o.Channels != 2 {
		return errors.New("synth: mp3 channels must be 1 or 2")
	}
	src := dst + ".src.flac.tmp"
	defer func() { _ = os.Remove(src) }()
	// FLAC publishes by rename, so src is a complete file when it returns.
	if err := FLAC(ctx, t, src, tone, StyleFFmpeg); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	args := ffmpegArgs("-i", "file:"+src, "-map", "0:a:0", "-map_metadata", "-1",
		"-ar", strconv.Itoa(o.SampleRate), "-ac", strconv.Itoa(o.Channels),
		"-c:a", "libmp3lame", "-b:a", strconv.Itoa(o.BitrateK)+"k", "-write_xing", "1")
	if o.NoID3 {
		args = append(args, "-id3v2_version", "0")
	} else {
		args = append(args, "-id3v2_version", "3", "-metadata", "title=synthetic")
	}
	args = append(args, "-f", "mp3", "file:"+tmp)
	return publish(tmp, dst, run(ctx, t.FFmpeg, args...))
}
