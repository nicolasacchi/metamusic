package clean

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
)

// Finding is one result of Verify.
type Finding struct {
	Severity string // "error" or "warning"
	Code     string
	Subject  string // "<track id>/<kind>"
	Detail   string
}

// Verify checks every clean rendition row against its file (C308.8): the file exists, its sha256 equals the row, the
// metadata pass Check (the allow-list, no MM_* key), and for flac_clean the STREAMINFO MD5 equals the master's. A missing
// file is a warning (the next Listener request rebuilds it); a hash mismatch, a tag leak and an audio mismatch are
// errors. With remove set, a file with an error is deleted so it is never served until rebuilt; the row stays (the
// renditions table has no physical delete, and a row whose file is gone is rebuilt by Ensure). Only files under
// derived/ named by a clean row are ever removed: a master is never opened for writing.
func Verify(ctx context.Context, db *sql.DB, data *os.Root, remove bool) ([]Finding, error) {
	rows, err := db.QueryContext(ctx, `SELECT r.track_id, r.kind, r.sha256, r.bytes, r.path, t.audio_md5 FROM renditions r JOIN tracks t ON t.id = r.track_id
		WHERE r.kind IN (?, ?) ORDER BY r.track_id, r.kind`, KindMP3, KindFLAC)
	if err != nil {
		return nil, err
	}
	type item struct {
		id, kind, sha, path, md5 string
		bytes                    int64
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.kind, &it.sha, &it.bytes, &it.path, &it.md5); err != nil {
			_ = rows.Close()
			return nil, err
		}
		items = append(items, it)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var out []Finding
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		subject := it.id + "/" + it.kind
		add := func(sev, code, detail string, bad bool) {
			out = append(out, Finding{Severity: sev, Code: code, Subject: subject, Detail: detail})
			if bad && remove {
				_ = data.Remove(it.path)
			}
		}
		if !strings.HasPrefix(it.path, "derived/") || strings.Contains(it.path, "..") {
			out = append(out, Finding{"error", "rendition_path", subject, "the row names a file outside derived/"})
			continue
		}
		f, err := data.Open(it.path)
		if errors.Is(err, fs.ErrNotExist) {
			out = append(out, Finding{"warning", "rendition_missing", subject, "file missing; the next request rebuilds it"})
			continue
		}
		if err != nil {
			return out, err
		}
		st, serr := f.Stat()
		h := sha256.New()
		var copyErr error
		if serr == nil {
			_, copyErr = io.Copy(h, f)
		}
		switch {
		case serr != nil || copyErr != nil:
			_ = f.Close()
			return out, fmt.Errorf("clean: read %s: %w", subject, errors.Join(serr, copyErr))
		case hex.EncodeToString(h.Sum(nil)) != it.sha || st.Size() != it.bytes:
			_ = f.Close()
			add("error", "rendition_hash_mismatch", "file does not match its row", true)
			continue
		}
		cerr := Check(it.kind, f, st.Size())
		if cerr == nil && it.kind == KindFLAC {
			var info flacmeta.Info
			if info, cerr = flacmeta.Read(f, st.Size()); cerr == nil && hex.EncodeToString(info.StreamInfo.MD5[:]) != it.md5 {
				_ = f.Close()
				add("error", "rendition_audio_mismatch", "STREAMINFO MD5 differs from the master's", true)
				continue
			}
		}
		_ = f.Close()
		if cerr != nil {
			add("error", "rendition_tag_leak", cerr.Error(), true)
		}
	}
	return out, nil
}
