package playlists

import (
	"context"
	"encoding/json"
	"fmt"
)

// OfflineFlac is the lossless master of a track.
type OfflineFlac struct {
	SHA256        string
	Bytes         int64
	SampleRate    int
	BitsPerSample int
}

// OfflineFile is an MP3 file (a rendition, or a lossy track's master).
type OfflineFile struct {
	SHA256 string
	Bytes  int64
}

// OfflineFact is what a device needs to plan the download of one track (C111).
type OfflineFact struct {
	Title, Album, Status string
	DurationS            float64
	Lossy                bool
	Flac                 *OfflineFlac // nil for a lossy reference track
	MP3                  *OfflineFile // the mp3_320 rendition, a lossy track's master, or nil
}

// OfflineFacts reads the media facts of the given tracks; unknown ids are absent from the result.
func (s *Service) OfflineFacts(ctx context.Context, trackIDs []string) (map[string]OfflineFact, error) {
	out := map[string]OfflineFact{}
	if len(trackIDs) == 0 {
		return out, nil
	}
	list, err := json.Marshal(trackIDs)
	if err != nil {
		return nil, err
	}
	rows, err := s.st.R.QueryContext(ctx, `SELECT t.id, t.title, COALESCE(r.title, t.title), t.status, t.duration_s, t.lossy,
		t.master_sha256, t.master_bytes, t.sample_rate, t.bits_per_sample, m.sha256, m.bytes
		FROM tracks t LEFT JOIN tracks r ON r.id = t.root_track_id
		LEFT JOIN renditions m ON m.track_id = t.id AND m.kind = 'mp3_320'
		WHERE t.id IN (SELECT value FROM json_each(?))`, string(list))
	if err != nil {
		return nil, fmt.Errorf("playlists: offline facts: %w", err)
	}
	for rows.Next() {
		var (
			id, sha string
			f       OfflineFact
			bytes   int64
			rate    int
			bits    int
			mp3Sha  *string
			mp3Size *int64
		)
		if err := rows.Scan(&id, &f.Title, &f.Album, &f.Status, &f.DurationS, &f.Lossy, &sha, &bytes, &rate, &bits, &mp3Sha, &mp3Size); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("playlists: offline facts: %w", err)
		}
		switch {
		case f.Lossy: // the master is the MP3 (D-M4a-25); no lossless file exists
			f.MP3 = &OfflineFile{SHA256: sha, Bytes: bytes}
		default:
			f.Flac = &OfflineFlac{SHA256: sha, Bytes: bytes, SampleRate: rate, BitsPerSample: bits}
			if mp3Sha != nil && mp3Size != nil {
				f.MP3 = &OfflineFile{SHA256: *mp3Sha, Bytes: *mp3Size}
			}
		}
		out[id] = f
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("playlists: offline facts: %w", err)
	}
	_ = rows.Close()
	return out, nil
}
