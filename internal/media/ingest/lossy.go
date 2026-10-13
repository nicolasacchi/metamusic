package ingest

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/media/mp3info"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// IngestLossy freezes a standalone MP3 byte-for-byte as a lossy reference master (C60, D-M2c-3):
// copy and verify, check it is an MPEG layer III stream, measure it from its decode, claim it under
// audio/masters/ and write peaks. The audio is never re-encoded and no tag is written.
func (p *Pipeline) IngestLossy(ctx context.Context, req Request) (res Result, err error) {
	if !filepath.IsAbs(p.dataDir) {
		return Result{}, errorf(CodeIO, "", "data directory must be absolute")
	}
	if !safeName(req.TrackID) || !safeName(req.Slug) {
		return Result{}, errorf(CodeIO, "", "unsafe track id or slug")
	}
	if req.Source == nil {
		return Result{}, errorf(CodeIO, "", "no source")
	}
	if len(req.Artifacts) > 0 {
		return Result{}, errorf(CodeIO, "", "a lossy reference take keeps no artifacts")
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
	rel := func(name string) string { return work + "/" + name }
	abs := func(name string) string { return filepath.Join(p.dataDir, filepath.FromSlash(work), name) }

	// 2. copy + verify source
	sum, cerr := p.copyHashed(rel("master.mp3"), req.Source)
	if cerr != nil {
		return Result{}, cerr
	}
	if sum != strings.ToLower(req.SourceSHA256) {
		return Result{}, errorf(CodeSourceChanged, "", "source changed since it was inventoried")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, &Error{Code: CodeToolFailed, Detail: "cancelled", Err: err}
	}

	// 3. it must be an MPEG audio layer III stream
	f, oerr := p.data.Open(rel("master.mp3"))
	if oerr != nil {
		return Result{}, &Error{Code: CodeIO, Path: work, Detail: "open work file", Err: oerr}
	}
	st, oerr := f.Stat()
	var info mp3info.Info
	if oerr == nil {
		info, oerr = mp3info.Parse(f, st.Size())
	}
	_ = f.Close()
	if errors.Is(oerr, mp3info.ErrNotMP3) {
		return Result{}, &Error{Code: CodeInvalidAudio, Detail: "not an MPEG audio layer III stream", Err: oerr}
	}
	if oerr != nil {
		return Result{}, &Error{Code: CodeIO, Path: work, Detail: "read work file", Err: oerr}
	}

	// 4. analysis from the decode: its frame count is the truth for the length
	actx, cancel := context.WithTimeout(ctx, p.to.decode)
	defer cancel()
	total, err := p.an.CountFrames(actx, abs("master.mp3"), info.SampleRate, info.Channels)
	if err != nil {
		return Result{}, toolErr(err, true, "decode")
	}
	if total == 0 {
		return Result{}, errorf(CodeInvalidAudio, "", "the stream decodes to no audio")
	}
	loud, err := p.an.Loudness(actx, abs("master.mp3"))
	if err != nil {
		return Result{}, toolErr(err, false, "loudness")
	}
	peaks, spec, err := p.an.Scan(actx, abs("master.mp3"), info.SampleRate, info.Channels, total)
	if err != nil {
		var te *mediatool.ToolError
		if errors.As(err, &te) || errors.Is(err, analysis.ErrToolMissing) {
			return Result{}, toolErr(err, false, "scan")
		}
		return Result{}, &Error{Code: CodeInvalidAudio, Detail: "decode: " + err.Error(), Err: err}
	}
	gain, peak := replayGain(loud)

	// 5. peaks file (no tag write, no MP3 encode)
	pj, merr := analysis.MarshalPeaks(peaks)
	if merr != nil {
		return Result{}, &Error{Code: CodeIO, Detail: "marshal peaks", Err: merr}
	}
	if err := p.writeWork(rel("peaks.json"), pj); err != nil {
		return Result{}, err
	}
	peaksSum, peaksBytes, serr := sumFile(p.data, rel("peaks.json"))
	if serr != nil {
		return Result{}, &Error{Code: CodeIO, Detail: "hash peaks", Err: serr}
	}

	// 6. claim final names
	for _, n := range []string{"master.mp3", "peaks.json"} {
		if err := p.data.Chmod(rel(n), 0o444); err != nil {
			return Result{}, &Error{Code: CodeIO, Path: rel(n), Detail: "chmod", Err: err}
		}
	}
	if beforeClaim != nil {
		beforeClaim(req.TrackID)
	}
	masterRel, cerr2 := cl.claimMaster(rel("master.mp3"), req.Slug, 0, req.RecordedAt, "mp3")
	if cerr2 != nil {
		return Result{}, cerr2
	}
	peaksRel := "derived/" + peaksSum[:2] + "/" + peaksSum + ".peaks.json"
	if _, err := cl.link(rel("peaks.json"), peaksRel); err != nil {
		return Result{}, err
	}

	// 7. the claimed master must still be the source, byte for byte
	got, masterBytes, serr := sumFile(p.data, masterRel)
	if serr != nil {
		return Result{}, &Error{Code: CodeIO, Path: masterRel, Detail: "re-read frozen master", Err: serr}
	}
	if got != sum {
		return Result{}, errorf(CodeFreezeInvariant, "", "frozen master differs from the source")
	}

	return Result{
		MasterRelPath: masterRel, MasterSHA256: sum, FramesSHA256: sum, AudioMD5: strings.Repeat("0", 32),
		MasterBytes: masterBytes,
		Peaks:       Rendition{Kind: "peaks", RelPath: peaksRel, SHA256: peaksSum, Bytes: peaksBytes, Params: map[string]any{"format": analysis.PeaksFormat}},
		Loudness:    loud, Spectrum: spec, ToolVersion: p.an.ToolVersion(),
		ReplayGainDB: gain, ReplayGainPeak: peak,
		Created: slices.Clone(cl.created),
		Lossy: &LossyInfo{SampleRate: info.SampleRate, Channels: info.Channels, TotalSamples: total,
			DurationS: float64(total) / float64(info.SampleRate)},
	}, nil
}
