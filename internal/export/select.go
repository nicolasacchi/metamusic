package export

import (
	"context"
	"os"
	"sort"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// cand is one row of a selection before the unique-id rule and the raw-or-regenerate choice.
type cand struct {
	raw        []byte        // the recorded source line, nil when there is none
	hadRaw     bool          // a recorded source line existed
	invalidRaw bool          // the raw does not pass yue2 batch (or conflicts) and is not emitted verbatim
	req        *yue2.Request // canonical request, nil for source rows until parsed
	id         string
	hash       string
	abcPath    string
	abc        []byte
	newID      string // set by applyUniqueIDs
}

// request returns the request to regenerate this row from.
func (c *cand) request() (yue2.Request, bool) {
	if c.req != nil {
		return *c.req, true
	}
	if c.raw == nil {
		return yue2.Request{}, false
	}
	p, err := yue2.ParseRequest(c.raw, yue2.ParseOptions{AllowTitle: true, ResolveABCPath: func(string) ([]byte, error) {
		if c.abc == nil {
			return nil, os.ErrNotExist
		}
		return c.abc, nil
	}})
	if err != nil {
		return yue2.Request{}, false
	}
	return p.Request, true
}

// rawOK reports whether the raw bytes pass the yue2 batch protocol check.
func rawOK(c *cand) bool {
	return len(yue2.CheckRequest(c.raw, yue2.ModeYuE2, func(string) ([]byte, error) {
		if c.abc == nil {
			return nil, os.ErrNotExist
		}
		return c.abc, nil
	})) == 0
}

// sourceCands takes, per line number, the row of the newest import run.
func sourceCands(ctx context.Context, src Source, path string) ([]cand, []Note, error) {
	all, err := src.JSONLSourceRows(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	// all is ordered by (line_no, import_run_id): the last row of a line_no group wins.
	var (
		out        []cand
		superseded []int
	)
	for i, r := range all {
		if i+1 < len(all) && all[i+1].LineNo == r.LineNo {
			superseded = append(superseded, r.LineNo)
			continue
		}
		c := cand{raw: r.Raw, hadRaw: true, id: idOf(r.Raw), abcPath: abcPathOf(r.Raw)}
		if r.ABC != "" {
			c.abc = []byte(r.ABC)
		}
		out = append(out, c)
	}
	var notes []Note
	if len(superseded) > 0 {
		superseded = dedupInts(superseded)
		notes = append(notes, Note{Code: "source_line_superseded", Message: "a line changed between imports; the newest version is exported",
			Details: map[string]any{"lines": superseded}})
	}
	return out, notes, nil
}

func dedupInts(in []int) []int {
	sort.Ints(in)
	out := in[:0]
	for i, v := range in {
		if i == 0 || v != in[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// canonicalSource is the earliest jsonl_line occurrence of a recipe (ties: path, line).
func canonicalSource(srcs []RawSource) (RawSource, bool) {
	if len(srcs) == 0 {
		return RawSource{}, false
	}
	best := srcs[0]
	for _, s := range srcs[1:] {
		switch {
		case s.ImportRunID != best.ImportRunID:
			if s.ImportRunID < best.ImportRunID {
				best = s
			}
		case s.Path != best.Path:
			if s.Path < best.Path {
				best = s
			}
		case s.LineNo < best.LineNo:
			best = s
		}
	}
	return best, true
}

func trackCands(ctx context.Context, src Source, f TrackFilter) ([]cand, error) {
	recs, err := src.SelectRecipes(ctx, f)
	if err != nil {
		return nil, err
	}
	type keyed struct {
		c    cand
		src  *RawSource
		when string
		hash string
	}
	ks := make([]keyed, 0, len(recs))
	for _, r := range recs {
		req := r.Request
		c := cand{req: &req, id: req.ID, hash: r.Hash}
		k := keyed{when: r.FirstRecordedAt, hash: r.Hash}
		if s, ok := canonicalSource(r.JSONLSources); ok {
			c.raw, c.hadRaw = s.Raw, true
			c.abcPath = abcPathOf(s.Raw)
			if req.ABC != nil {
				c.abc = []byte(*req.ABC)
			}
			c.invalidRaw = !rawOK(&c)
			k.src = &s
		}
		k.c = c
		ks = append(ks, k)
	}
	sort.SliceStable(ks, func(a, b int) bool {
		x, y := ks[a], ks[b]
		if (x.src != nil) != (y.src != nil) {
			return x.src != nil
		}
		if x.src != nil {
			if x.src.Path != y.src.Path {
				return x.src.Path < y.src.Path
			}
			if x.src.LineNo != y.src.LineNo {
				return x.src.LineNo < y.src.LineNo
			}
			return x.hash < y.hash
		}
		if x.when != y.when {
			return x.when < y.when
		}
		return x.hash < y.hash
	})
	out := make([]cand, len(ks))
	for i, k := range ks {
		out[i] = k.c
	}
	return out, nil
}

func draftCands(ctx context.Context, src Source, f DraftFilter) ([]cand, error) {
	ds, err := src.SelectDrafts(ctx, f)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(ds, func(a, b int) bool {
		if ds[a].GroupKey != ds[b].GroupKey {
			return ds[a].GroupKey < ds[b].GroupKey
		}
		if ds[a].RequestID != ds[b].RequestID {
			return ds[a].RequestID < ds[b].RequestID
		}
		return ds[a].DraftID < ds[b].DraftID
	})
	out := make([]cand, len(ds))
	for i, d := range ds {
		req := d.Request
		out[i] = cand{req: &req, id: req.ID}
	}
	return out, nil
}
