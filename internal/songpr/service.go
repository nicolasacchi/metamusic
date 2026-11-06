package songpr

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/anchor"
	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Options builds the Song PR service (C253).
type Options struct {
	Store *store.Store
	Now   func() time.Time // nil => time.Now
	NewID func() string    // nil => UUIDv7
}

// Service stores and reads Song PRs and answers generation's PatchSource. It is the only writer of patches and patch_comments.
type Service struct{ o Options }

// New builds the service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewID == nil {
		o.NewID = func() string { return uuid.NewV7().String() }
	}
	return &Service{o: o}
}

func perr(status int, code, msg string, details map[string]any) error {
	return problem.E(status, code, msg, details)
}

func notFound() error { return perr(http.StatusNotFound, "NOT_FOUND", "no such resource", nil) }

// ready refuses a service built without its dependencies (the registry tests build an empty one).
func (s *Service) ready() error {
	if s.o.Store == nil {
		return perr(http.StatusServiceUnavailable, "UNAVAILABLE", "songpr is not available", map[string]any{"component": "songpr"})
	}
	return nil
}

// OpJSON is one operation as it is proposed and stored (patches.ops_json): replace_line carries section_idx, line_idx and text,
// set_style carries style.
type OpJSON struct {
	Op         string  `json:"op"`
	SectionIdx *int    `json:"section_idx,omitempty"`
	LineIdx    *int    `json:"line_idx,omitempty"`
	Text       *string `json:"text,omitempty"`
	Style      *string `json:"style,omitempty"`
}

// ProposeInput is propose_patch's body (C253.1).
type ProposeInput struct {
	BaseTrackID         string
	AddressesCommentIDs []string
	Rationale           string
	Title               *string
	ExtraSections       []int
	Ops                 []OpJSON
}

// Author is the principal that proposed a patch, as stored.
type Author struct{ Key, Kind, Name string }

// Address is a comment a patch addresses (its current kind and state; the excerpt is empty for a hidden comment).
type Address struct{ CommentID, TrackID, Kind, State, BodyExcerpt string }

// Render is one job of a patch: a take of one of its batches.
type Render struct {
	BatchID, JobID, State string
	Seed                  int64
	ChildTrackID          *string
}

// Tally counts the callbacks of a patch by the human's answer.
type Tally struct{ Total, Answered, Fixed, NotYet, Worse, Kept, Lost int }

// Base names the take a patch applies to.
type Base struct{ TrackID, Title, RecipeHash string }

// Summary is a Song PR without its ops, diff and compiled patch.
type Summary struct {
	ID        string
	Base      Base
	Operator  string
	Title     *string
	Rationale string
	Addresses []Address
	Renders   []Render
	Callbacks Tally
	Author    Author
	CreatedAt string
}

// SongPR is a stored patch.
type SongPR struct {
	Summary
	Ops      []OpJSON
	Diff     Diff
	Bounds   Scope
	Compiled Patch
}

func excerpt(s string) string {
	if utf8.RuneCountInString(s) <= 200 {
		return s
	}
	return string([]rune(s)[:200])
}

// q is the read surface shared by *sql.DB and *sql.Tx.
type q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// baseTake is what a proposal needs of its base take.
type baseTake struct {
	title, status, engine, recipeHash, requestJSON string
	lossy                                          bool
}

func loadBase(ctx context.Context, x q, id string) (baseTake, error) {
	var b baseTake
	var lossy int
	err := x.QueryRowContext(ctx, `SELECT t.title, t.status, t.lossy, r.hash, r.engine, r.request_json FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, id).
		Scan(&b.title, &b.status, &lossy, &b.recipeHash, &b.engine, &b.requestJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return b, perr(http.StatusUnprocessableEntity, "UNKNOWN_TRACK", "no such track", map[string]any{"track_id": id})
	}
	if err != nil {
		return b, fmt.Errorf("songpr: base take: %w", err)
	}
	b.lossy = lossy == 1
	return b, nil
}

// lineageIDs is the base take and its ancestors up to the root.
func lineageIDs(ctx context.Context, x q, id string) (map[string]bool, error) {
	rows, err := x.QueryContext(ctx, `WITH RECURSIVE anc(id, parent) AS (SELECT id, parent_track_id FROM tracks WHERE id = ?
		UNION ALL SELECT t.id, t.parent_track_id FROM tracks t JOIN anc a ON t.id = a.parent) SELECT id FROM anc`, id)
	if err != nil {
		return nil, fmt.Errorf("songpr: lineage: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out[t] = true
	}
	return out, rows.Err()
}

// spotOf maps a comment onto the lyrics sections: the section kind and ordinal of its time on its own track (the best timed
// source at read time, D-M5-7), else the stored section label, else nothing (the proposer must name extra sections).
func spotOf(t anchor.Timing, at *float64, label sql.NullString) *Spot {
	if at == nil {
		return nil
	}
	if pt := anchor.Compute(t, *at); pt.Basis == anchor.BasisSection {
		return &Spot{Kind: pt.Kind, Ordinal: pt.Ordinal, Label: label.String}
	}
	if label.Valid && label.String != "" {
		return &Spot{Label: label.String}
	}
	return nil
}

func compileProblem(ce *CompileError, baseTrack string) error {
	code := ce.Code()
	d := map[string]any{"violations": ce.Violations}
	var ids []string
	for _, v := range ce.Violations {
		if v.Code != code {
			continue
		}
		ids = append(ids, v.CommentIDs...)
	}
	for _, v := range ce.Violations {
		if v.Code != code {
			continue
		}
		switch code {
		case CodeCommentNotAddressable:
			d["comment_ids"], d["why"] = ids, v.Rule
		case CodePatchInvalid:
			d["field"], d["rule"] = v.Field, v.Rule
			if v.OpIndex != nil {
				d["op_index"] = *v.OpIndex
			}
		case CodeRecipeUnknown:
			d["track_id"] = baseTrack
		}
		break
	}
	return perr(http.StatusUnprocessableEntity, code, "the patch cannot be proposed: "+ce.Error(), d)
}

func toOps(in []OpJSON) []Op {
	out := make([]Op, len(in))
	for i, o := range in {
		op := Op{Op: o.Op, SectionIdx: -1, LineIdx: -1}
		if o.SectionIdx != nil {
			op.SectionIdx = *o.SectionIdx
		}
		if o.LineIdx != nil {
			op.LineIdx = *o.LineIdx
		}
		if o.Text != nil {
			op.Text = *o.Text
		}
		if o.Style != nil {
			op.Style = *o.Style
		}
		out[i] = op
	}
	return out
}

// Propose compiles and stores a Song PR (C253.1, C253.2). Reads and the insert run in one write transaction, so the comments the
// compilation saw are the comments the patch is stored against. Spend-free.
func (s *Service) Propose(ctx context.Context, p auth.Principal, in ProposeInput) (SongPR, error) {
	if err := s.ready(); err != nil {
		return SongPR{}, err
	}
	switch p.Kind {
	case auth.KindHuman, auth.KindDevice, auth.KindAgent:
	default:
		return SongPR{}, perr(http.StatusForbidden, "FORBIDDEN", "this principal cannot propose a patch", nil)
	}
	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return SongPR{}, err
	}
	defer func() { _ = tx.Rollback() }()

	base, err := loadBase(ctx, tx, in.BaseTrackID)
	if err != nil {
		return SongPR{}, err
	}
	ci := CompileInput{Ops: toOps(in.Ops), ExtraSections: in.ExtraSections, RecipeKnown: library.RecipeKnownEngine(base.engine)}
	if ci.RecipeKnown {
		if base.lossy {
			return SongPR{}, perr(http.StatusUnprocessableEntity, "LOSSY_MASTER", "the master of this take is a lossy MP3", map[string]any{"track_id": in.BaseTrackID})
		}
		if base.engine != "yue2" {
			return SongPR{}, perr(http.StatusUnprocessableEntity, CodePatchInvalid, "Song PRs patch YuE2 takes only",
				map[string]any{"field": "base_track_id", "rule": "engine_unsupported", "violations": []Violation{{Code: CodePatchInvalid, Rule: "engine_unsupported", Field: "base_track_id"}}})
		}
		parsed, err := yue2.ParseRequest([]byte(base.requestJSON), yue2.ParseOptions{})
		if err != nil {
			return SongPR{}, fmt.Errorf("songpr: parse base recipe: %w", err)
		}
		ci.Base = parsed.Request
	}
	if base.status == "discarded" {
		return SongPR{}, perr(http.StatusUnprocessableEntity, CodePatchInvalid, "the base take is discarded",
			map[string]any{"field": "base_track_id", "rule": "base_discarded", "violations": []Violation{{Code: CodePatchInvalid, Rule: "base_discarded", Field: "base_track_id"}}})
	}
	anc, err := lineageIDs(ctx, tx, in.BaseTrackID)
	if err != nil {
		return SongPR{}, err
	}
	timings := map[string]anchor.Timing{}
	for _, cid := range in.AddressesCommentIDs {
		var track, kind, state, label sql.NullString
		var hidden int
		var at sql.NullFloat64
		err := tx.QueryRowContext(ctx, `SELECT track_id, kind, state, hidden_at IS NOT NULL, at_second, section_label FROM comments WHERE id = ?`, cid).
			Scan(&track, &kind, &state, &hidden, &at, &label)
		a := Addressed{CommentID: cid}
		switch {
		case errors.Is(err, sql.ErrNoRows): // unknown id: not on this lineage
		case err != nil:
			return SongPR{}, fmt.Errorf("songpr: comment: %w", err)
		default:
			a.Kind, a.State, a.Hidden, a.OnLineage = kind.String, state.String, hidden == 1, anc[track.String]
			if a.OnLineage {
				t, ok := timings[track.String]
				if !ok {
					if t, err = library.Timing(ctx, tx, track.String); err != nil {
						return SongPR{}, fmt.Errorf("songpr: timing: %w", err)
					}
					timings[track.String] = t
				}
				var atp *float64
				if at.Valid {
					atp = &at.Float64
				}
				a.Spot = spotOf(t, atp, label)
			}
		}
		ci.Addressed = append(ci.Addressed, a)
	}
	res, err := Compile(ci)
	if err != nil {
		var ce *CompileError
		if errors.As(err, &ce) {
			return SongPR{}, compileProblem(ce, in.BaseTrackID)
		}
		return SongPR{}, err
	}

	id := s.o.NewID()
	now := store.FormatTime(s.o.Now())
	opsJSON, _ := json.Marshal(in.Ops)
	compiled, _ := json.Marshal(res.Patch)
	scope, _ := json.Marshal(res.Scope)
	diff, _ := json.Marshal(res.Diff)
	var title any
	if in.Title != nil {
		title = *in.Title
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO patches(id, base_track_id, base_recipe_hash, operator, ops_json, compiled_json, scope_json, diff_json, title, rationale,
		principal_kind, principal_id, principal_name, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, in.BaseTrackID, base.recipeHash, res.Operator, string(opsJSON), string(compiled), string(scope), string(diff), title, in.Rationale,
		string(p.Kind), p.Key(), p.Name, now); err != nil {
		return SongPR{}, fmt.Errorf("songpr: insert patch: %w", err)
	}
	for _, cid := range in.AddressesCommentIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO patch_comments(patch_id, comment_id) VALUES (?,?)`, id, cid); err != nil {
			return SongPR{}, fmt.Errorf("songpr: insert patch comment: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return SongPR{}, err
	}
	audit.NoteCreated(ctx, id)
	return s.Get(ctx, p, id)
}
