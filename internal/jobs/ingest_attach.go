package jobs

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/engine/acestep"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/upload"
)

// The attach half of the ingest (E2b, C166, D-E2-11, D-E2-22). Main's ingest ends every completed job in a new tracks row. Two
// kinds of output are not a take: a stem (task extract) is attached to its source take as the artifact stem-<name>.flac, and
// lyrics.lrc rides along with a normal text2music take as the artifact lyrics.lrc.

// Codes of a failed stem attach (jobs INGEST_FAILED detail ingest_code).
const (
	codeStemRequest = "STEM_REQUEST" // the recipe is not a one-stem extract on the job's source take
	codeStemLength  = "STEM_LENGTH"  // the stem's length differs from its source's by more than 0.1 s
)

const (
	lrcArtifact   = "lyrics.lrc"
	lrcMaxBytes   = 1 << 20
	stemRate      = 48000
	stemTolerance = 0.1 // seconds
)

// lrcLine is the shape of every non-empty line of a lyrics.lrc: a [mm:ss.xx] time stamp first.
var lrcLine = regexp.MustCompile(`^\[\d{2,}:\d{2}\.\d{2,3}\]`)

// lrcProblem validates a received lyrics.lrc: at most 1 MiB, UTF-8, no NUL, at least one line, every non-empty line starting with
// a time stamp. It returns "" for a good file and the reason it is dropped otherwise.
func (b *IngestBridge) lrcProblem(u upload.Received) string {
	raw, err := b.o.Data.ReadFile(u.Path)
	if err != nil {
		return "unreadable"
	}
	return lrcReason(raw)
}

func lrcReason(raw []byte) string {
	switch {
	case len(raw) > lrcMaxBytes:
		return "too_large"
	case !utf8.Valid(raw) || strings.ContainsRune(string(raw), 0):
		return "not_utf8"
	}
	lines := 0
	for _, ln := range strings.Split(strings.TrimPrefix(string(raw), "\ufeff"), "\n") {
		ln = strings.TrimRight(ln, "\r")
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if !lrcLine.MatchString(ln) {
			return "bad_line"
		}
		lines++
	}
	if lines == 0 {
		return "empty"
	}
	return ""
}

// attachStem implements the attach shape of C166 for a finished extract job: the rendered audio.flac is verified (digest, FLAC,
// 48 kHz, length = the source's within 0.1 s) and attached to the SOURCE take as stem-<name>.flac (a repeated name becomes
// stem-<name>-<n>.flac), frozen at 0444. No tracks row, no Ears analysis, no Song, Pick, recipe key or mutation row (a mutation
// needs a child take; the job's own mutation_json records operator extract_stems against the parent). The job completes with
// track_id = the source take, outcome clean and the worker's env (set on the job row here). It returns the source take id.
func (b *IngestBridge) attachStem(ctx context.Context, owner string, job JobRow, att AttemptRow, files []upload.Received, out provider.Output, eng ingestEngine,
	req recipeInfo, byName map[string]upload.Received) (string, error) {
	r, _, issues := acestep.Parse([]byte(req.requestJSON))
	for _, i := range issues {
		if i.Severity == "error" {
			return "", &IngestFailedError{Code: codeStemRequest, Err: errors.New(i.Message)}
		}
	}
	if r.Task != acestep.TaskExtract || len(r.Tracks) != 1 || job.SourceTrackID == nil || (r.Src != nil && r.Src.TrackID != *job.SourceTrackID) {
		return "", &IngestFailedError{Code: codeStemRequest, Err: errors.New("not a one-stem extract of the job's source take")}
	}
	parent, stem := *job.SourceTrackID, r.Tracks[0]
	var parentS float64
	if err := b.o.Store.R.QueryRowContext(ctx, `SELECT duration_s FROM tracks WHERE id = ?`, parent).Scan(&parentS); err != nil {
		return "", &IngestFailedError{Code: codeStemRequest, Err: fmt.Errorf("source take: %w", err)}
	}
	envID, err := b.envFor(ctx, att, out)
	if err != nil {
		return "", err
	}

	// copy the received audio to a work file, hashing it, then verify it before it touches the take's directory
	audio := byName["audio.flac"]
	work := "incoming/attach-" + job.ID
	_ = b.o.Data.Remove(work)
	sum, size, err := b.copyToWork(audio, work)
	if err != nil {
		return "", err
	}
	defer func() { _ = b.o.Data.Remove(work) }() // the hard link, once made, keeps the file
	if sum != strings.ToLower(audio.SHA256) {
		return "", &IngestFailedError{Code: ingest.CodeSourceChanged, Err: errors.New("stem changed since it was inventoried")}
	}
	if err := verifyStem(b.o.Data, work, size, parentS); err != nil {
		return "", err
	}

	var created string
	err = b.fencedTx(ctx, owner, job, func(tx *sql.Tx, f store.LeaseFence) error {
		gpu, err := b.putEnv(ctx, tx, f, job, att, out, envID)
		if err != nil {
			return err
		}
		if b.o.Spend != nil && gpu != "" {
			if err := b.recordEngineRTF(ctx, tx, job, eng, req, out); err != nil {
				return err
			}
		}
		_, n, err := library.NextStemArtifact(ctx, tx, parent, stem)
		if err != nil {
			return err
		}
		var artifact string
		// an orphan file of a crashed attach can hold the name without a row: the next number is free
		for ; ; n++ {
			artifact = library.StemArtifactName(stem, n)
			created, err = b.linkStem(work, parent, artifact)
			if err == nil {
				break
			}
			if !errors.Is(err, fs.ErrExist) || n >= 99 {
				return &IngestFailedError{Code: ingest.CodeIO, Err: err}
			}
		}
		if err := b.o.Library.AttachArtifact(ctx, tx, parent, library.NewArtifact{Name: artifact, SHA256: sum, Path: created, Bytes: size}); err != nil {
			return err
		}
		// the job keeps the worker's environment (a take would carry it on its tracks row)
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET env_id = ? WHERE id = ? AND state = 'ingesting' AND lease_owner = ?`+f.And(), //nolint:gosec // G202: fixed C69 predicate
			append([]any{envID, job.ID, f.Owner}, f.Args()...)...); err != nil {
			return fmt.Errorf("jobs: attach env: %w", err)
		}
		detail := map[string]any{"track_id": parent, "artifact": artifact}
		if out.AudioSeconds != nil {
			if v, err := out.AudioSeconds.Float64(); err == nil && v > 0 {
				detail["audio_s"] = v
			}
		}
		if err := insertEvent(ctx, tx, f, Event{JobID: job.ID, AttemptID: att.ID, Name: EvAttached, Detail: detail}); err != nil {
			return fmt.Errorf("jobs: attach event: %w", err)
		}
		ids := make([]string, 0, len(byName))
		for _, u := range byName {
			ids = append(ids, u.UploadID)
		}
		return b.o.Uploads.Consume(ctx, tx, ids)
	})
	if err != nil {
		if created != "" {
			_ = b.o.Data.Remove(created)
		}
		var ife *IngestFailedError
		if errors.As(err, &ife) || errors.Is(err, store.ErrLeaseLost) || errors.Is(err, errStale) {
			return "", err
		}
		b.o.Log.Warn("attach commit", "err", err.Error())
		return "", &IngestFailedError{Code: "DB_INSERT", Err: err}
	}
	ids := make([]string, 0, len(files))
	for _, f := range files {
		ids = append(ids, f.UploadID)
	}
	if err := b.o.Uploads.RemoveConsumedFiles(ctx, ids); err != nil {
		b.o.Log.Warn("remove consumed uploads", "job", job.ID)
	}
	return parent, nil
}

// copyToWork copies a received upload to a new 0444 work file and returns its sha256 and size.
func (b *IngestBridge) copyToWork(u upload.Received, work string) (sum string, size int64, err error) {
	src, err := b.o.Data.Open(u.Path)
	if err != nil {
		return "", 0, &IngestFailedError{Code: ingest.CodeSourceChanged, Err: err}
	}
	defer func() { _ = src.Close() }()
	if err := b.o.Data.MkdirAll("incoming", 0o755); err != nil {
		return "", 0, &IngestFailedError{Code: ingest.CodeIO, Err: err}
	}
	dst, err := b.o.Data.OpenFile(work, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, &IngestFailedError{Code: ingest.CodeIO, Err: err}
	}
	h := sha256.New()
	size, err = io.Copy(io.MultiWriter(dst, h), src)
	if err == nil {
		err = dst.Sync()
	}
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = b.o.Data.Chmod(work, 0o444)
	}
	if err != nil {
		_ = b.o.Data.Remove(work)
		return "", 0, &IngestFailedError{Code: ingest.CodeIO, Err: err}
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

// verifyStem checks the work file is a FLAC at 48 kHz whose length is the source's within 0.1 s.
func verifyStem(data *os.Root, work string, size int64, sourceS float64) error {
	f, err := data.Open(work)
	if err != nil {
		return &IngestFailedError{Code: ingest.CodeIO, Err: err}
	}
	defer func() { _ = f.Close() }()
	info, err := flacmeta.Read(f, size)
	if err != nil {
		return &IngestFailedError{Code: ingest.CodeInvalidAudio, Err: fmt.Errorf("stem FLAC: %w", err)}
	}
	if info.StreamInfo.SampleRate != stemRate || info.StreamInfo.TotalSamples == 0 {
		return &IngestFailedError{Code: ingest.CodeInvalidAudio, Err: fmt.Errorf("stem is %d Hz with %d samples, want %d Hz", info.StreamInfo.SampleRate, info.StreamInfo.TotalSamples, stemRate)}
	}
	if d := math.Abs(info.Duration() - sourceS); d > stemTolerance {
		return &IngestFailedError{Code: codeStemLength, Err: fmt.Errorf("stem is %.3f s, its source %.3f s", info.Duration(), sourceS)}
	}
	return nil
}

// linkStem hard-links the verified work file to takes/<parent>/<artifact> (fs.ErrExist when the name is taken) and returns the
// relative path.
func (b *IngestBridge) linkStem(work, parent, artifact string) (string, error) {
	dir := path.Join("takes", parent)
	if err := b.o.Data.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	final := path.Join(dir, artifact)
	if err := b.o.Data.Link(work, final); err != nil {
		return "", err
	}
	return final, nil
}
