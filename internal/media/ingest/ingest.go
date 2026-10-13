package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

type timeouts struct{ flacTest, metaflac, mp3 time.Duration }

// Pipeline ingests masters into DataDir.
type Pipeline struct {
	data    *os.Root
	dataDir string
	tools   mediatool.Tools
	an      analysis.Analyzer
	now     func() time.Time
	to      timeouts
}

// New builds a Pipeline. dataDir must be the absolute path data was opened on.
func New(data *os.Root, dataDir string, tools mediatool.Tools, an analysis.Analyzer, now func() time.Time) *Pipeline {
	if now == nil {
		now = time.Now
	}
	return &Pipeline{data: data, dataDir: dataDir, tools: tools, an: an, now: now,
		to: timeouts{flacTest: 120 * time.Second, metaflac: 30 * time.Second, mp3: 300 * time.Second}}
}

const maxToolOut = 1 << 20

// toolErr maps a tool failure to an ingest error. An exit status of a validator
// maps to invalid when invalidOnExit is set; timeouts and start failures are
// always TOOL_FAILED.
func toolErr(err error, invalidOnExit bool, detail string) *Error {
	var te *mediatool.ToolError
	if errors.As(err, &te) {
		code := CodeToolFailed
		if invalidOnExit && te.Err == nil && te.ExitCode > 0 {
			code = CodeInvalidAudio
		}
		return &Error{Code: code, Detail: detail + ": " + te.Error(), Err: err}
	}
	return &Error{Code: CodeToolFailed, Detail: detail, Err: err}
}

func sumFile(root *os.Root, rel string) (sum string, n int64, err error) {
	f, err := root.Open(rel)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// copyHashed copies src to a new file rel (0600, fsynced) and returns its sha256.
func (p *Pipeline) copyHashed(rel string, src io.Reader) (string, error) {
	f, err := p.data.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", &Error{Code: CodeIO, Path: rel, Detail: "create work file", Err: err}
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), src)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", &Error{Code: CodeIO, Path: rel, Detail: "copy", Err: err}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (p *Pipeline) writeWork(rel string, b []byte) error {
	if err := p.data.WriteFile(rel, b, 0o600); err != nil {
		return &Error{Code: CodeIO, Path: rel, Detail: "write work file", Err: err}
	}
	return nil
}

// Ingest freezes req.Source into the library (C15 steps 1-15).
func (p *Pipeline) Ingest(ctx context.Context, req Request) (res Result, err error) {
	if !filepath.IsAbs(p.dataDir) {
		return Result{}, errorf(CodeIO, "", "data directory must be absolute")
	}
	if !safeName(req.TrackID) || !safeName(req.Slug) {
		return Result{}, errorf(CodeIO, "", "unsafe track id or slug")
	}
	if req.Source == nil {
		return Result{}, errorf(CodeIO, "", "no source")
	}
	for _, a := range req.Artifacts {
		if !safeName(a.Name) {
			return Result{}, errorf(CodeIO, "", "unsafe artifact name")
		}
	}

	// 1. work dir
	work := "incoming/ingest-" + req.TrackID
	if err := p.data.MkdirAll("incoming", 0o755); err != nil {
		return Result{}, &Error{Code: CodeIO, Path: "incoming", Detail: "create", Err: err}
	}
	if err := p.data.Mkdir(work, 0o700); err != nil {
		return Result{}, &Error{Code: CodeIO, Path: work, Detail: "create work dir", Err: err}
	}
	cl := &claimer{data: p.data}
	defer func() {
		_ = p.data.RemoveAll(work)
		if err != nil {
			_ = discard(p.data, cl.created)
			res = Result{}
		}
	}()
	absWork := filepath.Join(p.dataDir, filepath.FromSlash(work))
	rel := func(name string) string { return work + "/" + name }
	abs := func(name string) string { return filepath.Join(absWork, name) }

	// 2. copy + verify source
	sum, cerr := p.copyHashed(rel("master.flac"), req.Source)
	if cerr != nil {
		return Result{}, cerr
	}
	if sum != strings.ToLower(req.SourceSHA256) {
		return Result{}, errorf(CodeSourceChanged, "", "source changed since it was inventoried")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, &Error{Code: CodeToolFailed, Detail: "cancelled", Err: err}
	}

	// 3. flac --test
	if _, err := mediatool.Run(ctx, p.tools.FLAC, []string{"--test", "--silent", abs("master.flac")},
		mediatool.RunOpts{Timeout: p.to.flacTest, MaxStdout: maxToolOut}); err != nil {
		return Result{}, toolErr(err, true, "flac --test")
	}

	// 4. stream info + pre-freeze frames hash
	pre, info, rerr := readInfo(p.data, rel("master.flac"))
	if rerr != nil {
		return Result{}, rerr
	}
	md5hex := hex.EncodeToString(info.StreamInfo.MD5[:])

	// 5. analysis
	loud, err := p.an.Loudness(ctx, abs("master.flac"))
	if err != nil {
		return Result{}, toolErr(err, false, "loudness")
	}
	peaks, spec, err := p.an.Scan(ctx, abs("master.flac"), int(info.StreamInfo.SampleRate), int(info.StreamInfo.Channels), info.StreamInfo.TotalSamples)
	if err != nil {
		var te *mediatool.ToolError
		if errors.As(err, &te) || errors.Is(err, analysis.ErrToolMissing) {
			return Result{}, toolErr(err, false, "scan")
		}
		return Result{}, &Error{Code: CodeInvalidAudio, Detail: "decode: " + err.Error(), Err: err}
	}

	// 6-7. tags
	gain, peak := replayGain(loud)
	lines := tagLines(req, gain, peak)
	if err := p.writeWork(rel("tags.txt"), []byte(strings.Join(lines, "\n")+"\n")); err != nil {
		return Result{}, err
	}

	// 8. the single metadata write
	if _, err := mediatool.Run(ctx, p.tools.Metaflac, []string{
		"--no-utf8-convert", "--remove-all-tags", "--import-tags-from=" + abs("tags.txt"),
		"--add-seekpoint=2s", abs("master.flac"),
	}, mediatool.RunOpts{Timeout: p.to.metaflac, MaxStdout: maxToolOut}); err != nil {
		return Result{}, toolErr(err, false, "metaflac")
	}

	// 9. verify the freeze
	post, pinfo, rerr := readInfo(p.data, rel("master.flac"))
	if rerr != nil {
		return Result{}, errorf(CodeFreezeInvariant, "", "frozen file unreadable")
	}
	switch {
	case pinfo.StreamInfo.MD5 != info.StreamInfo.MD5:
		return Result{}, errorf(CodeFreezeInvariant, "", "STREAMINFO MD5 changed")
	case post != pre:
		return Result{}, errorf(CodeFreezeInvariant, "", "audio frames changed")
	case pinfo.Count(flacmeta.BlockVorbisComment) != 1:
		return Result{}, errorf(CodeFreezeInvariant, "", "expected exactly one VORBIS_COMMENT block")
	case !slices.Equal(pinfo.Comments, lines):
		return Result{}, errorf(CodeFreezeInvariant, "", "comments differ from the written tags")
	case pinfo.Count(flacmeta.BlockSeekTable) < 1:
		return Result{}, errorf(CodeFreezeInvariant, "", "no SEEKTABLE")
	}

	// 10. master hash
	masterSum, masterBytes, serr := sumFile(p.data, rel("master.flac"))
	if serr != nil {
		return Result{}, &Error{Code: CodeIO, Detail: "hash master", Err: serr}
	}

	// 11. MP3 rendition
	gainDB := math.Min(0, -1.0-loud.TruePeakDBTP)
	gainDB = math.Round(gainDB*1000) / 1000
	if gainDB == 0 || math.IsNaN(gainDB) {
		gainDB = 0
	}
	ffTok := strings.TrimPrefix(p.an.ToolVersion(), "ffmpeg ")
	args := []string{"-nostdin", "-hide_banner", "-v", "error", "-protocol_whitelist", "file,pipe",
		"-i", "file:" + abs("master.flac"), "-map", "0:a:0", "-map_metadata", "-1",
		"-fflags", "+bitexact", "-flags:a", "+bitexact"}
	if gainDB != 0 {
		args = append(args, "-af", "volume="+strconv.FormatFloat(gainDB, 'f', 3, 64)+"dB")
	}
	args = append(args, "-c:a", "libmp3lame", "-b:a", "320k", "-ar", "48000", "-write_xing", "1",
		"-f", "mp3", "file:"+abs("hq.mp3"))
	if _, err := mediatool.Run(ctx, p.tools.FFmpeg, args, mediatool.RunOpts{Timeout: p.to.mp3, MaxStdout: maxToolOut}); err != nil {
		return Result{}, toolErr(err, false, "mp3 encode")
	}

	// 12. peaks file + derived hashes
	pj, merr := analysis.MarshalPeaks(peaks)
	if merr != nil {
		return Result{}, &Error{Code: CodeIO, Detail: "marshal peaks", Err: merr}
	}
	if err := p.writeWork(rel("peaks.json"), pj); err != nil {
		return Result{}, err
	}
	mp3Sum, mp3Bytes, serr := sumFile(p.data, rel("hq.mp3"))
	if serr != nil {
		return Result{}, &Error{Code: CodeIO, Detail: "hash mp3", Err: serr}
	}
	peaksSum, peaksBytes, serr := sumFile(p.data, rel("peaks.json"))
	if serr != nil {
		return Result{}, &Error{Code: CodeIO, Detail: "hash peaks", Err: serr}
	}

	// 13. artifacts
	var arts []Rendition
	if len(req.Artifacts) > 0 {
		if err := p.data.Mkdir(rel("artifacts"), 0o700); err != nil {
			return Result{}, &Error{Code: CodeIO, Detail: "create artifacts dir", Err: err}
		}
	}
	seen := map[string]bool{}
	for _, a := range req.Artifacts {
		if seen[a.Name] {
			return Result{}, errorf(CodeIO, "", "duplicate artifact name")
		}
		seen[a.Name] = true
		if a.File == nil {
			return Result{}, errorf(CodeIO, "", "artifact without file")
		}
		s, cerr := p.copyHashed(rel("artifacts/"+a.Name), a.File)
		if cerr != nil {
			return Result{}, cerr
		}
		if s != strings.ToLower(a.SHA256) {
			return Result{}, errorf(CodeSourceChanged, "", "artifact changed since it was inventoried")
		}
		_, n, serr := sumFile(p.data, rel("artifacts/"+a.Name))
		if serr != nil {
			return Result{}, &Error{Code: CodeIO, Detail: "stat artifact", Err: serr}
		}
		arts = append(arts, Rendition{Kind: "artifact", RelPath: "takes/" + req.TrackID + "/" + a.Name, SHA256: s, Bytes: n})
	}

	// 14. claim final names
	chmod := func(names ...string) error {
		for _, n := range names {
			if err := p.data.Chmod(rel(n), 0o444); err != nil {
				return &Error{Code: CodeIO, Path: rel(n), Detail: "chmod", Err: err}
			}
		}
		return nil
	}
	if err := chmod("master.flac", "hq.mp3", "peaks.json"); err != nil {
		return Result{}, err
	}
	for _, a := range req.Artifacts {
		if err := chmod("artifacts/" + a.Name); err != nil {
			return Result{}, err
		}
	}
	if beforeClaim != nil {
		beforeClaim(req.TrackID)
	}
	masterRel, cerr2 := cl.claimMaster(rel("master.flac"), req.Slug, req.Seed, req.RecordedAt, "flac")
	if cerr2 != nil {
		return Result{}, cerr2
	}
	derived := func(name, sum, ext string) (string, error) {
		final := "derived/" + sum[:2] + "/" + sum + ext
		if _, err := cl.link(rel(name), final); err != nil {
			return "", err
		}
		return final, nil
	}
	mp3Rel, derr := derived("hq.mp3", mp3Sum, ".mp3")
	if derr != nil {
		return Result{}, derr
	}
	peaksRel, derr := derived("peaks.json", peaksSum, ".peaks.json")
	if derr != nil {
		return Result{}, derr
	}
	for i, a := range req.Artifacts {
		exists, err := cl.link(rel("artifacts/"+a.Name), arts[i].RelPath)
		if err != nil {
			return Result{}, err
		}
		if exists {
			return Result{}, errorf(CodeIO, arts[i].RelPath, "artifact already exists")
		}
	}

	f, oerr := p.data.Open(masterRel)
	if oerr != nil {
		return Result{}, &Error{Code: CodeIO, Path: masterRel, Detail: "reopen master", Err: oerr}
	}
	st, oerr := f.Stat()
	var fin flacmeta.Info
	if oerr == nil {
		fin, oerr = flacmeta.Read(f, st.Size())
	}
	_ = f.Close()
	if oerr != nil {
		return Result{}, &Error{Code: CodeIO, Path: masterRel, Detail: "re-read frozen master", Err: oerr}
	}

	return Result{
		MasterRelPath: masterRel, MasterSHA256: masterSum, FramesSHA256: pre, AudioMD5: md5hex,
		MasterBytes: masterBytes, Info: fin,
		MP3: Rendition{Kind: "mp3", RelPath: mp3Rel, SHA256: mp3Sum, Bytes: mp3Bytes, Params: map[string]any{
			"encoder": "libmp3lame", "bitrate": "320k", "sample_rate": 48000, "gain_db": gainDB, "ffmpeg": ffTok}},
		Peaks:    Rendition{Kind: "peaks", RelPath: peaksRel, SHA256: peaksSum, Bytes: peaksBytes, Params: map[string]any{"format": analysis.PeaksFormat}},
		Loudness: loud, Spectrum: spec, ToolVersion: p.an.ToolVersion(),
		ReplayGainDB: gain, ReplayGainPeak: peak,
		Artifacts: arts, Created: slices.Clone(cl.created),
	}, nil
}

// readInfo parses a work file and returns its frames hash.
func readInfo(root *os.Root, rel string) (frames string, info flacmeta.Info, err error) {
	f, err := root.Open(rel)
	if err != nil {
		return "", info, &Error{Code: CodeIO, Path: path.Dir(rel), Detail: "open work file", Err: err}
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return "", info, &Error{Code: CodeIO, Detail: "stat work file", Err: err}
	}
	info, err = flacmeta.Read(f, st.Size())
	if err != nil {
		return "", info, &Error{Code: CodeInvalidAudio, Detail: "FLAC metadata: " + err.Error(), Err: err}
	}
	frames, err = flacmeta.FramesSHA256(f, st.Size(), info.FramesOffset)
	if err != nil {
		return "", info, &Error{Code: CodeInvalidAudio, Detail: "FLAC frames: " + err.Error(), Err: err}
	}
	return frames, info, nil
}

// IngestLossy freezes a standalone MP3 byte-for-byte as a lossy reference master (C60, D-M2c-3).
//
// STUB(M2c-B2): copy, verify, decode-based analysis, freeze and claim are implemented there.
func (p *Pipeline) IngestLossy(ctx context.Context, req Request) (Result, error) {
	return Result{}, errors.ErrUnsupported
}
