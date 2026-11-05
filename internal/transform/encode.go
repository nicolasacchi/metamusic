package transform

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// Media is the tool-facing half of an attempt (C210.6 steps 2 and 4): decode a frozen master to raw float32 and encode the
// child's 24-bit PCM to FLAC. The production ToolMedia runs ffmpeg through mediatool; transformtest.FakeMedia stands in for the
// fast test tier. Every method returns the wall time of the tool run (it counts toward cpu_ms) and an error that never carries
// argv or a path.
type Media interface {
	// Decode writes src's audio to dst as interleaved little-endian float32 at the file's native rate and channel count
	// (D-T1-19: no -ar, no -ac).
	Decode(ctx context.Context, src, dst string) (time.Duration, error)
	// Encode turns the interleaved stereo 24-bit little-endian PCM at pcm into a 24-bit FLAC at out.
	Encode(ctx context.Context, pcm, out string, rate int) (time.Duration, error)
	// Versions reports the tool versions recorded in transform.json (information only, D-T1-19).
	Versions(ctx context.Context) ToolVersions
}

// ToolVersions is the tool_versions object of transform.json.
type ToolVersions struct {
	FFmpeg string `json:"ffmpeg"`
	FLAC   string `json:"flac"`
}

// ToolMedia is Media over ffmpeg and flac. Its runs use mediatool's process-group mode so a cancel kills ffmpeg's helpers too.
type ToolMedia struct {
	Tools mediatool.Tools
	// DecodeTimeout and EncodeTimeout default to 10 minutes: the wall deadline of the child bounds the render, these bound a
	// stuck tool.
	DecodeTimeout, EncodeTimeout time.Duration
}

const toolTimeout = 10 * time.Minute

// decodeArgs is the decode argv: native format only, so decode is exact and an ffmpeg upgrade cannot change the PCM.
func decodeArgs(src, dst string) []string {
	return []string{"-nostdin", "-hide_banner", "-v", "error", "-protocol_whitelist", "file", "-i", "file:" + src,
		"-map", "0:a:0", "-f", "f32le", "file:" + dst}
}

// encodeArgs is the 24-bit FLAC argv of C210.6(4), verified at T1-X0 (metaflac --show-bps = 24); the leading flags only make
// the run quiet and non-interactive.
func encodeArgs(pcm, out string, rate int) []string {
	return []string{"-nostdin", "-hide_banner", "-v", "error",
		"-f", "s24le", "-ar", strconv.Itoa(rate), "-ac", "2", "-i", "file:" + pcm,
		"-c:a", "flac", "-compression_level", "8", "-sample_fmt", "s32", "-bits_per_raw_sample", "24", "file:" + out}
}

// Decode implements Media.
func (m ToolMedia) Decode(ctx context.Context, src, dst string) (time.Duration, error) {
	return m.run(ctx, m.Tools.FFmpeg, decodeArgs(src, dst), m.DecodeTimeout)
}

// Encode implements Media.
func (m ToolMedia) Encode(ctx context.Context, pcm, out string, rate int) (time.Duration, error) {
	return m.run(ctx, m.Tools.FFmpeg, encodeArgs(pcm, out, rate), m.EncodeTimeout)
}

func (m ToolMedia) run(ctx context.Context, bin string, args []string, timeout time.Duration) (time.Duration, error) {
	if timeout <= 0 {
		timeout = toolTimeout
	}
	start := time.Now()
	_, err := mediatool.Run(ctx, bin, args, mediatool.RunOpts{Timeout: timeout, MaxStdout: 1 << 20, Group: true})
	return time.Since(start), err
}

// Versions implements Media: the first line of `ffmpeg -version` and `flac --version`, "" when a tool cannot run.
func (m ToolMedia) Versions(ctx context.Context) ToolVersions {
	first := func(bin string, args ...string) string {
		out, err := mediatool.Run(ctx, bin, args, mediatool.RunOpts{Timeout: 10 * time.Second, MaxStdout: 1 << 16})
		if err != nil {
			return ""
		}
		line, _, _ := strings.Cut(string(out), "\n")
		return strings.TrimSpace(line)
	}
	return ToolVersions{FFmpeg: first(m.Tools.FFmpeg, "-version"), FLAC: first(m.Tools.FLAC, "--version")}
}
