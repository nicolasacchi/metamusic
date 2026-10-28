package tape

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
)

// The production Pipeline (C128.4). Every argv is literals plus app-created absolute paths: no title, tag or Muse text is ever
// passed to a tool; those reach the file only through the Go writers of flacmeta.

const (
	coversDir     = "derived/covers" // where cover.Service keeps its PNGs; TestCoverPath_MatchesService pins the layout
	workParent    = "incoming"
	trackFile     = "tape.flac"
	finalFile     = "final.flac"
	minToolBudget = 120 * time.Second
	// perItemSlack and perLossyItemSlack pad the sample count handed to the encoder as --input-size: it must not be lower
	// than the real count (the encoder would truncate), and an excess only shortens nothing (it warns and finishes).
	perItemSlack      = 480    // 10 ms
	perLossyItemSlack = 12_000 // 250 ms: decoder delay and padding of a lossy source
)

type pipeline struct {
	o     PipelineOptions
	free  func(string) (uint64, error)
	newID func() string
}

// NewPipeline builds the production Pipeline: ffmpeg decode, the Go gain stage, the reference flac encoder, the Go metadata
// rewrite and the verification.
func NewPipeline(o PipelineOptions) Pipeline {
	return &pipeline{o: o, free: freeBytes, newID: randomID}
}

func randomID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (p *pipeline) log(msg string, args ...any) {
	if p.o.Log != nil {
		p.o.Log.Info(msg, args...)
	}
}

// decodeArgs is the ffmpeg argv of one item: first audio stream, no metadata, 48 kHz, stereo (a mono source is duplicated to
// both channels, anything wider is folded down by the default matrix), raw s32le on stdout.
func decodeArgs(masterAbs string, upmixed bool) []string {
	a := []string{"-nostdin", "-hide_banner", "-nostats", "-v", "error", "-protocol_whitelist", "file,pipe",
		"-i", "file:" + masterAbs, "-map", "0:a:0", "-map_metadata", "-1", "-vn"}
	if upmixed {
		a = append(a, "-af", "pan=stereo|c0=c0|c1=c0")
	} else {
		a = append(a, "-ac", "2")
	}
	return append(a, "-ar", "48000", "-f", "s32le", "pipe:1")
}

// encodeArgs is the flac argv: raw 24-bit stereo from stdin, -8, no padding. inputSamples (an upper bound of the real count)
// lets the encoder lay a seek table (one point every 10 s) although stdin has no length; the real STREAMINFO count wins.
func encodeArgs(outAbs string, inputSamples int64) []string {
	return []string{"--silent", "--force", "--force-raw-format", "--endian=little", "--sign=signed", "--channels=2", "--bps=24",
		"--sample-rate=48000", "-8", "--no-padding", fmt.Sprintf("--input-size=%d", inputSamples*6), "--seekpoint=10s", "-o", outAbs, "-"}
}

func testArgs(fileAbs string) []string { return []string{"--silent", "--test", fileAbs} }

// toolBudget is max(120 s, 0.5 x the tape's duration).
func toolBudget(durationS float64) time.Duration {
	return max(minToolBudget, time.Duration(durationS*0.5*float64(time.Second)))
}

// estimateSamples is an upper bound of the tape's sample count from the items' durations.
func estimateSamples(items []PlannedItem) int64 {
	var n int64
	for _, it := range items {
		slack := int64(perItemSlack)
		if it.Item.Lossy {
			slack = perLossyItemSlack
		}
		n += int64(math.Ceil(it.Item.DurationS*Rate)) + slack
	}
	return n
}

// CoverRel is the data-relative path of the cached PNG of a cover key (the cover service's layout).
func CoverRel(key string) string { return coversDir + "/" + key[:2] + "/" + key + ".png" }

func validCoverKey(k string) bool {
	if len(k) != 64 {
		return false
	}
	for i := range len(k) {
		if c := k[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Render implements Pipeline. On success the finished FLAC is Result.Path inside a private work directory that
// Result.Cleanup removes; on any failure or cancel the work directory is already gone and nothing is left behind.
func (p *pipeline) Render(ctx context.Context, j Job, progress func(Progress)) (res Result, err error) {
	if progress == nil {
		progress = func(Progress) {}
	}
	n := len(j.Items)
	switch {
	case n == 0 || n > MaxItems:
		return Result{}, renderErr(ErrEncodeFailed, 0, fmt.Errorf("a tape has 1 to %d items, got %d", MaxItems, n))
	case len(j.Chapters) != n:
		return Result{}, renderErr(ErrEncodeFailed, 0, errors.New("chapter names do not match the items"))
	case p.o.Data == nil || !filepath.IsAbs(p.o.DataDir) || p.o.Tools.FFmpeg == "" || p.o.Tools.FLAC == "":
		return Result{}, renderErr(ErrEncodeFailed, 0, errors.New("the pipeline is not configured"))
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	var durationS float64
	for i, it := range j.Items {
		st, err := p.o.Data.Stat(it.Item.MasterRel)
		if err != nil || !st.Mode().IsRegular() || st.Size() != it.Item.MasterBytes {
			return Result{}, renderErr(ErrSourceChanged, i+1, nil)
		}
		durationS += it.Item.DurationS
	}
	if err := p.o.Data.MkdirAll(workParent, 0o755); err != nil {
		return Result{}, renderErr(ErrEncodeFailed, 0, err)
	}
	if err := checkDisk(p.free, filepath.Join(p.o.DataDir, workParent), DiskNeeded(durationS)); err != nil {
		return Result{}, err
	}
	rel := workParent + "/tape-" + p.newID()
	if err := p.o.Data.Mkdir(rel, 0o700); err != nil {
		return Result{}, renderErr(ErrEncodeFailed, 0, err)
	}
	cleanup := func() { _ = p.o.Data.RemoveAll(rel) }
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	work := filepath.Join(p.o.DataDir, rel)

	frames, peak, err := p.transcode(ctx, j, work, durationS, progress)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	res, err = p.finish(ctx, j, rel, work, frames, peak, durationS, progress)
	if err != nil {
		return Result{}, err
	}
	res.Cleanup = cleanup
	return res, nil
}

// transcode runs the encoder and feeds it item by item through the gain stage (the concurrency rules of C128.4). It returns
// the frame count of every item and the post-gain sample peak.
func (p *pipeline) transcode(ctx context.Context, j Job, work string, durationS float64, progress func(Progress)) (frames []int64, peak int32, err error) {
	ctx2, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	pr, pw := io.Pipe()
	encDone := make(chan struct{})
	encOut := filepath.Join(work, trackFile)
	go func() {
		defer close(encDone)
		_, e := mediatool.Run(ctx2, p.o.Tools.FLAC, encodeArgs(encOut, estimateSamples(j.Items)),
			mediatool.RunOpts{Timeout: toolBudget(durationS), Stdin: pr, Stdout: io.Discard})
		if e == nil {
			pr.Close() // a clean exit follows the end of input; if the feeder is still writing, its next write fails
			return
		}
		ce := renderErr(ErrEncodeFailed, 0, e)
		cancel(ce) // first: the encoder's failure must win over the decode errors it causes
		pr.CloseWithError(ce)
	}()

	fail := func(e error) error {
		cancel(e)
		pw.CloseWithError(e)
		<-encDone
		return context.Cause(ctx2)
	}
	n := len(j.Items)
	frames = make([]int64, 0, n)
	for i, it := range j.Items {
		progress(Progress{Phase: "decode", Item: i + 1, Items: n})
		stage := newGainStage(pw, it.Gain.GainCDB)
		_, runErr := mediatool.Run(ctx2, p.o.Tools.FFmpeg, decodeArgs(filepath.Join(p.o.DataDir, it.Item.MasterRel), it.Upmixed),
			mediatool.RunOpts{Timeout: ItemTimeout, Stdout: stage})
		closeErr := stage.Close()
		if runErr != nil || closeErr != nil {
			if ctx.Err() != nil {
				return nil, 0, fail(ctx.Err())
			}
			switch {
			case errors.Is(stage.err, errClipped):
				return nil, 0, fail(renderErr(ErrClipped, i+1, nil))
			case stage.err != nil && !errors.Is(stage.err, errPartialFrame):
				// the downstream write failed: the encoder side is the cause (already recorded when it died first)
				return nil, 0, fail(renderErr(ErrEncodeFailed, 0, stage.err))
			}
			e := runErr
			if e == nil {
				e = closeErr
			}
			return nil, 0, fail(renderErr(ErrDecodeFailed, i+1, e))
		}
		frames = append(frames, stage.frames)
		peak = max(peak, stage.peak)
	}
	progress(Progress{Phase: "encode", Item: n, Items: n})
	pw.Close()
	<-encDone
	if cause := context.Cause(ctx2); cause != nil {
		return nil, 0, cause
	}
	return frames, peak, nil
}

// finish reads the encoded tape, writes the chapters, tags and cover with Rewrite, and verifies the result.
func (p *pipeline) finish(ctx context.Context, j Job, rel, work string, frames []int64, peak int32, durationS float64, progress func(Progress)) (Result, error) {
	n := len(j.Items)
	var total int64
	for _, f := range frames {
		total += f
	}
	root := p.o.Data
	tapeFile, err := root.Open(rel + "/" + trackFile)
	if err != nil {
		return Result{}, renderErr(ErrEncodeFailed, 0, err)
	}
	defer tapeFile.Close()
	st, err := tapeFile.Stat()
	if err != nil {
		return Result{}, renderErr(ErrEncodeFailed, 0, err)
	}
	info, err := flacmeta.Read(tapeFile, st.Size())
	if err != nil {
		return Result{}, renderErr(ErrEncodeFailed, 0, err)
	}
	si := info.StreamInfo
	if si.SampleRate != Rate || si.BitsPerSample != Bits || si.Channels != 2 {
		return Result{}, renderErr(ErrEncodeFailed, 0, fmt.Errorf("encoded format is %d Hz %d-bit %d ch", si.SampleRate, si.BitsPerSample, si.Channels))
	}
	if int64(si.TotalSamples) != total { //nolint:gosec // 36-bit field
		return Result{}, renderErr(ErrSampleMismatch, 0, fmt.Errorf("encoder wrote %d samples, decoders produced %d", si.TotalSamples, total))
	}
	if st.Size() > MaxBytes {
		return Result{}, renderErr(ErrEncodeFailed, 0, errors.New("the tape exceeds the size limit"))
	}

	progress(Progress{Phase: "rewrite", Item: n, Items: n})
	chapters := PlanChapters(j.Items, frames)
	cue := CuesheetOf(chapters)
	if err := flacmeta.ValidateCuesheet(cue); err != nil {
		return Result{}, renderErr(ErrEncodeFailed, 0, err)
	}
	comments := make([]flacmeta.Comment, 0, len(j.Tags)+2*n+2)
	for _, c := range j.Tags {
		if c.Key == PeakTag(0).Key || c.Key == cueKey || strings.HasPrefix(c.Key, "CHAPTER") {
			continue // written below from the render itself
		}
		comments = append(comments, c)
	}
	comments = append(comments, PeakTag(peak))
	comments = append(comments, ChapterTags(j.Chapters, chapters)...)
	if err := flacmeta.ValidateComments(comments); err != nil {
		return Result{}, renderErr(ErrEncodeFailed, 0, err)
	}
	vendor := "metamusic " + p.o.Version
	vc := flacmeta.VorbisComment(vendor, comments)
	cueBlock := flacmeta.CuesheetBlock(cue)
	rep := flacmeta.Replace{Vorbis: vc, Cuesheet: cueBlock, Picture: []byte{}}
	var coverPNG []byte
	if j.CoverKey != "" {
		if !validCoverKey(j.CoverKey) {
			return Result{}, renderErr(ErrEncodeFailed, 0, errors.New("bad cover key"))
		}
		if coverPNG, err = root.ReadFile(CoverRel(j.CoverKey)); err != nil {
			return Result{}, renderErr(ErrEncodeFailed, 0, fmt.Errorf("read cover: %w", err))
		}
		pic, err := pictureOf(coverPNG)
		if err != nil {
			return Result{}, renderErr(ErrEncodeFailed, 0, err)
		}
		rep.Picture = flacmeta.PictureBlock(pic)
	}
	if len(vc) > flacmeta.MaxBlockLen {
		return Result{}, renderErr(ErrEncodeFailed, 0, errors.New("tags exceed the block size limit"))
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	finalRel := rel + "/" + finalFile
	out, err := root.OpenFile(finalRel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Result{}, renderErr(ErrEncodeFailed, 0, err)
	}
	bw := bufio.NewWriterSize(out, 1<<20)
	_, err = flacmeta.Rewrite(bw, tapeFile, st.Size(), info, rep)
	if err == nil {
		err = bw.Flush()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Result{}, renderErr(ErrEncodeFailed, 0, err)
	}

	progress(Progress{Phase: "verify", Item: n, Items: n})
	finalAbs := filepath.Join(work, finalFile)
	if _, err := mediatool.Run(ctx, p.o.Tools.FLAC, testArgs(finalAbs), mediatool.RunOpts{Timeout: toolBudget(durationS), MaxStdout: 1 << 10}); err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{}, renderErr(ErrVerifyFailed, 0, err)
	}
	sha, size, err := p.verifyFinal(rel, finalRel, tapeFile, st.Size(), info, vendor, comments, chapters, j.Chapters, coverPNG, total)
	if err != nil {
		return Result{}, err
	}
	p.log("tape rendered", "items", n, "samples", total, "bytes", size)
	return Result{Path: finalAbs, SHA256: sha, Bytes: size, TotalSamples: total, Chapters: chapters, PeakSample: float64(peak) / (1 << 23)}, nil
}

func pictureOf(b []byte) (flacmeta.Picture, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return flacmeta.Picture{}, fmt.Errorf("cover is not a PNG: %w", err)
	}
	p := flacmeta.Picture{Type: 3, MIME: "image/png", W: uint32(cfg.Width), H: uint32(cfg.Height), Depth: 8, Data: b} //nolint:gosec // DecodeConfig bounds them
	if pal, ok := cfg.ColorModel.(color.Palette); ok {
		p.Colors = uint32(len(pal)) //nolint:gosec // at most 256
	}
	return p, nil
}

// verifyFinal parses final.flac back against the plan: frames identical to tape.flac, tags, chapters in all three forms,
// cuesheet and picture. It returns the sha256 and size of the file.
func (p *pipeline) verifyFinal(rel, finalRel string, tapeFile *os.File, tapeSize int64, tapeInfo flacmeta.Info, vendor string,
	comments []flacmeta.Comment, chapters []Chapter, names []string, coverPNG []byte, total int64) (string, int64, error) {
	bad := func(format string, a ...any) (string, int64, error) {
		return "", 0, renderErr(ErrVerifyFailed, 0, fmt.Errorf(format, a...))
	}
	f, err := p.o.Data.Open(finalRel)
	if err != nil {
		return bad("open final: %v", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return bad("stat final: %v", err)
	}
	info, err := flacmeta.Read(f, st.Size())
	if err != nil {
		return bad("read final: %v", err)
	}
	a, err := flacmeta.FramesSHA256(tapeFile, tapeSize, tapeInfo.FramesOffset)
	if err != nil {
		return bad("hash tape frames: %v", err)
	}
	b, err := flacmeta.FramesSHA256(f, st.Size(), info.FramesOffset)
	if err != nil {
		return bad("hash final frames: %v", err)
	}
	if a != b || info.StreamInfo != tapeInfo.StreamInfo {
		return bad("the audio frames or STREAMINFO changed in the rewrite")
	}
	if int64(info.StreamInfo.TotalSamples) != total { //nolint:gosec // 36-bit field
		return bad("sample count changed")
	}
	if info.Vendor != vendor {
		return bad("vendor string differs")
	}
	want := make([]string, len(comments))
	for i, c := range comments {
		want[i] = c.Key + "=" + c.Value
	}
	if !slices.Equal(want, info.Comments) {
		return bad("comments differ from the plan")
	}
	cue, ok, err := flacmeta.ReadCuesheet(f, st.Size(), info)
	if err != nil || !ok {
		return bad("cuesheet block missing or unreadable")
	}
	wantCue := CuesheetOf(chapters)
	if cue.LeadOut != wantCue.LeadOut || len(cue.Tracks) != len(wantCue.Tracks) {
		return bad("cuesheet block differs from the plan")
	}
	for i, t := range cue.Tracks {
		w := wantCue.Tracks[i]
		if t.Offset != w.Offset || t.Number != w.Number || !slices.Equal(t.Indexes, w.Indexes) {
			return bad("cuesheet track %d differs from the plan", i+1)
		}
	}
	if err := checkChapterForms(info.Comments, chapters, names); err != nil {
		return bad("%v", err)
	}
	pics, err := flacmeta.ReadPictures(f, st.Size(), info)
	if err != nil {
		return bad("picture block unreadable")
	}
	switch {
	case coverPNG == nil && len(pics) != 0:
		return bad("unexpected picture")
	case coverPNG != nil && (len(pics) != 1 || pics[0].Type != 3 || pics[0].MIME != "image/png" || !bytes.Equal(pics[0].Data, coverPNG)):
		return bad("picture differs from the cover")
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, 0, st.Size())); err != nil {
		return bad("hash final: %v", err)
	}
	return hex.EncodeToString(h.Sum(nil)), st.Size(), nil
}

// checkChapterForms asserts that the CHAPTERnnn comments and the text cuesheet agree with the chapters: milliseconds exact,
// cue frames to the floor of the 75 fps position, names equal.
func checkChapterForms(comments []string, chapters []Chapter, names []string) error {
	pc, err := ParseChapterComments(comments)
	if err != nil || len(pc) != len(chapters) {
		return fmt.Errorf("chapter comments differ from the plan")
	}
	var cueVal string
	for _, c := range comments {
		if v, ok := strings.CutPrefix(c, cueKey+"="); ok {
			cueVal = v
		}
	}
	pcue, err := ParseCueText(cueVal)
	if err != nil || len(pcue) != len(chapters) {
		return fmt.Errorf("text cuesheet differs from the plan")
	}
	for i, ch := range chapters {
		if pc[i].MS != ch.StartMS || pc[i].Name != oneLine(names[i]) {
			return fmt.Errorf("chapter %d comment differs from the plan", i+1)
		}
		if pcue[i].Track != ch.Index || pcue[i].Frames != cueFrames(ch.StartSample) {
			return fmt.Errorf("chapter %d text cuesheet differs from the plan", i+1)
		}
	}
	return nil
}
