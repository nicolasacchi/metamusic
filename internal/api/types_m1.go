package api

import (
	"errors"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/library"
)

// Output envelopes of the M1 operations. Shapes that mirror library rows
// (TrackSummary, TrackDetail, DraftDetail, Stats, ...) are the library's types.

type trackPageOut struct{ Body library.TrackPage }
type facetsOut struct{ Body library.Facets }
type trackOut struct{ Body library.TrackDetail }
type peaksOut struct{ Body library.PeaksLevel }
type draftPageOut struct{ Body library.DraftPage }
type draftOut struct{ Body library.DraftDetail }
type statsOut struct{ Body library.Stats }
type importRunPageOut struct{ Body library.ImportRunPage }
type deviceProfileOut struct{ Body library.DeviceProfile }

type importRunsOut struct {
	Body struct {
		Items []library.ImportRunSummary `json:"items" nullable:"false"`
	}
}

type deviceProfilesOut struct {
	Body struct {
		Items []library.DeviceProfile `json:"items" nullable:"false"`
	}
}

// FlacLink is a signed link to the lossless master.
type FlacLink struct {
	URL           string `json:"url" doc:"Signed, relative URL; valid until expires_at"`
	ContentType   string `json:"content_type" example:"audio/flac"`
	Bytes         int64  `json:"bytes"`
	SHA256        string `json:"sha256"`
	SampleRate    int    `json:"sample_rate"`
	BitsPerSample int    `json:"bits_per_sample"`
}

// MP3Link is a signed link to the MP3 rendition.
type MP3Link struct {
	URL         string `json:"url" doc:"Signed, relative URL; valid until expires_at"`
	ContentType string `json:"content_type" example:"audio/mpeg"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"sha256"`
	BitrateKbps int    `json:"bitrate_kbps"`
}

type mediaLinksOut struct {
	Body struct {
		TrackID   string   `json:"track_id"`
		ExpiresAt string   `json:"expires_at" doc:"RFC 3339 UTC"`
		TTLS      int64    `json:"ttl_s" doc:"Link lifetime granted to the caller, seconds"`
		Flac      FlacLink `json:"flac"`
		MP3       MP3Link  `json:"mp3"`
	}
}

type deviceProbeOut struct {
	Body struct {
		Version       int     `json:"version" example:"1"`
		URL           string  `json:"url" doc:"Signed, relative URL of the probe file"`
		ContentType   string  `json:"content_type" example:"audio/flac"`
		SampleRate    int     `json:"sample_rate"`
		BitsPerSample int     `json:"bits_per_sample"`
		Channels      int     `json:"channels"`
		DurationS     float64 `json:"duration_s"`
		SeekToS       float64 `json:"seek_to_s"`
		ExpiresAt     string  `json:"expires_at" doc:"RFC 3339 UTC"`
	}
}

// Inputs.

type searchTracksIn struct {
	Q            string            `query:"q" maxLength:"200" doc:"Free text over title, request id, style and lyrics (prefix match)"`
	Status       []string          `query:"status,explode" nullable:"false" enum:"draft,to_relisten,final,discarded" doc:"Repeatable status filter"`
	Style        []string          `query:"style,explode" nullable:"false" maxItems:"5" doc:"Repeatable style term; all must match"`
	MinDurationS optParam[float64] `query:"min_duration_s" minimum:"0"`
	MaxDurationS optParam[float64] `query:"max_duration_s" minimum:"0"`
	RecordedFrom string            `query:"recorded_from" doc:"RFC 3339 date or date-time (inclusive)"`
	RecordedTo   string            `query:"recorded_to" doc:"RFC 3339 date (whole day, inclusive) or date-time"`
	HasScore     optParam[bool]    `query:"has_score"`
	Truncated    optParam[bool]    `query:"truncated"`
	RootTrackID  string            `query:"root_track_id"`
	Sort         string            `query:"sort" enum:"recorded_desc,recorded_asc,duration_desc,duration_asc,title_asc,relevance" doc:"Default recorded_desc, or relevance when q is set"`
	Limit        int               `query:"limit" minimum:"1" maximum:"100" default:"50"`
	Cursor       string            `query:"cursor" doc:"Opaque cursor from next_cursor"`
}

type trackIDIn struct {
	ID string `path:"id" doc:"Track id"`
}

type trackPeaksIn struct {
	ID    string `path:"id" doc:"Track id"`
	Level string `query:"level" enum:"overview,detail" default:"overview"`
}

type listDraftsIn struct {
	Q          string `query:"q" maxLength:"200"`
	Status     string `query:"status" enum:"backlog,fulfilled,archived" default:"backlog"`
	Collection string `query:"collection"`
	GroupKey   string `query:"group_key"`
	Origin     string `query:"origin"`
	Limit      int    `query:"limit" minimum:"1" maximum:"100" default:"50"`
	Cursor     string `query:"cursor"`
}

type draftIDIn struct {
	ID string `path:"id" doc:"Draft id"`
}

type listImportsIn struct {
	Limit int `query:"limit" minimum:"1" maximum:"200" default:"50"`
}

type importRunIn struct {
	ID      string `path:"id" doc:"Import run id"`
	Section string `query:"section" enum:"errors,warnings,unmatched,ambiguous,audio,recipes,drafts,duplicates,ignored" default:"errors"`
	Limit   int    `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Cursor  string `query:"cursor"`
}

type putDeviceProfileIn struct {
	DeviceID string `path:"device_id" pattern:"^[A-Za-z0-9_-]{8,64}$" doc:"Client-generated device id"`
	Body     struct {
		Label         string  `json:"label" minLength:"1" maxLength:"80"`
		UserAgent     string  `json:"user_agent" maxLength:"512"`
		Quality       string  `json:"quality" doc:"lossless | high | auto"`
		QualitySource string  `json:"quality_source" enum:"default,user,probe"`
		ProbeResult   string  `json:"probe_result" enum:"none,ok,failed,inconclusive"`
		FlacCanPlay   string  `json:"flac_can_play" doc:"canPlayType('audio/flac') result: probably, maybe or empty"`
		FlacDecodeOK  *bool   `json:"flac_decode_ok,omitempty" doc:"Absent when the probe was inconclusive"`
		FlacSeekOK    *bool   `json:"flac_seek_ok,omitempty"`
		ProbeVersion  int     `json:"probe_version" minimum:"0"`
		ProbedAt      *string `json:"probed_at,omitempty" doc:"RFC 3339"`
	}
}

// unavailable is the 503 for a dependency the process was started without.
func unavailable(component string) error {
	return Err(http.StatusServiceUnavailable, "UNAVAILABLE", component+" is not available", map[string]any{"component": component})
}

// libErr maps library errors to the problem model.
func libErr(err error) error {
	var ve *library.ValidationError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ve):
		return Err(http.StatusUnprocessableEntity, "VALIDATION_FAILED", ve.Error(), map[string]any{"field": ve.Field})
	case errors.Is(err, library.ErrNotFound):
		return Err(http.StatusNotFound, "NOT_FOUND", "no such resource", nil)
	case errors.Is(err, library.ErrMediaUnavailable):
		return Err(http.StatusConflict, "MEDIA_UNAVAILABLE", "the media for this track is not available", nil)
	}
	return err
}

// optParam is an optional query parameter that records whether it was sent
// (Huma does not support pointer parameters).
type optParam[T any] struct {
	Value T
	IsSet bool
}

func (o optParam[T]) Schema(r huma.Registry) *huma.Schema {
	return huma.SchemaFromType(r, reflect.TypeOf(o.Value))
}
func (o *optParam[T]) Receiver() reflect.Value      { return reflect.ValueOf(o).Elem().Field(0) }
func (o *optParam[T]) OnParamSet(isSet bool, _ any) { o.IsSet = isSet }

func (o optParam[T]) ptr() *T {
	if !o.IsSet {
		return nil
	}
	v := o.Value
	return &v
}
