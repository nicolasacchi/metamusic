package exports

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/export"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
)

// Error is a client-visible failure with a stable code.
type Error struct {
	Status  int
	Code    string
	Msg     string
	Details map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

// inlineLimit is the largest JSONL (bytes) returned inline as Export.JSONL.Content.
const inlineLimit = 60000

// zipEpoch stamps the entries of a JSONL bundle, which has no natural modification time.
var zipEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

var mimeByExt = map[string]string{"jsonl": "application/x-ndjson", "zip": "application/zip"}

// cleanFilename reduces name to [a-z0-9._-] (at most 120 bytes) and makes sure it ends in .ext.
func cleanFilename(name, def, ext string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), ".-")
	out = strings.TrimSuffix(out, "."+ext)
	out = strings.Trim(out, ".-")
	if out == "" {
		return def
	}
	if len(out)+len(ext)+1 > 120 {
		out = strings.Trim(out[:120-len(ext)-1], ".-")
	}
	return out + "." + ext
}

// stored is a finished export file.
type stored struct {
	id, sha, rel string
	bytes        int64
}

// writeFile builds a file in incoming/export-<id>/, hashes it, makes it read-only and claims it as
// derived/exports/<aa>/<sha>.<ext>. An existing file with that name has identical content and is reused.
func (s *Service) writeFile(id, ext string, write func(w io.Writer) error) (stored, error) {
	data := s.o.Data
	if data == nil {
		return stored{}, errors.New("exports: no data root")
	}
	work := "incoming/export-" + id
	if err := data.MkdirAll(work, 0o755); err != nil {
		return stored{}, fmt.Errorf("exports: work dir: %w", err)
	}
	defer func() { _ = data.RemoveAll(work) }()
	tmp := work + "/out." + ext
	f, err := data.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return stored{}, fmt.Errorf("exports: create file: %w", err)
	}
	h := sha256.New()
	cw := &countWriter{w: io.MultiWriter(f, h)}
	if err := write(cw); err != nil {
		_ = f.Close()
		return stored{}, err
	}
	if err := f.Close(); err != nil {
		return stored{}, fmt.Errorf("exports: close file: %w", err)
	}
	if cw.n == 0 {
		return stored{}, errors.New("exports: empty file")
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if err := data.Chmod(tmp, 0o444); err != nil {
		return stored{}, fmt.Errorf("exports: chmod: %w", err)
	}
	rel := "derived/exports/" + sum[:2] + "/" + sum + "." + ext
	if err := data.MkdirAll(path.Dir(rel), 0o755); err != nil {
		return stored{}, fmt.Errorf("exports: derived dir: %w", err)
	}
	if err := data.Link(tmp, rel); err != nil && !errors.Is(err, fs.ErrExist) {
		return stored{}, fmt.Errorf("exports: claim file: %w", err)
	}
	return stored{id: id, sha: sum, rel: rel, bytes: cw.n}, nil
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// zipStored adds a stored (uncompressed) entry with a fixed timestamp.
func zipStored(zw *zip.Writer, name string, mod time.Time) (io.Writer, error) {
	return zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: mod.UTC()})
}

func (s *Service) link(p auth.Principal, sha, ext string) Link {
	ttl, bucket := serve.TTLFor(p)
	l := s.o.Signer.Sign(sha, ext, ttl, bucket)
	return Link{URL: l.URL, ExpiresAt: l.Expires.UTC().Format(time.RFC3339)}
}

// record inserts the exports row and returns the view with a fresh link.
func (s *Service) record(ctx context.Context, p auth.Principal, f stored, kind, filename string, params any, jsonl *JSONLInfo, zipInfo *ZIPInfo) (Export, error) {
	ext := path.Ext(f.rel)[1:]
	sum, _ := json.Marshal(summary{JSONL: jsonl, ZIP: zipInfo})
	pj, _ := json.Marshal(params)
	now := s.o.Now().UTC().Format(time.RFC3339)
	if _, err := s.o.Store.W.ExecContext(ctx, `INSERT INTO exports(id, kind, params_json, sha256, bytes, path, filename, summary_json, principal_id, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, f.id, kind, string(pj), f.sha, f.bytes, f.rel, filename, string(sum), p.Key(), now); err != nil {
		return Export{}, fmt.Errorf("exports: record: %w", err)
	}
	return Export{ID: f.id, Kind: kind, Filename: filename, SHA256: f.sha, Bytes: f.bytes, CreatedAt: now,
		Download: s.link(p, f.sha, ext), JSONL: jsonl, ZIP: zipInfo}, nil
}

type summary struct {
	JSONL *JSONLInfo `json:"jsonl,omitempty"`
	ZIP   *ZIPInfo   `json:"zip,omitempty"`
}

func problemOf(err error) error {
	switch {
	case errors.Is(err, export.ErrEmpty):
		return &Error{Status: http.StatusUnprocessableEntity, Code: "EXPORT_EMPTY", Msg: "the selection matches nothing to export"}
	case errors.Is(err, export.ErrUnknownSource):
		return &Error{Status: http.StatusUnprocessableEntity, Code: "UNKNOWN_SOURCE", Msg: "no imported JSONL file has that path",
			Details: map[string]any{"field": "source_path"}}
	case errors.Is(err, export.ErrBadSelection):
		return &Error{Status: http.StatusUnprocessableEntity, Code: "VALIDATION_FAILED", Msg: err.Error(), Details: map[string]any{"field": "selection"}}
	}
	return err
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// jsonlInfo describes a Build result.
func jsonlInfo(res export.Result) *JSONLInfo {
	info := &JSONLInfo{
		Rows: len(res.Rows), Renamed: []Rename{}, ABCFiles: []ABCFile{}, Warnings: res.Warnings,
		Style:      StyleInfo{KeyOrder: nonNilStrings(res.Style.KeyOrder), ItemSep: res.Style.ItemSep, KeySep: res.Style.KeySep, EnsureASCII: res.Style.EnsureASCII},
		Validation: Validation{OK: res.Validation.OK, Fatal: res.Validation.Fatal, Issues: []ExportIssue{}},
	}
	if info.Warnings == nil {
		info.Warnings = []export.Note{}
	}
	for _, r := range res.Rows {
		if r.Raw {
			info.RawRows++
		} else {
			info.GeneratedRows++
		}
		if r.RenamedFrom != "" {
			info.Renamed = append(info.Renamed, Rename{From: r.RenamedFrom, To: r.RequestID})
		}
	}
	for _, is := range res.Validation.Issues {
		info.Validation.Issues = append(info.Validation.Issues, ExportIssue{Code: is.Code, Field: is.Field, Line: is.Line, Fatal: is.Fatal})
	}
	paths := make([]string, 0, len(res.ABCFiles))
	for p := range res.ABCFiles {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		sum := sha256.Sum256(res.ABCFiles[p])
		info.ABCFiles = append(info.ABCFiles, ABCFile{Path: p, SHA256: hex.EncodeToString(sum[:])})
	}
	if len(res.JSONL) <= inlineLimit {
		c := string(res.JSONL)
		info.Content = &c
	}
	return info
}

// JSONL exports a selection as songs.jsonl (or a bundle when ABC files are needed).
func (s *Service) JSONL(ctx context.Context, p auth.Principal, sel export.Selection, filename string) (Export, error) {
	res, err := export.Build(ctx, s.o.Source, sel)
	if err != nil {
		return Export{}, problemOf(err)
	}
	info := jsonlInfo(res)
	id := s.o.NewID()
	if len(res.ABCFiles) == 0 {
		name := cleanFilename(filename, "songs.jsonl", "jsonl")
		f, err := s.writeFile(id, "jsonl", func(w io.Writer) error { _, err := w.Write(res.JSONL); return err })
		if err != nil {
			return Export{}, err
		}
		return s.record(ctx, p, f, "songs_jsonl", name, sel, info, nil)
	}
	name := cleanFilename(filename, "songs-bundle.zip", "zip")
	paths := make([]string, 0, len(res.ABCFiles))
	for p := range res.ABCFiles {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	f, err := s.writeFile(id, "zip", func(w io.Writer) error {
		zw := zip.NewWriter(w)
		ew, err := zipStored(zw, "songs.jsonl", zipEpoch)
		if err != nil {
			return err
		}
		if _, err := ew.Write(res.JSONL); err != nil {
			return err
		}
		for _, p := range paths {
			ew, err := zipStored(zw, p, zipEpoch)
			if err != nil {
				return err
			}
			if _, err := ew.Write(res.ABCFiles[p]); err != nil {
				return err
			}
		}
		return zw.Close()
	})
	if err != nil {
		return Export{}, err
	}
	return s.record(ctx, p, f, "songs_bundle", name, sel, info, nil)
}

// Get returns an export with a fresh link; EXPORT_EXPIRED when swept.
func (s *Service) Get(ctx context.Context, p auth.Principal, id string) (Export, error) {
	var (
		e        Export
		rel, sum string
	)
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT kind, filename, sha256, bytes, created_at, path, summary_json FROM exports WHERE id = ?`, id).
		Scan(&e.Kind, &e.Filename, &e.SHA256, &e.Bytes, &e.CreatedAt, &rel, &sum)
	if errors.Is(err, sql.ErrNoRows) {
		return Export{}, &Error{Status: http.StatusNotFound, Code: "NOT_FOUND", Msg: "no such resource"}
	}
	if err != nil {
		return Export{}, fmt.Errorf("exports: get: %w", err)
	}
	e.ID = id
	if s.o.Data != nil {
		if _, err := s.o.Data.Stat(rel); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return Export{}, &Error{Status: http.StatusGone, Code: "EXPORT_EXPIRED", Msg: "the export file was removed; create a new export"}
			}
			return Export{}, fmt.Errorf("exports: stat: %w", err)
		}
	}
	var sm summary
	if err := json.Unmarshal([]byte(sum), &sm); err != nil {
		return Export{}, fmt.Errorf("exports: stored summary: %w", err)
	}
	e.JSONL, e.ZIP = sm.JSONL, sm.ZIP
	e.Download = s.link(p, e.SHA256, path.Ext(rel)[1:])
	return e, nil
}

// Sources lists the JSONL source paths with line and recipe counts.
func (s *Service) Sources(ctx context.Context) ([]ExportSource, error) {
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT source_path, COUNT(DISTINCT line_no), COUNT(DISTINCT recipe_id), MIN(created_at)
		FROM recipe_sources WHERE raw_kind = 'jsonl_line' GROUP BY source_path ORDER BY source_path`)
	if err != nil {
		return nil, fmt.Errorf("exports: sources: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []ExportSource{}
	for rows.Next() {
		x := ExportSource{Kind: "jsonl"}
		if err := rows.Scan(&x.Path, &x.Lines, &x.Recipes, &x.FirstSeenAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// Resolve maps a media address (ext jsonl|zip) to a stored export file.
func (s *Service) Resolve(ctx context.Context, sha, ext string) (serve.Object, error) {
	ct, ok := mimeByExt[ext]
	if !ok {
		return serve.Object{}, serve.ErrNotFound
	}
	var rel, name, created string
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT path, filename, created_at FROM exports WHERE sha256 = ? AND path LIKE ?
		ORDER BY created_at DESC, id DESC LIMIT 1`, sha, "%."+ext).Scan(&rel, &name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return serve.Object{}, serve.ErrNotFound
	}
	if err != nil {
		return serve.Object{}, fmt.Errorf("exports: resolve: %w", err)
	}
	if s.o.Data != nil {
		if _, err := s.o.Data.Stat(rel); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return serve.Object{}, serve.ErrNotFound
			}
			return serve.Object{}, fmt.Errorf("exports: stat: %w", err)
		}
	}
	mt, _ := time.Parse(time.RFC3339, created)
	return serve.Object{RelPath: rel, ContentType: ct, ETag: sha, ModTime: mt, DownloadName: name}, nil
}

// Sweep removes export files whose newest row is older than olderThan. Rows stay.
func (s *Service) Sweep(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := s.o.Now().Add(-olderThan).UTC().Format(time.RFC3339)
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT path FROM exports GROUP BY path HAVING MAX(created_at) < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("exports: sweep: %w", err)
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			_ = rows.Close()
			return 0, err
		}
		paths = append(paths, p)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	n := 0
	for _, p := range paths {
		if !strings.HasPrefix(p, "derived/exports/") || s.o.Data == nil {
			continue
		}
		switch err := s.o.Data.Remove(p); {
		case err == nil:
			n++
		case errors.Is(err, fs.ErrNotExist):
		default:
			return n, fmt.Errorf("exports: sweep %s: %w", p, err)
		}
	}
	return n, nil
}
