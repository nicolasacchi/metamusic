package subsonictest

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// PlaceholderSet resolves the {{song:N}}, {{album:N}}, {{mid}}, {{bytes}}, {{user}}, {{text}} and {{query}}
// placeholders of a client fixture deterministically. Besides the placeholder keys it holds "master:<track id>"
// (the master's path relative to the data directory) for every song; the caller may add "user" (the client's
// username) and "data_dir" (the rig's data directory, needed by bytes_equal_master_from).
type PlaceholderSet map[string]string

// libraryVisible is the folder rule of the Subsonic catalog without a musicFolderId (D-M4a-10): final, to_relisten
// and draft tracks.
const libraryVisible = `t.status IN ('final','to_relisten','draft')`

// NewPlaceholders resolves the placeholders against the library in st: songs are the visible tracks in created_at, id
// order and albums their lineages in the root's created_at, id order; N counts from 1. {{bytes}} is the master size
// of song 1 and {{mid}} half of it; {{query}} is the last word of song 1's title (an FTS term) and {{text}} a fixed
// note. {{user}} is "" until the caller sets it.
func NewPlaceholders(tb testing.TB, st *store.Store) PlaceholderSet {
	tb.Helper()
	ctx := context.Background()
	ps := PlaceholderSet{"text": "synthetic note", "user": ""}
	rows, err := st.R.QueryContext(ctx, `SELECT t.id, t.master_path, t.master_bytes, t.title FROM tracks t WHERE `+libraryVisible+` ORDER BY t.created_at, t.id`)
	if err != nil {
		tb.Fatalf("subsonictest: songs: %v", err)
	}
	n := 0
	for rows.Next() {
		var id, path, title string
		var size int64
		if err := rows.Scan(&id, &path, &size, &title); err != nil {
			_ = rows.Close()
			tb.Fatalf("subsonictest: scan song: %v", err)
		}
		n++
		ps[fmt.Sprintf("song:%d", n)] = id
		ps["master:"+id] = path
		if n == 1 {
			ps["bytes"] = strconv.FormatInt(size, 10)
			ps["mid"] = strconv.FormatInt(size/2, 10)
			if f := strings.Fields(title); len(f) > 0 {
				ps["query"] = f[len(f)-1]
			}
		}
	}
	if err := rows.Err(); err != nil {
		tb.Fatalf("subsonictest: songs: %v", err)
	}
	_ = rows.Close()
	arows, err := st.R.QueryContext(ctx, `SELECT DISTINCT t.root_track_id FROM tracks t JOIN tracks rt ON rt.id = t.root_track_id
		WHERE `+libraryVisible+` ORDER BY rt.created_at, rt.id`)
	if err != nil {
		tb.Fatalf("subsonictest: albums: %v", err)
	}
	defer func() { _ = arows.Close() }()
	for i := 1; arows.Next(); i++ {
		var root string
		if err := arows.Scan(&root); err != nil {
			tb.Fatalf("subsonictest: scan album: %v", err)
		}
		ps[fmt.Sprintf("album:%d", i)] = "al-" + root
	}
	if err := arows.Err(); err != nil {
		tb.Fatalf("subsonictest: albums: %v", err)
	}
	return ps
}

// expand replaces every {{name}} of s; an unknown placeholder is an error.
func (ps PlaceholderSet) expand(s string) (string, error) {
	var b strings.Builder
	for {
		i := strings.Index(s, "{{")
		if i < 0 {
			b.WriteString(s)
			return b.String(), nil
		}
		j := strings.Index(s[i:], "}}")
		if j < 0 {
			return "", fmt.Errorf("unterminated placeholder in %q", s)
		}
		name := s[i+2 : i+j]
		v, ok := ps[name]
		if !ok || strings.HasPrefix(name, "master:") {
			return "", fmt.Errorf("unknown placeholder {{%s}}", name)
		}
		b.WriteString(s[:i])
		b.WriteString(v)
		s = s[i+j+2:]
	}
}
