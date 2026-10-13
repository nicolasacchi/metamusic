package export

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// ErrEmpty is returned by Build when the selection matches no row (EXPORT_EMPTY).
var ErrEmpty = errors.New("export: nothing to export")

// ErrBadSelection is returned by Build for a Selection it cannot interpret.
var ErrBadSelection = errors.New("export: bad selection")

// Build assembles the songs.jsonl of a selection (C33): EXPORT_EMPTY (ErrEmpty) when no row matches.
func Build(ctx context.Context, src Source, sel Selection) (Result, error) {
	samples, err := src.StyleSamples(ctx)
	if err != nil {
		return Result{}, err
	}
	style, _ := DetectStyle(samples)

	var (
		rows     []cand
		warnings []Note
		always   = true // the unique-id rule applies
	)
	switch sel.Kind {
	case "source":
		rows, warnings, err = sourceCands(ctx, src, sel.SourcePath)
		always = sel.ForBatch
	case "tracks":
		rows, err = trackCands(ctx, src, sel.Tracks)
	case "drafts":
		rows, err = draftCands(ctx, src, sel.Drafts)
	default:
		return Result{}, fmt.Errorf("%w: kind %q", ErrBadSelection, sel.Kind)
	}
	if err != nil {
		return Result{}, err
	}
	if len(rows) == 0 {
		return Result{}, ErrEmpty
	}

	// ForBatch means "yue2 batch must accept this": raw lines it would reject are regenerated too.
	if sel.Kind == "source" && sel.ForBatch {
		for i := range rows {
			c := &rows[i]
			if c.raw != nil && !rawOK(c) {
				c.invalidRaw = true
			}
		}
	}
	renamed := map[int]bool{}
	if always {
		renamed = applyUniqueIDs(rows)
	}

	res := Result{Style: style, Warnings: warnings}
	var invalid []string
	abcOwner := map[string][]byte{}
	for i := range rows {
		c := &rows[i]
		row := Row{RecipeHash: c.hash, RequestID: c.id}
		useRaw := c.raw != nil && !c.invalidRaw && !renamed[i]
		if useRaw && c.abcPath != "" && c.abc != nil {
			// the path may be taken by a different file already (two recipes, one abc_path)
			if have, ok := abcOwner[c.abcPath]; ok && !bytes.Equal(have, c.abc) && sel.Kind != "source" {
				useRaw, c.invalidRaw = false, true
			}
		}
		if useRaw {
			row.Bytes, row.Raw = c.raw, true
			if c.abcPath != "" {
				row.ABCPath = c.abcPath
				if c.abc == nil {
					res.Warnings = append(res.Warnings, Note{Code: "abc_missing", Message: "the abc_path target of a row is not recorded",
						Details: map[string]any{"request_id": c.id, "abc_path": c.abcPath}})
				} else if _, dup := abcOwner[c.abcPath]; !dup {
					abcOwner[c.abcPath] = c.abc
					if res.ABCFiles == nil {
						res.ABCFiles = map[string][]byte{}
					}
					res.ABCFiles[c.abcPath] = c.abc
				}
			}
		} else {
			req, ok := c.request()
			if !ok {
				// nothing to regenerate from (an unparsable source line): keep the bytes
				row.Bytes, row.Raw = c.raw, true
			} else {
				if renamed[i] {
					row.RenamedFrom = req.ID
					req.ID = c.newID
					row.RequestID = c.newID
				}
				row.Bytes = EncodeRequest(req, style)
				if c.hadRaw && c.invalidRaw && !renamed[i] {
					invalid = append(invalid, req.ID)
				}
			}
		}
		res.Rows = append(res.Rows, row)
	}
	if len(invalid) > 0 {
		ids := invalid
		if len(ids) > 20 {
			ids = ids[:20]
		}
		res.Warnings = append(res.Warnings, Note{Code: "raw_not_batch_valid", Message: "the recorded source line would not pass yue2 batch; the row was regenerated",
			Details: map[string]any{"count": len(invalid), "request_ids": ids}})
	}

	var buf bytes.Buffer
	for _, r := range res.Rows {
		buf.Write(r.Bytes)
		buf.WriteByte('\n')
	}
	res.JSONL = buf.Bytes()
	res.Validation = yue2.ValidateBatch(res.JSONL, abcFS(res.ABCFiles))
	sort.SliceStable(res.Warnings, func(a, b int) bool { return res.Warnings[a].Code < res.Warnings[b].Code })
	return res, nil
}

// abcFS serves the ABC files of a result to ValidateBatch.
type abcFS map[string][]byte

func (m abcFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

func (m abcFS) ReadFile(name string) ([]byte, error) {
	if b, ok := m[name]; ok {
		return b, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// topLevel reads the top-level keys of one JSON object line.
func topLevel(raw []byte) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(raw), &m); err != nil {
		return nil
	}
	return m
}

// abcPathOf returns the abc_path value of a raw line ("" when absent or null).
func abcPathOf(raw []byte) string {
	var s string
	if v, ok := topLevel(raw)["abc_path"]; ok && json.Unmarshal(v, &s) == nil {
		return s
	}
	return ""
}

// idOf returns the id value of a raw line ("" when absent).
func idOf(raw []byte) string {
	var s string
	if v, ok := topLevel(raw)["id"]; ok && json.Unmarshal(v, &s) == nil {
		return s
	}
	return ""
}
