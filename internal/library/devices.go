package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

var deviceIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// DeviceProfileIn is the writable part of a device profile.
type DeviceProfileIn struct {
	DeviceID      string
	Label         string
	UserAgent     string
	Quality       string // lossless | high | auto
	QualitySource string // default | user | probe
	ProbeResult   string // none | ok | failed | inconclusive
	FlacCanPlay   string // probably | maybe | ""
	FlacDecodeOK  *bool
	FlacSeekOK    *bool
	ProbeVersion  int
	ProbedAt      *string
}

// DeviceProfile is a stored profile.
type DeviceProfile struct {
	DeviceID        string  `json:"device_id"`
	Label           string  `json:"label"`
	UserAgent       string  `json:"user_agent"`
	Quality         string  `json:"quality"`
	QualitySource   string  `json:"quality_source"`
	ProbeResult     string  `json:"probe_result"`
	FlacCanPlay     string  `json:"flac_can_play"`
	FlacDecodeOK    *bool   `json:"flac_decode_ok"`
	FlacSeekOK      *bool   `json:"flac_seek_ok"`
	ProbeVersion    int     `json:"probe_version"`
	ProbedAt        *string `json:"probed_at"`
	EffectiveFormat string  `json:"effective_format" enum:"flac,mp3"`
	UpdatedAt       string  `json:"updated_at"`
}

func (in DeviceProfileIn) validate() error {
	switch {
	case !deviceIDRe.MatchString(in.DeviceID):
		return invalid("device_id", "must match %s", deviceIDRe)
	case len(in.Label) < 1 || len([]rune(in.Label)) > 80:
		return invalid("label", "1 to 80 characters")
	case len(in.UserAgent) > 512:
		return invalid("user_agent", "at most 512 bytes")
	case !oneOf(in.Quality, enumQuality...):
		return invalid("quality", "must be one of lossless, high, auto")
	case !oneOf(in.QualitySource, "default", "user", "probe"):
		return invalid("quality_source", "must be one of default, user, probe")
	case !oneOf(in.ProbeResult, "none", "ok", "failed", "inconclusive"):
		return invalid("probe_result", "must be one of none, ok, failed, inconclusive")
	case !oneOf(in.FlacCanPlay, "probably", "maybe", ""):
		return invalid("flac_can_play", "must be probably, maybe or empty")
	case in.ProbeVersion < 0:
		return invalid("probe_version", "must not be negative")
	}
	if in.ProbedAt != nil {
		if _, err := time.Parse(time.RFC3339, *in.ProbedAt); err != nil {
			return invalid("probed_at", "want RFC 3339")
		}
	}
	return nil
}

func effectiveFormat(quality string) string {
	if quality == "high" {
		return "mp3"
	}
	return "flac"
}

func (s *Service) putActor(p auth.Principal) string {
	if p.ID != "" {
		return p.ID
	}
	return p.Name
}

// PutDeviceProfile upserts a profile; created_* never change.
func (s *Service) PutDeviceProfile(ctx context.Context, p auth.Principal, in DeviceProfileIn) (DeviceProfile, error) {
	if err := in.validate(); err != nil {
		return DeviceProfile{}, err
	}
	now := s.nowStr()
	actor := s.putActor(p)
	var probedAt any
	if in.ProbedAt != nil {
		probedAt = *in.ProbedAt
	}
	_, err := s.st.W.ExecContext(ctx, `INSERT INTO device_profiles(device_id, label, user_agent, quality, quality_source, probe_result, flac_can_play,
		flac_decode_ok, flac_seek_ok, probe_version, probed_at, created_by, updated_by, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(device_id) DO UPDATE SET label=excluded.label, user_agent=excluded.user_agent, quality=excluded.quality,
		quality_source=excluded.quality_source, probe_result=excluded.probe_result, flac_can_play=excluded.flac_can_play,
		flac_decode_ok=excluded.flac_decode_ok, flac_seek_ok=excluded.flac_seek_ok, probe_version=excluded.probe_version,
		probed_at=excluded.probed_at, updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
		in.DeviceID, in.Label, in.UserAgent, in.Quality, in.QualitySource, in.ProbeResult, in.FlacCanPlay,
		boolPtr(in.FlacDecodeOK), boolPtr(in.FlacSeekOK), in.ProbeVersion, probedAt, actor, actor, now, now)
	if err != nil {
		return DeviceProfile{}, fmt.Errorf("library: put device profile: %w", err)
	}
	row := s.st.W.QueryRowContext(ctx, deviceSelect+` WHERE device_id = ?`, in.DeviceID)
	return scanDevice(row)
}

const deviceSelect = `SELECT device_id, label, user_agent, quality, quality_source, probe_result, flac_can_play, flac_decode_ok, flac_seek_ok,
	probe_version, probed_at, updated_at FROM device_profiles`

func scanDevice(r rowScanner) (DeviceProfile, error) {
	var d DeviceProfile
	var dec, seek sql.NullInt64
	var pa sql.NullString
	if err := r.Scan(&d.DeviceID, &d.Label, &d.UserAgent, &d.Quality, &d.QualitySource, &d.ProbeResult, &d.FlacCanPlay, &dec, &seek,
		&d.ProbeVersion, &pa, &d.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return d, ErrNotFound
		}
		return d, err
	}
	d.FlacDecodeOK, d.FlacSeekOK, d.ProbedAt = nb(dec), nb(seek), ptrStr(pa)
	d.EffectiveFormat = effectiveFormat(d.Quality)
	return d, nil
}

// DeviceProfileList is one page of device profiles.
type DeviceProfileList struct {
	Items      []DeviceProfile `json:"items" nullable:"false"`
	NextCursor *string         `json:"next_cursor"`
}

// DeviceProfiles lists profiles by device id under the page byte cap.
func (s *Service) DeviceProfiles(ctx context.Context, limit int, cursor string) (DeviceProfileList, error) {
	off, err := decodeCursor(cursor)
	if err != nil {
		return DeviceProfileList{}, err
	}
	limit = clampLimit(limit, 50, 200)
	rows, err := s.st.R.QueryContext(ctx, deviceSelect+` ORDER BY device_id LIMIT ? OFFSET ?`, limit+1, off)
	if err != nil {
		return DeviceProfileList{}, err
	}
	defer rows.Close()
	out := DeviceProfileList{Items: []DeviceProfile{}}
	pg := newPager(out)
	more := false
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return out, err
		}
		if len(out.Items) >= limit || !pg.add(sizeOf(d)) {
			more = true
			break
		}
		out.Items = append(out.Items, d)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if more {
		c := encodeCursor(off + len(out.Items))
		out.NextCursor = &c
	}
	return out, nil
}
