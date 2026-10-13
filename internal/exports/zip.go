package exports

import (
	"archive/zip"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/media/id3"
	"github.com/nicolasacchi/metamusic/internal/playlists"
)

// Caps of a playlist ZIP (C33): the export work dir would double the disk use in incoming/.
const (
	zipMaxTracks = 200
	zipMaxBytes  = 2 << 30
)

type rendition struct {
	path  string
	bytes int64
}

// entryText folds s into a flat, printable ASCII file name part: letters, digits and a few separators
// are kept as they are, accents are folded like yue2.Slug does, anything else (controls, "/", "\")
// becomes "_".
func entryText(s string, limit int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == ' ', r == '.', r == '_', r == '-', r == '(', r == ')', r == ',', r == '\'', r == '&', r == '!', r == '+':
			b.WriteRune(r)
		default:
			if f := yue2.Slug(string(r)); f != "" && r >= 0x80 {
				b.WriteString(f)
			} else {
				b.WriteByte('_')
			}
		}
	}
	out := strings.TrimSpace(b.String())
	if len(out) > limit {
		out = strings.TrimSpace(out[:limit])
	}
	return out
}

// plainText is s with controls replaced by a space (EXTINF titles).
func plainText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return ' '
		}
		return r
	}, s)
}

// skipID3 drops a leading ID3v2 tag from the stream, so the exported file carries exactly one tag.
func skipID3(br *bufio.Reader) error {
	h, err := br.Peek(10)
	if err != nil { // shorter than a tag header: nothing to skip
		return nil //nolint:nilerr
	}
	if string(h[:3]) != "ID3" {
		return nil
	}
	size := int(h[6]&0x7f)<<21 | int(h[7]&0x7f)<<14 | int(h[8]&0x7f)<<7 | int(h[9]&0x7f)
	if h[5]&0x10 != 0 {
		size += 10 // footer
	}
	_, err = br.Discard(10 + size)
	return err
}

// PlaylistZIP exports a playlist as a ZIP of tagged MP3 files.
func (s *Service) PlaylistZIP(ctx context.Context, p auth.Principal, playlistID string) (Export, error) {
	pl, err := s.o.Playlists.Get(ctx, playlistID)
	if err != nil {
		return Export{}, err
	}
	rends, err := s.renditions(ctx, pl)
	if err != nil {
		return Export{}, err
	}
	var tracks int
	var total int64
	for _, it := range pl.Items {
		if r, ok := rends[it.Track.ID]; ok {
			tracks++
			total += r.bytes
		}
	}
	if tracks > zipMaxTracks || total > zipMaxBytes {
		return Export{}, &Error{Status: http.StatusRequestEntityTooLarge, Code: "PAYLOAD_TOO_LARGE", Msg: "the playlist is too large for one ZIP",
			Details: map[string]any{"tracks": tracks, "limit_tracks": zipMaxTracks, "bytes": total, "limit_bytes": int64(zipMaxBytes)}}
	}

	ids := make([]string, 0, len(pl.Items))
	for _, it := range pl.Items {
		ids = append(ids, it.Track.ID)
	}
	var stars map[string]int
	if s.o.Ratings != nil {
		if stars, err = s.o.Ratings.HumanStars(ctx, ids); err != nil {
			return Export{}, err
		}
	}

	type entry struct {
		name  string
		tag   id3.Tag
		rend  rendition
		title string
		secs  int
		track string
	}
	width := max(2, len(strconv.Itoa(len(pl.Items))))
	used := map[string]bool{}
	var entries []entry
	info := &ZIPInfo{Tracks: []ZIPEntry{}, MissingMP3: []string{}}
	missing := map[string]bool{}
	for i, it := range pl.Items {
		t := it.Track
		r, ok := rends[t.ID]
		if ok && s.o.Data != nil {
			if _, err := s.o.Data.Stat(r.path); err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					return Export{}, fmt.Errorf("exports: stat rendition: %w", err)
				}
				ok = false
			}
		}
		if !ok {
			if !missing[t.ID] {
				missing[t.ID] = true
				info.MissingMP3 = append(info.MissingMP3, t.ID)
			}
			continue
		}
		title := entryText(t.Title, 80)
		if title == "" {
			title = "track"
		}
		prefix := fmt.Sprintf("%0*d - ", width, i+1)
		name := prefix + title + ".mp3"
		for n := 2; used[name]; n++ {
			name = prefix + title + " (" + strconv.Itoa(n) + ").mp3"
		}
		used[name] = true
		entries = append(entries, entry{
			name: name, rend: r, title: plainText(t.Title), secs: int(math.Round(t.DurationS)), track: t.ID,
			tag: id3.Tag{Title: t.Title, Artist: "metamusic", Album: pl.Name, Track: fmt.Sprintf("%d/%d", i+1, len(pl.Items)), Stars: stars[t.ID],
				TXXX: [][2]string{{"MM_TAKE_ID", t.ID}, {"MM_REQUEST_ID", t.RequestID}, {"MM_SEED", strconv.FormatInt(t.Seed, 10)}, {"MM_STATUS", t.Status}}},
		})
		info.Tracks = append(info.Tracks, ZIPEntry{TrackID: t.ID, Entry: name})
	}
	if len(entries) == 0 {
		return Export{}, &Error{Status: http.StatusUnprocessableEntity, Code: "EXPORT_EMPTY", Msg: "the playlist has no MP3 files to export"}
	}
	info.Entries = len(entries)

	plName := entryText(pl.Name, 80)
	if plName == "" {
		plName = "playlist"
	}
	if strings.HasPrefix(plName, ".") {
		plName = "_" + plName[1:]
	}
	m3uName := plName + ".m3u8"
	var m3u strings.Builder
	m3u.WriteString("#EXTM3U\n")
	for _, e := range entries {
		m3u.WriteString("#EXTINF:" + strconv.Itoa(e.secs) + "," + e.title + "\n" + e.name + "\n")
	}

	mod, err := time.Parse(time.RFC3339, pl.UpdatedAt)
	if err != nil {
		mod = zipEpoch
	}
	filename := cleanFilename(yue2.Slug(pl.Name), "playlist.zip", "zip")
	f, err := s.writeFile(s.o.NewID(), "zip", func(w io.Writer) error {
		zw := zip.NewWriter(w)
		for _, e := range entries {
			ew, err := zipStored(zw, e.name, mod)
			if err != nil {
				return err
			}
			if _, err := ew.Write(id3.Encode(e.tag)); err != nil {
				return err
			}
			src, err := s.o.Data.Open(e.rend.path)
			if err != nil {
				return fmt.Errorf("exports: open rendition: %w", err)
			}
			br := bufio.NewReader(src)
			if err := skipID3(br); err != nil {
				_ = src.Close()
				return err
			}
			_, err = io.Copy(ew, br)
			_ = src.Close()
			if err != nil {
				return err
			}
		}
		ew, err := zipStored(zw, m3uName, mod)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(ew, m3u.String()); err != nil {
			return err
		}
		return zw.Close()
	})
	if err != nil {
		return Export{}, err
	}
	return s.record(ctx, p, f, "playlist_zip", filename, map[string]any{"playlist_id": pl.ID, "revision": pl.Revision}, nil, info)
}

// renditions returns the mp3_320 rendition rows of the playlist's tracks.
func (s *Service) renditions(ctx context.Context, pl playlists.Playlist) (map[string]rendition, error) {
	ids := make([]string, 0, len(pl.Items))
	for _, it := range pl.Items {
		ids = append(ids, it.Track.ID)
	}
	rows, err := s.o.Store.R.QueryContext(ctx, `SELECT track_id, path, bytes FROM renditions WHERE kind = 'mp3_320'
		AND track_id IN (SELECT value FROM json_each(?))`, jsonArray(ids))
	if err != nil {
		return nil, fmt.Errorf("exports: renditions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]rendition{}
	for rows.Next() {
		var id string
		var r rendition
		if err := rows.Scan(&id, &r.path, &r.bytes); err != nil {
			return nil, err
		}
		out[id] = r
	}
	return out, rows.Err()
}
