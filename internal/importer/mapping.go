package importer

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// MapRow is one parsed row of the operator mapping file (C16.6).
type MapRow struct {
	Line      int
	RequestID string // "" when the row only sets title/parent; RefTarget for "import as reference track"
	Seed      *int64
	Title     string
	Parent    string // "", "-", a request id, an inbox path or sha256:<hex>
	Skip      bool   // request_id "-": do not import this audio
}

// RefTarget is the request_id cell that sends an audio file to the reference library (C61).
const RefTarget = "reference"

// Mapping is the parsed mapping file. Rows are keyed by clean inbox path, by
// content sha256 (hex) or, for varianti drafts, by request id.
type Mapping struct {
	ByPath map[string]*MapRow
	BySHA  map[string]*MapRow
	Drafts map[string]*MapRow
	Errors []Issue
}

var shaKeyRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// cleanInboxPath reports whether p is a clean, relative, UTF-8 inbox path.
func cleanInboxPath(p string) bool {
	if p == "" || p == "." || !utf8.ValidString(p) || strings.ContainsRune(p, '\\') || strings.ContainsRune(p, 0) {
		return false
	}
	if path.IsAbs(p) || path.Clean(p) != p {
		return false
	}
	for _, el := range strings.Split(p, "/") {
		if el == ".." {
			return false
		}
	}
	return true
}

// ParseMapping reads the mapping CSV. fsys is the inbox view used to confirm
// that path rows name a regular file; name labels errors. Row problems are
// returned in Mapping.Errors (code bad_mapping_row), never as an error, and
// their messages never contain row values.
func ParseMapping(r io.Reader, fsys fs.FS, name string) (*Mapping, error) {
	data, err := io.ReadAll(io.LimitReader(r, 16<<20))
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	m := &Mapping{ByPath: map[string]*MapRow{}, BySHA: map[string]*MapRow{}, Drafts: map[string]*MapRow{}}
	bad := func(line int, format string, a ...any) {
		m.Errors = append(m.Errors, Issue{Path: name, Line: line, Code: "bad_mapping_row", Message: fmt.Sprintf(format, a...)})
	}
	cr := csv.NewReader(bytes.NewReader(data))
	cr.Comment = '#'
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	first := true
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line, _ := cr.FieldPos(0)
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				bad(pe.StartLine, "malformed CSV row")
				continue
			}
			return nil, err
		}
		if first {
			first = false
			if strings.EqualFold(strings.TrimSpace(rec[0]), "audio") {
				continue
			}
		}
		if len(rec) > 6 {
			bad(line, "too many columns")
			continue
		}
		for len(rec) < 5 {
			rec = append(rec, "")
		}
		row := &MapRow{Line: line, RequestID: rec[1], Title: rec[3], Parent: rec[4]}
		if row.RequestID == "-" {
			row.Skip, row.RequestID = true, ""
		}
		if rec[2] != "" {
			n, perr := strconv.ParseInt(rec[2], 10, 64)
			if perr != nil || n < 0 || n > yue2.MaxSeed {
				bad(line, "seed is not an integer in range")
				continue
			}
			row.Seed = &n
		}
		if row.RequestID == RefTarget && rec[2] != "" {
			bad(line, "reference rows leave seed empty")
			continue
		}
		audio := rec[0]
		switch {
		case strings.HasPrefix(audio, "draft:"):
			id := strings.TrimPrefix(audio, "draft:")
			if id == "" || !utf8.ValidString(id) {
				bad(line, "draft row needs a request id")
				continue
			}
			if rec[1] != "" || rec[2] != "" || rec[3] != "" {
				bad(line, "draft rows leave request_id, seed and title empty")
				continue
			}
			if _, dup := m.Drafts[id]; dup {
				bad(line, "duplicate key")
				continue
			}
			m.Drafts[id] = row
		case strings.HasPrefix(audio, "sha256:"):
			h := strings.TrimPrefix(audio, "sha256:")
			if !shaKeyRe.MatchString(h) {
				bad(line, "sha256 key is not 64 lowercase hex digits")
				continue
			}
			if _, dup := m.BySHA[h]; dup {
				bad(line, "duplicate key")
				continue
			}
			m.BySHA[h] = row
		default:
			if !cleanInboxPath(audio) {
				bad(line, "audio path is not a clean relative inbox path")
				continue
			}
			fi, lerr := fs.Lstat(fsys, audio)
			if lerr != nil || !fi.Mode().IsRegular() {
				bad(line, "audio path is not a regular file in the inbox")
				continue
			}
			if _, dup := m.ByPath[audio]; dup {
				bad(line, "duplicate key")
				continue
			}
			m.ByPath[audio] = row
		}
	}
	return m, nil
}
