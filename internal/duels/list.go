package duels

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
)

// offsetCursor / parseCursor: opaque base64url of {"o":<offset>}.
func offsetCursor(n int) string {
	b, _ := json.Marshal(struct {
		O int `json:"o"`
	}{n})
	return base64.RawURLEncoding.EncodeToString(b)
}

func parseCursor(c string) (int, error) {
	if c == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	var v struct {
		O int `json:"o"`
	}
	if err != nil || json.Unmarshal(b, &v) != nil || v.O < 0 {
		return 0, invalid("cursor", "malformed cursor")
	}
	return v.O, nil
}

// List pages duels, newest first (C48, D-M2b-4).
func (s *Service) List(ctx context.Context, p auth.Principal, f ListFilter) (Page, error) {
	if s.unconfigured() {
		return Page{}, errors.ErrUnsupported
	}
	off, err := parseCursor(f.Cursor)
	if err != nil {
		return Page{}, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)
	var conds []string
	var args []any
	now := rfc(s.now())
	if len(f.States) > 0 {
		var ors []string
		for _, st := range slices.Compact(slices.Sorted(slices.Values(f.States))) {
			switch st {
			case "decided":
				ors = append(ors, `EXISTS (SELECT 1 FROM duel_verdicts h WHERE h.duel_id = d.id AND h.source = 'human')`)
			case "open":
				ors = append(ors, `(NOT EXISTS (SELECT 1 FROM duel_verdicts h WHERE h.duel_id = d.id AND h.source = 'human') AND d.expires_at > ?)`)
				args = append(args, now)
			case "expired":
				ors = append(ors, `(NOT EXISTS (SELECT 1 FROM duel_verdicts h WHERE h.duel_id = d.id AND h.source = 'human') AND d.expires_at <= ?)`)
				args = append(args, now)
			default:
				return Page{}, invalid("state", "state must be open, decided or expired")
			}
		}
		conds = append(conds, "("+strings.Join(ors, " OR ")+")")
	}
	if f.TrackID != "" {
		conds = append(conds, `(d.slot_a_track_id = ? OR d.slot_b_track_id = ?)`)
		args = append(args, f.TrackID, f.TrackID)
	}
	if f.RootTrackID != "" {
		conds = append(conds, `(ta.root_track_id = ? OR tb.root_track_id = ?)`)
		args = append(args, f.RootTrackID, f.RootTrackID)
	}
	switch f.Mode {
	case "", "any":
	case "blind":
		conds = append(conds, `d.blind = 1`)
	case "sighted":
		conds = append(conds, `d.blind = 0`)
	default:
		return Page{}, invalid("mode", "mode must be any, blind or sighted")
	}
	from := ` FROM duels d JOIN tracks ta ON ta.id = d.slot_a_track_id JOIN tracks tb ON tb.id = d.slot_b_track_id`
	if len(conds) > 0 {
		from += ` WHERE ` + strings.Join(conds, " AND ")
	}
	var total int
	if err := s.o.Store.R.QueryRowContext(ctx, `SELECT count(*)`+from, args...).Scan(&total); err != nil {
		return Page{}, fmt.Errorf("duels: list count: %w", err)
	}
	page := Page{Items: []DuelSummary{}, Total: total}
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT `+duelCols+from+` ORDER BY d.created_at DESC, d.id DESC LIMIT ? OFFSET ?`,
		append(slices.Clone(args), limit, off)...)
	if err != nil {
		return Page{}, fmt.Errorf("duels: list: %w", err)
	}
	var ds []duelRow
	for rows.Next() {
		d, err := scanDuel(rows)
		if err != nil {
			_ = rows.Close()
			return Page{}, fmt.Errorf("duels: list: %w", err)
		}
		ds = append(ds, d)
	}
	if err := rows.Close(); err != nil {
		return Page{}, err
	}
	if len(ds) == 0 {
		return page, nil
	}
	ids := make([]string, len(ds))
	var tids []string
	for i, d := range ds {
		ids[i] = d.ID
		tids = append(tids, d.SlotA, d.SlotB)
	}
	vs, err := s.loadVerdicts(ctx, ids...)
	if err != nil {
		return Page{}, err
	}
	refs, err := s.o.Library.TrackRefs(ctx, tids)
	if err != nil {
		return Page{}, err
	}
	for _, d := range ds {
		page.Items = append(page.Items, s.summary(p, d, vs[d.ID], refs))
	}
	if off+len(ds) < total {
		c := offsetCursor(off + len(ds))
		page.NextCursor = &c
	}
	return page, nil
}

func (s *Service) summary(p auth.Principal, d duelRow, vs []verdictRow, refs map[string]library.TrackRef) DuelSummary {
	st := s.standingOf(p, d, vs)
	out := DuelSummary{ID: d.ID, State: st.state, Blind: d.Blind, Revealed: st.revealed, CreatedAt: rfc(d.CreatedAt), ExpiresAt: rfc(d.ExpireAt),
		Window: d.window(), LoudnessMatched: d.loudnessMatched()}
	if st.revealed {
		if r, ok := refs[d.SlotA]; ok {
			out.Slots.A.Track = brief(r)
		}
		if r, ok := refs[d.SlotB]; ok {
			out.Slots.B.Track = brief(r)
		}
		if st.human != nil {
			out.HumanVerdict = &HumanVerdictBriefOrNull{Verdict: st.human.Verdict, WinnerTrackID: nullStr(st.human.Winner)}
		}
	}
	mine := st.mineVerdict()
	if mine != nil {
		v := mine.Verdict
		out.MyVerdict = &v
	}
	if st.judged {
		out.CriticVerdicts = len(st.critics)
	}
	return out
}
