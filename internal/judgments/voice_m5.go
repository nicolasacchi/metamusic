package judgments

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
)

// VoiceLinker mints the signed playback link of a voice note's MP3 rendition (C259). The voice service implements it; the
// judgments service never learns where the files live.
type VoiceLinker interface {
	VoiceURL(ctx context.Context, mp3SHA256 string) string
}

// UseVoice wires the link minter. Without it, comments carry no voice field.
func (s *Service) UseVoice(l VoiceLinker) { s.voice = l }

// CommentByID returns one comment, voice included.
func (s *Service) CommentByID(ctx context.Context, id string) (Comment, error) {
	return s.comment(ctx, id)
}

// attachVoice fills c.Voice for a comment that carries a voice note. Only the MP3 rendition is ever linked.
func (s *Service) attachVoice(ctx context.Context, c *Comment) error {
	if s.voice == nil {
		return nil
	}
	var v VoiceRef
	var mp3 string
	err := s.st.R.QueryRowContext(ctx, `SELECT v.id, v.duration_s, v.mp3_sha256, v.container, v.codec, v.bytes
		FROM comments c JOIN voice_notes v ON v.id = c.voice_note_id WHERE c.id = ?`, c.ID).
		Scan(&v.ID, &v.DurationS, &mp3, &v.Container, &v.Codec, &v.Bytes)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("judgments: voice note: %w", err)
	}
	v.URL = s.voice.VoiceURL(ctx, mp3)
	c.Voice = &v
	return nil
}

// NewVoiceComment is the input of AddVoiceCommentTx: the comment of a voice note, written in the voice service's transaction.
type NewVoiceComment struct {
	ID, TrackID, VoiceNoteID, Body, Kind, Source string
	AtSecond                                     *float64
	Author                                       auth.Principal
	Now                                          time.Time
}

// AddVoiceCommentTx inserts the open comment of a voice note inside tx; the voice_notes row must already exist in tx. It
// validates like AddComment (kind, 1..4000 body characters, at_second inside the track, rounded to 0.1 s) and computes the
// section label from the timed sections, but never reads outside tx.
func AddVoiceCommentTx(ctx context.Context, tx *sql.Tx, in NewVoiceComment) error {
	if !slices.Contains(commentSrcs, in.Source) {
		return inv("source", "unknown comment source %q", in.Source)
	}
	if !slices.Contains(commentKinds, in.Kind) {
		return inv("kind", "must be note, fix, keep or marker")
	}
	body, err := cleanBody(in.Body)
	if err != nil {
		return err
	}
	var dur float64
	switch err := tx.QueryRowContext(ctx, `SELECT duration_s FROM tracks WHERE id = ?`, in.TrackID).Scan(&dur); {
	case errors.Is(err, sql.ErrNoRows):
		return library.ErrNotFound
	case err != nil:
		return fmt.Errorf("judgments: track lookup: %w", err)
	}
	var at, label, lsrc, lconf any
	if in.AtSecond != nil {
		t := *in.AtSecond
		if math.IsNaN(t) || math.IsInf(t, 0) || t < 0 {
			return inv("at_second", "must be 0 or more")
		}
		if t > dur {
			return &Error{Status: http.StatusUnprocessableEntity, Code: "AT_SECOND_OUT_OF_RANGE", Field: "at_second",
				Msg: fmt.Sprintf("at_second %.1f is after the end of the track (%.1f s)", t, dur)}
		}
		t = math.Round(t*10) / 10
		at = t
		sec, err := sectionFor(ctx, tx, in.TrackID, t)
		if err != nil {
			return err
		}
		if sec != nil {
			label, lsrc, lconf = sec.Label, sec.Source, sec.Confidence
		}
	}
	now := in.Now.UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, track_id, body, at_second, section_label, section_source, section_confidence, kind, state, source,
		principal_kind, principal_id, principal_name, created_at, updated_at, voice_note_id) VALUES (?,?,?,?,?,?,?,?,'open',?,?,?,?,?,?,?)`,
		in.ID, in.TrackID, body, at, label, lsrc, lconf, in.Kind, in.Source, string(in.Author.Kind), in.Author.Key(), in.Author.Name, now, now, in.VoiceNoteID); err != nil {
		return fmt.Errorf("judgments: add voice comment: %w", err)
	}
	audit.NoteCreated(ctx, in.ID)
	return nil
}
