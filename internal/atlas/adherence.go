package atlas

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/problem"
)

// AdherenceFacets are the Sound facets get_adherence aggregates by (D-L3-28), in output order. They are song_features columns.
var AdherenceFacets = []string{"family", "vocal", "era", "mood", "language"}

// Adherence defaults and bounds (C141.4).
const (
	DefaultMinN         = 3
	DefaultAdherenceLim = 50
	MaxAdherenceLim     = 100
	within5PctX100      = 500 // the 5 % band in units of 0.01 %
)

// TempoErrorX100 is the requested-versus-got error of C141.4 in units of 0.01 %, for tempos in centi-BPM: the smallest of
// the error against the request, against twice the request and against half the request, each truncated. req must be > 0.
func TempoErrorX100(req, got int) int {
	abs := func(n int) int {
		if n < 0 {
			return -n
		}
		return n
	}
	e1 := abs(got-req) * 10000 / req
	eh := abs(got-2*req) * 10000 / (2 * req)
	ed := abs(2*got-req) * 10000 / req
	return min(e1, eh, ed)
}

// adhRow is one live Song whose subject is a take, as song_features describes it.
type adhRow struct {
	songID, title, takeID string
	req, meas             sql.NullInt64
	octave                bool
	dur, exp              sql.NullFloat64
	facets                [5]sql.NullString // AdherenceFacets order
}

func (r adhRow) hasPair() bool { return r.req.Valid && r.meas.Valid && r.req.Int64 > 0 }

func (r adhRow) hasDuration() bool {
	return r.dur.Valid && r.exp.Valid && r.exp.Float64 > 0
}

// errX100 is the tempo error of a row that has a pair.
func (r adhRow) errX100() int { return TempoErrorX100(int(r.req.Int64), int(r.meas.Int64)) }

// ratioMilli is duration / expected duration in thousandths, rounded half up.
func (r adhRow) ratioMilli() int { return int(math.Floor(r.dur.Float64/r.exp.Float64*1000 + 0.5)) }

func invalid(field, msg string) error {
	return problem.E(422, "VALIDATION_FAILED", msg, map[string]any{"field": field})
}

func invalidCursor() error {
	return problem.E(422, "INVALID_CURSOR", "the cursor is not valid", map[string]any{"field": "cursor"})
}

func encodeAdhCursor(errX100 int, songID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(errX100) + "\x00" + songID))
}

func decodeAdhCursor(c string) (errX100 int, songID string, ok bool, err error) {
	if c == "" {
		return 0, "", false, nil
	}
	raw, derr := base64.RawURLEncoding.DecodeString(c)
	a, b, found := strings.Cut(string(raw), "\x00")
	if derr != nil || !found || b == "" {
		return 0, "", false, invalidCursor()
	}
	n, aerr := strconv.Atoi(a)
	if aerr != nil || n < 0 {
		return 0, "", false, invalidCursor()
	}
	return n, b, true, nil
}

// Adherence answers get_adherence from song_features in one statement (so it is one snapshot): the population is every
// live Song whose subject is a take (its Pick take, or the newest take when it has none). Facet and Value narrow the
// population of every part; MinErrorPct and Octave narrow only the item list. No Muse text is read and none is returned
// (D-L3-16 is not approved): items carry the Song title only.
func (s *Service) Adherence(ctx context.Context, q AdherenceQuery) (Adherence, error) {
	col := -1
	if q.Facet != "" {
		if col = slices.Index(AdherenceFacets, q.Facet); col < 0 {
			return Adherence{}, invalid("facet", "facet must be one of "+strings.Join(AdherenceFacets, ", "))
		}
	}
	if q.Value != "" && col < 0 {
		return Adherence{}, invalid("value", "value needs a facet")
	}
	minN, limit := q.MinN, q.Limit
	if minN <= 0 {
		minN = DefaultMinN
	}
	if limit <= 0 {
		limit = DefaultAdherenceLim
	}
	limit = min(limit, MaxAdherenceLim)
	curErr, curID, haveCur, err := decodeAdhCursor(q.Cursor)
	if err != nil {
		return Adherence{}, err
	}
	rows, err := s.adhRows(ctx, q, col)
	if err != nil {
		return Adherence{}, err
	}
	out := Adherence{DimsAvailable: []string{}, ByFacet: []AdherenceFacet{}, Items: []AdherenceItem{}}
	out.Coverage.Songs = len(rows)
	var errs []int
	var octaves, within int
	var pairs []adhRow
	for _, r := range rows {
		if r.hasPair() {
			pairs = append(pairs, r)
			e := r.errX100()
			errs = append(errs, e)
			if r.octave {
				octaves++
			}
			if e <= within5PctX100 {
				within++
			}
		}
		if r.hasDuration() {
			out.Coverage.WithDuration++
		}
	}
	out.Coverage.WithPair = len(pairs)
	if len(pairs) > 0 {
		out.DimsAvailable = append(out.DimsAvailable, "bpm")
		out.Summary = AdherenceSummary{N: len(pairs), Within5PctShare: share(within, len(pairs)), OctaveShare: share(octaves, len(pairs)),
			MedianErrorPct: float64(medianInt(errs)) / 100}
	}
	if out.Coverage.WithDuration > 0 {
		out.DimsAvailable = append(out.DimsAvailable, "duration")
	}
	out.ByFacet = byFacet(rows, col, q.Value, minN)

	// items: error desc, song id asc, keyset after the cursor
	slices.SortFunc(pairs, func(a, b adhRow) int {
		if ea, eb := a.errX100(), b.errX100(); ea != eb {
			return eb - ea
		}
		return strings.Compare(a.songID, b.songID)
	})
	for _, r := range pairs {
		e := r.errX100()
		if haveCur && (e > curErr || e == curErr && r.songID <= curID) {
			continue
		}
		if q.MinErrorPct != nil && float64(e)/100 < *q.MinErrorPct {
			continue
		}
		if q.Octave != nil && r.octave != *q.Octave {
			continue
		}
		if len(out.Items) == limit {
			last := out.Items[len(out.Items)-1]
			c := encodeAdhCursor(int(math.Round(last.ErrorPct*100)), last.SongID)
			out.NextCursor = &c
			break
		}
		it := AdherenceItem{SongID: r.songID, TakeID: r.takeID, Title: r.title, RequestedBPM: float64(r.req.Int64) / 100,
			MeasuredBPM: float64(r.meas.Int64) / 100, ErrorPct: float64(e) / 100, Octave: r.octave}
		if r.hasDuration() {
			v := float64(r.ratioMilli()) / 1000
			it.DurationRatio = &v
		}
		out.Items = append(out.Items, it)
	}
	return out, nil
}

func (s *Service) adhRows(ctx context.Context, q AdherenceQuery, col int) ([]adhRow, error) {
	query := `SELECT f.song_id, s.title, f.subject_id, f.bpm_requested_x100, f.bpm_measured_x100, f.bpm_octave, f.duration_s, f.duration_expected_s,
		f.family, f.vocal, f.era, f.mood, f.language
		FROM song_features f JOIN songs s ON s.id = f.song_id WHERE s.retired_at IS NULL AND f.subject_kind = 'take'`
	var args []any
	if col >= 0 && q.Value != "" {
		query += ` AND f.` + AdherenceFacets[col] + ` = ?` // the column comes from the AdherenceFacets whitelist
		args = append(args, q.Value)
	}
	rs, err := s.st.R.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("atlas: adherence: %w", err)
	}
	defer rs.Close()
	var out []adhRow
	for rs.Next() {
		var r adhRow
		var oct int
		if err := rs.Scan(&r.songID, &r.title, &r.takeID, &r.req, &r.meas, &oct, &r.dur, &r.exp,
			&r.facets[0], &r.facets[1], &r.facets[2], &r.facets[3], &r.facets[4]); err != nil {
			return nil, fmt.Errorf("atlas: adherence: %w", err)
		}
		r.octave = oct == 1
		out = append(out, r)
	}
	return out, rs.Err()
}

// byFacet aggregates by (facet, primary value). Only facet col is aggregated when col >= 0 (and only value when set); groups
// with fewer than minN Songs are dropped.
func byFacet(rows []adhRow, col int, value string, minN int) []AdherenceFacet {
	type key struct{ facet, value string }
	groups := map[key][]adhRow{}
	for _, r := range rows {
		if !r.hasPair() && !r.hasDuration() {
			continue
		}
		for i, f := range AdherenceFacets {
			if (col >= 0 && i != col) || !r.facets[i].Valid {
				continue
			}
			if value != "" && r.facets[i].String != value {
				continue
			}
			k := key{f, r.facets[i].String}
			groups[k] = append(groups[k], r)
		}
	}
	out := []AdherenceFacet{}
	for k, g := range groups {
		if len(g) < minN {
			continue
		}
		af := AdherenceFacet{Facet: k.facet, Value: k.value, N: len(g)}
		var errs, ratios []int
		var octaves, within int
		var sumReq, sumGot int64
		for _, r := range g {
			if r.hasPair() {
				e := r.errX100()
				errs = append(errs, e)
				if r.octave {
					octaves++
				}
				if e <= within5PctX100 {
					within++
				}
				sumReq += r.req.Int64
				sumGot += r.meas.Int64
			}
			if r.hasDuration() {
				ratios = append(ratios, r.ratioMilli())
			}
		}
		if n := len(errs); n > 0 {
			af.BPM = &AdherenceBPM{N: n, Within5PctShare: share(within, n), MedianErrorPct: float64(medianInt(errs)) / 100, OctaveShare: share(octaves, n),
				MeanRequested: tenth(sumReq, n), MeanGot: tenth(sumGot, n)}
		}
		if n := len(ratios); n > 0 {
			af.Duration = &AdherenceDuration{N: n, MedianRatio: float64(medianInt(ratios)) / 1000}
		}
		out = append(out, af)
	}
	slices.SortFunc(out, func(a, b AdherenceFacet) int {
		if fa, fb := slices.Index(AdherenceFacets, a.Facet), slices.Index(AdherenceFacets, b.Facet); fa != fb {
			return fa - fb
		}
		return strings.Compare(a.Value, b.Value)
	})
	return out
}

// medianInt is the middle value of an odd count and the mean of the two middle values (rounded half up) of an even one.
func medianInt(v []int) int {
	s := slices.Clone(v)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2] + 1) / 2
}

// share is count/n as a 4-decimal ratio.
func share(count, n int) float64 {
	return float64((count*10000*2+n)/(2*n)) / 10000
}

// tenth is the mean of centi-BPM values as BPM with one decimal, rounded half up.
func tenth(sumX100 int64, n int) float64 {
	return float64((sumX100*2+int64(n)*10)/(int64(n)*20)) / 10
}
