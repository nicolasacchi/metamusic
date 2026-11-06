package voice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"path"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

// Input is one voice note upload.
type Input struct {
	AudioBase64 string
	AtSecond    *float64
	Kind        string // note (default), fix or keep
	Body        string // empty: "[voice note m:ss]"
}

// Reasons of VOICE_INVALID (details.reason).
const (
	ReasonEncoding  = "encoding"
	ReasonSize      = "size"
	ReasonContainer = "container"
	ReasonDecode    = "decode"
	ReasonTooLong   = "too_long"
)

// invalid is the 422 VOICE_INVALID answer. It never carries audio bytes or tool output.
func invalid(reason, msg string) error {
	return problem.E(http.StatusUnprocessableEntity, "VOICE_INVALID", msg, map[string]any{"reason": reason})
}

// Create stores a voice note and its comment in one transaction and returns the comment id. src is the comment transport label.
// Only the operator and their devices may record (403 HUMAN_ONLY). Nothing is stored unless the upload validated: container
// by magic, codec, a successful decode and a duration within MaxSeconds.
func (s *Service) Create(ctx context.Context, p auth.Principal, src, trackID string, in Input) (string, error) {
	if !p.ActsAsHuman() {
		return "", problem.E(http.StatusForbidden, "HUMAN_ONLY", "only the operator records voice notes", nil)
	}
	if err := s.Available(ctx); err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	kind := in.Kind
	if kind == "" {
		kind = "note"
	}
	if kind != "note" && kind != "fix" && kind != "keep" {
		return "", problem.E(http.StatusUnprocessableEntity, "VALIDATION", "kind must be note, fix or keep", map[string]any{"field": "kind"})
	}
	if base64.StdEncoding.DecodedLen(len(in.AudioBase64)) > MaxBytes+2 {
		return "", invalid(ReasonSize, "the recording is larger than 4 MiB")
	}
	raw, err := base64.StdEncoding.DecodeString(in.AudioBase64)
	if err != nil || len(raw) == 0 {
		return "", invalid(ReasonEncoding, "audio_base64 is not valid base64")
	}
	if len(raw) > MaxBytes {
		return "", invalid(ReasonSize, "the recording is larger than 4 MiB")
	}
	container, codec, ok := Detect(raw)
	if !ok {
		return "", invalid(ReasonContainer, "the recording must be webm or ogg with opus, or mp4 with aac or opus")
	}
	var trackDur float64
	switch err := s.o.Store.R.QueryRowContext(ctx, `SELECT duration_s FROM tracks WHERE id = ?`, trackID).Scan(&trackDur); {
	case errors.Is(err, sql.ErrNoRows):
		return "", library.ErrNotFound
	case err != nil:
		return "", fmt.Errorf("voice: track lookup: %w", err)
	}
	var at *float64
	if in.AtSecond != nil {
		t := *in.AtSecond
		if math.IsNaN(t) || math.IsInf(t, 0) || t < 0 {
			return "", &judgments.Error{Status: http.StatusUnprocessableEntity, Code: "VALIDATION", Field: "at_second", Msg: "at_second must be 0 or more"}
		}
		if t > trackDur {
			return "", &judgments.Error{Status: http.StatusUnprocessableEntity, Code: "AT_SECOND_OUT_OF_RANGE", Field: "at_second",
				Msg: fmt.Sprintf("at_second %.1f is after the end of the track (%.1f s)", t, trackDur)}
		}
		t = math.Round(t*10) / 10
		at = &t
	}

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}

	rel, abs, err := s.workDir()
	if err != nil {
		return "", err
	}
	defer func() { _ = s.o.Data.RemoveAll(rel) }()
	ext := map[string]string{ContainerWebM: "webm", ContainerOgg: "ogg", ContainerMP4: "mp4"}[container]
	if err := s.o.Data.WriteFile(rel+"/in."+ext, raw, 0o600); err != nil {
		return "", fmt.Errorf("voice: write upload: %w", err)
	}
	dur, err := s.duration(ctx, abs+"/in."+ext)
	if err != nil {
		return "", err
	}
	if err := s.encode(ctx, abs+"/in."+ext, abs+"/out.mp3"); err != nil {
		return "", err
	}
	mp3, err := s.o.Data.ReadFile(rel + "/out.mp3")
	if err != nil || len(mp3) == 0 {
		return "", invalid(ReasonDecode, "the recording could not be converted")
	}
	sum, mp3sum := sha256.Sum256(raw), sha256.Sum256(mp3)
	sha, msha := hex.EncodeToString(sum[:]), hex.EncodeToString(mp3sum[:])
	origRel, mp3Rel := objectPath(sha, ext), objectPath(msha, "mp3")
	if err := s.publish(rel+"/in."+ext, origRel); err != nil {
		return "", err
	}
	if err := s.publish(rel+"/out.mp3", mp3Rel); err != nil {
		return "", err
	}

	id, commentID := uuid.NewV7().String(), uuid.NewV7().String()
	if s.o.NewID != nil {
		id, commentID = s.o.NewID(), s.o.NewID()
	}
	body := in.Body
	if body == "" {
		body = fmt.Sprintf("[voice note %d:%02d]", int(dur)/60, int(dur)%60)
	}
	now := s.o.Now()
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("voice: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var atArg any
	if at != nil {
		atArg = *at
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO voice_notes(id, track_id, sha256, bytes, container, codec, path, mp3_sha256, mp3_path, mp3_bytes,
		duration_s, at_second, principal_kind, principal_id, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, trackID, sha, len(raw), container, codec, origRel, msha, mp3Rel, len(mp3), dur, atArg, string(p.Kind), p.ID, now.UTC().Format(time.RFC3339)); err != nil {
		return "", fmt.Errorf("voice: insert note: %w", err)
	}
	if err := judgments.AddVoiceCommentTx(ctx, tx, judgments.NewVoiceComment{ID: commentID, TrackID: trackID, VoiceNoteID: id,
		Body: body, Kind: kind, Source: src, AtSecond: at, Author: p, Now: now}); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("voice: commit: %w", err)
	}
	audit.NoteCreated(ctx, id)
	return commentID, nil
}

// objectPath is the content-addressed place of a file: voice/<aa>/<sha>.<ext>.
func objectPath(sha, ext string) string { return "voice/" + sha[:2] + "/" + sha + "." + ext }

// workDir makes a private work dir under incoming/ and returns its data-relative and absolute paths.
func (s *Service) workDir() (rel, abs string, err error) {
	if err := s.o.Data.MkdirAll("incoming", 0o755); err != nil {
		return "", "", fmt.Errorf("voice: create incoming: %w", err)
	}
	rel = "incoming/voice-" + uuid.NewV7().String()
	if err := s.o.Data.Mkdir(rel, 0o700); err != nil {
		return "", "", fmt.Errorf("voice: create work dir: %w", err)
	}
	return rel, s.o.DataDir + "/" + rel, nil
}

// publish freezes the work file (0444) and hard-links it into place. An existing object of the same address is the same
// content and is kept as is (never rewritten).
func (s *Service) publish(workRel, dst string) error {
	if err := s.o.Data.Chmod(workRel, 0o444); err != nil {
		return fmt.Errorf("voice: chmod: %w", err)
	}
	if err := s.o.Data.MkdirAll(path.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("voice: create dir: %w", err)
	}
	if err := s.o.Data.Link(workRel, dst); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("voice: link into place: %w", err)
	}
	return nil
}

// duration decodes the upload to 16 kHz mono PCM and measures it. Stdout is capped at 125 s of samples.
func (s *Service) duration(ctx context.Context, in string) (float64, error) {
	args := []string{"-nostdin", "-hide_banner", "-nostats", "-v", "error", "-protocol_whitelist", "file,pipe",
		"-i", "file:" + in, "-map", "0:a:0", "-ac", "1", "-ar", "16000", "-f", "s16le", "-acodec", "pcm_s16le", "-"}
	out, err := mediatool.Run(ctx, s.o.FFmpeg, args, mediatool.RunOpts{Timeout: decodeTimeout, MaxStdout: maxDecodeBytes})
	switch {
	case errors.Is(err, mediatool.ErrOutputLimit):
		return 0, invalid(ReasonTooLong, "the recording is longer than 120 seconds")
	case ctx.Err() != nil:
		return 0, ctx.Err()
	case err != nil:
		return 0, invalid(ReasonDecode, "the recording could not be decoded")
	case len(out) < 2:
		return 0, invalid(ReasonDecode, "the recording has no audio")
	}
	d := float64(len(out)/2) / decodeRate
	if d > maxDurationS {
		return 0, invalid(ReasonTooLong, "the recording is longer than 120 seconds")
	}
	// the stored duration is positive by construction (the table CHECK)
	return math.Max(d, 0.001), nil
}

// encode renders the 64 kbps mono MP3 playback rendition (bit-exact, no metadata).
func (s *Service) encode(ctx context.Context, in, out string) error {
	args := []string{"-nostdin", "-hide_banner", "-nostats", "-v", "error", "-protocol_whitelist", "file,pipe",
		"-i", "file:" + in, "-map", "0:a:0", "-map_metadata", "-1", "-fflags", "+bitexact", "-flags:a", "+bitexact",
		"-ac", "1", "-ar", "44100", "-c:a", "libmp3lame", "-b:a", fmt.Sprintf("%dk", MP3Kbps), "-f", "mp3", "file:" + out}
	var sink bytes.Buffer
	if _, err := mediatool.Run(ctx, s.o.FFmpeg, args, mediatool.RunOpts{Timeout: encodeTimeout, Stdout: &sink}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return invalid(ReasonDecode, "the recording could not be converted")
	}
	return nil
}
