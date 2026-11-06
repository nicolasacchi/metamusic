package fake

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/synth"
)

// InputFetcher is how the simulated worker reads its ACE-Step inputs: a GET of a signed media URL. Implementations
// must never log or return the URL (it carries a capability signature).
type InputFetcher interface {
	Get(ctx context.Context, url string) (status int, body []byte, err error)
}

// maxFetchBytes bounds one fetched input (the provider's own limit for a frozen master).
const maxFetchBytes = 192 << 20

// aceDecoder is the VAE the ACE-Step worker reports as env.decoder (C153.1).
const aceDecoder = "ACE-Step/Ace-Step1.5:vae"

func (s *Session) decoder() string {
	if s.ace {
		return aceDecoder
	}
	return "m-a-p/YuE2-Vae"
}

// aceRequest holds the fields of an ACE-Step request the simulation reads.
type aceRequest struct {
	ID         string `json:"id"`
	Task       string `json:"task"`
	Lyrics     string `json:"lyrics"`
	BPM        *int   `json:"bpm"`
	DurationDS int    `json:"duration_ds"`
	Seed       int64  `json:"seed"`
	LRC        bool   `json:"lrc"`
	ExtendDS   *int   `json:"extend_ds"`
	Src        *struct {
		DurationDS int `json:"duration_ds"`
	} `json:"src"`
	Ref *struct {
		ClipSHA256 string `json:"clip_sha256"`
	} `json:"ref"`
	Tracks []string `json:"tracks"`
}

// fetchInputs performs step 1b: every input is fetched and checked against its digest. A non-empty code is the
// worker error to answer with.
func (s *Session) fetchInputs(ctx context.Context) (code, msg string) {
	f := s.w.o.Fetcher
	if f == nil {
		return "", ""
	}
	for _, name := range []string{provider.InputSrcAudio, provider.InputReferenceAudio} {
		in, ok := s.in.Inputs[name]
		if !ok {
			continue
		}
		status, body, err := f.Get(ctx, in.URL)
		if err != nil || status < 200 || status > 299 {
			return "INPUT_FAILED", name + " could not be fetched"
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != in.SHA256 || (in.Bytes > 0 && int64(len(body)) != in.Bytes) {
			return "INPUT_MISMATCH", name + " does not match its digest"
		}
	}
	return "", ""
}

// audioScale is the cap on really synthesised seconds: the option, else MM_FAKE_AUDIO_SCALE; 0 means none.
func (w *SimWorker) audioScale() float64 {
	if w.o.AudioScale > 0 {
		return w.o.AudioScale
	}
	if v, err := strconv.ParseFloat(os.Getenv("MM_FAKE_AUDIO_SCALE"), 64); err == nil && v > 0 {
		return v
	}
	return 0
}

// SynthesizedSeconds is the total seconds of audio the ACE-Step takes of this worker asked the renderer for
// (MM_FAKE_AUDIO_SCALE keeps it small while accounted lengths stay the request's).
func (w *SimWorker) SynthesizedSeconds() float64 { return float64(w.synth.Load()) / 1000 }

func hash64(parts ...string) uint64 {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return binary.BigEndian.Uint64(sum[:8])
}

// accountedSeconds is the audio length the take reports: text2music duration_ds/10; repaint, complete and extract
// the source length; extend the source plus extend_ds/10.
func (r aceRequest) accountedSeconds() float64 {
	src := 0
	if r.Src != nil {
		src = r.Src.DurationDS
	}
	switch r.Task {
	case "repaint", "complete", "extract":
		return float64(src) / 10
	case "extend":
		ext := 0
		if r.ExtendDS != nil {
			ext = *r.ExtendDS
		}
		return float64(src+ext) / 10
	}
	return float64(r.DurationDS) / 10
}

// lrc is one stamp per 4 s per lyric line (section headers and blank lines are skipped).
func (r aceRequest) lrc() []byte {
	var b strings.Builder
	i := 0
	for _, ln := range strings.Split(strings.ReplaceAll(r.Lyrics, "\r\n", "\n"), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || headerRe.MatchString(ln) {
			continue
		}
		t := i * 4
		fmt.Fprintf(&b, "[%02d:%02d.00] %s\n", t/60, t%60, ln)
		i++
	}
	return []byte(b.String())
}

func (s *Session) renderACE(ctx context.Context) ([]artifact, float64, error) {
	var req aceRequest
	if err := json.Unmarshal(s.in.Request, &req); err != nil {
		return nil, 0, err
	}
	secs := req.accountedSeconds()
	if secs <= 0 {
		return nil, 0, fmt.Errorf("the request has no positive length")
	}
	rendered := secs
	if sc := s.w.audioScale(); sc > 0 && sc < rendered {
		rendered = sc
	}
	gen := s.w.o.RenderDuration.Seconds()
	if gen <= 0 {
		gen = 1
	}
	result := jsonIndent(map[string]any{
		"id": req.ID, "task": req.Task, "seed": req.Seed, "audio_seconds": secs, "duration": secs,
		"timing": map[string]float64{"e2e_seconds": gen + 0.2, "generate_s": gen},
	})
	arts := []artifact{{"request.json", append([]byte(nil), s.in.Request...)}, {"result.json", result}}
	if req.LRC {
		arts = append(arts, artifact{"lyrics.lrc", req.lrc()})
	}
	// The audio identity: the attempt's, mixed with the reference digest (a reference render's tone shifts
	// deterministically) or replaced by the stem name (a stem is a distinct tone per name).
	id := audioIdentity(s.in.AttemptID, s.mk.sameAudio)
	if req.Ref != nil {
		id ^= hash64("ref", req.Ref.ClipSHA256)
	}
	if req.Task == "extract" && len(req.Tracks) > 0 {
		id = hash64("stem", strings.Join(req.Tracks, ","))
	}
	var audio []byte
	if f := s.w.o.HeaderOnlyFLAC; f != nil {
		audio = f(id, rendered)
	} else {
		dir, err := os.MkdirTemp(s.w.o.TempDir, "simworker-")
		if err != nil {
			return nil, 0, err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		variant := 0
		if !s.mk.sameAudio || req.Ref != nil || req.Task == "extract" {
			variant = int(id%1000) + 1
		}
		bpm := 120
		if req.BPM != nil && *req.BPM > 0 {
			bpm = *req.BPM
		}
		path := filepath.Join(dir, "audio.flac")
		if err := synth.FLACUncalibrated(ctx, s.w.o.Tools, path, synth.Tone{Seconds: rendered, BPM: bpm, Variant: variant}, synth.StyleFFmpeg); err != nil {
			return nil, 0, err
		}
		if audio, err = os.ReadFile(path); err != nil { // #nosec G304 -- a file this function just wrote in its own temp dir
			return nil, 0, err
		}
	}
	s.w.synth.Add(int64(rendered * 1000))
	return append(arts, artifact{"audio.flac", audio}), secs, nil
}
