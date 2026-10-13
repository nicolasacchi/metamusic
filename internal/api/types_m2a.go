package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/backup"
	"github.com/nicolasacchi/metamusic/internal/drafts"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/exports"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
)

// Types of the M2a operations (contract C38), frozen by M2a-B4a. Domain shapes
// live in their service packages; this file holds the request inputs, the
// envelopes and the shapes that exist only on the wire.

// PrincipalRef is how a principal appears in a response (D-M2a-19).
type PrincipalRef = auth.PrincipalRef

// RefFor renders a principal reference for viewer. A viewer that acts as the
// operator (human or device) sees references unchanged; any other viewer
// (an agent) sees the operator as {human, "human", "human"} and a device as
// {device, <key>, "device"}. Handlers never build references by hand.
func RefFor(viewer auth.Principal, key, kind, name string) PrincipalRef {
	if viewer.ActsAsHuman() {
		return PrincipalRef{Kind: kind, ID: key, Name: name}
	}
	switch kind {
	case string(auth.KindHuman):
		return PrincipalRef{Kind: kind, ID: "human", Name: "human"}
	case string(auth.KindDevice):
		return PrincipalRef{Kind: kind, ID: key, Name: "device"}
	}
	return PrincipalRef{Kind: kind, ID: key, Name: name}
}

// KeyFor renders a key-valued string field (created_by, updated_by, status_set_by)
// for viewer: a human key becomes "human" for an agent, other keys are unchanged.
func KeyFor(viewer auth.Principal, key string) string {
	if !viewer.ActsAsHuman() && strings.HasPrefix(key, string(auth.KindHuman)+":") {
		return "human"
	}
	return key
}

// RawRequest is a request object kept as the client's exact JSON bytes, so
// seeds and other numbers reach the protocol checker as their original tokens.
// Declaring it a SchemaProvider keeps the OpenAPI schema {type: object}; a bare
// json.RawMessage would get the empty schema.
type RawRequest json.RawMessage

// Schema implements huma.SchemaProvider.
func (RawRequest) Schema(huma.Registry) *huma.Schema { return &huma.Schema{Type: huma.TypeObject} }

// UnmarshalJSON keeps the exact bytes.
func (r *RawRequest) UnmarshalJSON(b []byte) error {
	*r = append((*r)[:0], b...)
	return nil
}

// MarshalJSON writes the bytes back unchanged.
func (r RawRequest) MarshalJSON() ([]byte, error) {
	if r == nil {
		return []byte("null"), nil
	}
	return r, nil
}

// IdemHeader is embedded by every create operation (D-M2a-3).
type IdemHeader struct {
	IdempotencyKey string `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255" pattern:"^[!-~]+$" doc:"Client-chosen key; a retry with the same key and body replays the first response (24 h)."`
}

// Body limits (bytes) of the operations with a body (C38).
const (
	bodyUpload = 6 << 20
	bodyLint   = 2 << 20
	bodyDraft  = 2 << 20
	bodyListen = 256 << 10
	bodySmall  = 64 << 10
)

// ---- judgments ----

type rateTrackIn struct {
	ID   string `path:"id" doc:"Track id"`
	Body struct {
		Stars   *int    `json:"stars" required:"true" nullable:"true" minimum:"1" maximum:"5" doc:"1..5, or null to clear your rating"`
		Verdict *string `json:"verdict,omitempty" nullable:"true" maxLength:"280" doc:"Short free-text verdict, or null"`
	}
}
type ratingOut struct{ Body library.TrackRating }

type updateTrackIn struct {
	ID   string `path:"id" doc:"Track id"`
	Body struct {
		Title  *string `json:"title,omitempty" minLength:"1" maxLength:"200"`
		Status *string `json:"status,omitempty" enum:"draft,to_relisten,final,discarded"`
	}
}
type trackDetailOut struct{ Body library.TrackDetail }

type setTagsIn struct {
	ID   string `path:"id" doc:"Track id"`
	Body struct {
		Add    []string `json:"add,omitempty" maxItems:"20" doc:"Tags to add (normalised: lower case, single spaces)"`
		Remove []string `json:"remove,omitempty" maxItems:"20"`
	}
}
type tagsOut struct{ Body library.TrackTags }

type tagCountsOut struct {
	Body struct {
		Items []judgments.TagCount `json:"items" nullable:"false"`
	}
}

type addCommentIn struct {
	IdemHeader
	ID   string `path:"id" doc:"Track id"`
	Body struct {
		Body     string   `json:"body" minLength:"1" maxLength:"4000"`
		AtSecond *float64 `json:"at_second,omitempty" nullable:"true" minimum:"0" doc:"Position in the track, rounded to 0.1 s"`
		Kind     string   `json:"kind,omitempty" enum:"note,fix,keep,marker" doc:"Default note"`
	}
}
type commentOut struct{ Body judgments.Comment }

type updateCommentIn struct {
	ID   string `path:"id" doc:"Comment id"`
	Body struct {
		Body   *string `json:"body,omitempty" minLength:"1" maxLength:"4000"`
		Kind   *string `json:"kind,omitempty" enum:"note,fix,keep,marker"`
		State  *string `json:"state,omitempty" enum:"open,resolved,addressed,regressed"`
		Hidden *bool   `json:"hidden,omitempty"`
	}
}

type listCommentsIn struct {
	ID            string   `path:"id" doc:"Track id"`
	State         []string `query:"state,explode" nullable:"false" enum:"open,addressed,resolved,regressed"`
	Kind          []string `query:"kind,explode" nullable:"false" enum:"note,fix,keep,marker"`
	IncludeHidden bool     `query:"include_hidden"`
	Order         string   `query:"order" enum:"newest,time" default:"newest"`
	Limit         int      `query:"limit" minimum:"1" maximum:"200" default:"100"`
	Cursor        string   `query:"cursor"`
}
type commentPageOut struct{ Body judgments.CommentPage }

// ---- context and listening ----

type contextIn struct {
	ID      string   `path:"id" doc:"Track id"`
	Include []string `query:"include,explode" nullable:"false" enum:"score,lyrics,comments" doc:"Parts to add and protect from trimming"`
}
type contextOut struct{ Body trackctx.Bundle }

type listenEventIn struct {
	ID        string  `json:"id" doc:"Client-generated UUID (UUIDv7); re-sending the same id is harmless"`
	TrackID   string  `json:"track_id"`
	SessionID string  `json:"session_id"`
	Kind      string  `json:"kind" enum:"start,segment,seek,complete,skip"`
	FromMS    *int64  `json:"from_ms,omitempty" minimum:"0"`
	ToMS      *int64  `json:"to_ms,omitempty" minimum:"0"`
	Format    *string `json:"format,omitempty" enum:"flac,mp3"`
	DeviceID  *string `json:"device_id,omitempty"`
	ClientAt  string  `json:"client_at" format:"date-time"`
}

type logListenIn struct {
	Body struct {
		Events []listenEventIn `json:"events" minItems:"1" maxItems:"200"`
	}
}

type listenRejected struct {
	ID   string `json:"id"`
	Code string `json:"code"`
}

type logListenOut struct {
	Body struct {
		Accepted   int              `json:"accepted"`
		Duplicates int              `json:"duplicates"`
		Rejected   []listenRejected `json:"rejected" nullable:"false"`
	}
}

// ---- drafts ----

type lintItemIn struct {
	Request RawRequest `json:"request" doc:"The request object to lint, as sent"`
	DraftID string     `json:"draft_id,omitempty"`
}

type lintRecipeIn struct {
	Body struct {
		Items []lintItemIn `json:"items" minItems:"1" maxItems:"200"`
		Batch *bool        `json:"batch,omitempty" doc:"Also check ids for duplicates across items (default true)"`
	}
}
type lintOut struct{ Body yue2.LintReport }

type createDraftIn struct {
	IdemHeader
	Body struct {
		Request       RawRequest `json:"request" doc:"Song request object (id, style or tags, lyrics, cot, seed, abc, cfg_scale)"`
		Label         *string    `json:"label,omitempty" maxLength:"40"`
		ParentTrackID *string    `json:"parent_track_id,omitempty"`
		Collection    *string    `json:"collection,omitempty" minLength:"1" maxLength:"80"`
	}
}

type updateDraftIn struct {
	ID   string `path:"id" doc:"Draft id"`
	Body struct {
		Revision      int         `json:"revision" minimum:"1" doc:"The revision you read; a stale value answers 409 REVISION_CONFLICT"`
		Request       *RawRequest `json:"request,omitempty"`
		Label         *string     `json:"label,omitempty" nullable:"true" maxLength:"40" doc:"null or empty clears"`
		Status        *string     `json:"status,omitempty" enum:"backlog,archived"`
		ParentTrackID *string     `json:"parent_track_id,omitempty" nullable:"true" doc:"null or empty clears"`
	}
}

type uploadBatchIn struct {
	IdemHeader
	Body struct {
		JSONL         string            `json:"jsonl" doc:"songs.jsonl text, one request per line (at most 1000 lines, 4 MiB)"`
		Collection    *string           `json:"collection,omitempty" minLength:"1" maxLength:"80"`
		ParentTrackID *string           `json:"parent_track_id,omitempty"`
		AllowPartial  bool              `json:"allow_partial,omitempty" doc:"Create the clean lines even when others fail"`
		ABCFiles      map[string]string `json:"abc_files,omitempty" doc:"abc_path to ABC text (at most 50 files, 1 MiB each)"`
	}
}
type uploadOut struct{ Body drafts.UploadResult }

// ---- playlists ----

type includeHiddenIn struct {
	IncludeHidden bool `query:"include_hidden"`
}
type playlistsOut struct {
	Body struct {
		Items []playlists.PlaylistSummary `json:"items" nullable:"false"`
	}
}
type playlistIDIn struct {
	ID string `path:"id" doc:"Playlist id"`
}
type playlistOut struct{ Body playlists.Playlist }

type createPlaylistIn struct {
	IdemHeader
	Body struct {
		Name        string   `json:"name" minLength:"1" maxLength:"120"`
		Description string   `json:"description,omitempty" maxLength:"2000"`
		TrackIDs    []string `json:"track_ids,omitempty" maxItems:"1000"`
	}
}

type updatePlaylistIn struct {
	ID   string `path:"id" doc:"Playlist id"`
	Body struct {
		Revision    int      `json:"revision" minimum:"1"`
		Name        *string  `json:"name,omitempty" minLength:"1" maxLength:"120"`
		Description *string  `json:"description,omitempty" maxLength:"2000"`
		TrackIDs    []string `json:"track_ids,omitempty" maxItems:"1000" doc:"Full ordered replacement; exclusive with add_track_ids"`
		AddTrackIDs []string `json:"add_track_ids,omitempty" maxItems:"1000" doc:"Appended in order; duplicates allowed"`
		Hidden      *bool    `json:"hidden,omitempty"`
	}
}

// ---- exports ----

type exportPlaylistIn struct {
	IdemHeader
	ID string `path:"id" doc:"Playlist id"`
}
type exportOut struct{ Body exports.Export }

type exportSourcesOut struct {
	Body struct {
		Items []exports.ExportSource `json:"items" nullable:"false"`
	}
}

type exportTracksSel struct {
	Status     []string `json:"status,omitempty" enum:"draft,to_relisten,final,discarded"`
	MinStars   *int     `json:"min_stars,omitempty" minimum:"1" maximum:"5"`
	Tags       []string `json:"tags,omitempty"`
	PlaylistID string   `json:"playlist_id,omitempty"`
	TrackIDs   []string `json:"track_ids,omitempty"`
}

type exportDraftsSel struct {
	Status     []string `json:"status,omitempty" enum:"backlog,fulfilled,archived"`
	Collection string   `json:"collection,omitempty"`
	Origin     string   `json:"origin,omitempty"`
	GroupKey   string   `json:"group_key,omitempty"`
	DraftIDs   []string `json:"draft_ids,omitempty"`
}

type exportJSONLIn struct {
	IdemHeader
	Body struct {
		Selection  string           `json:"selection" enum:"source,tracks,drafts"`
		SourcePath string           `json:"source_path,omitempty" doc:"Selection source: a path from list_export_sources"`
		Tracks     *exportTracksSel `json:"tracks,omitempty"`
		Drafts     *exportDraftsSel `json:"drafts,omitempty"`
		ForBatch   bool             `json:"for_batch,omitempty" doc:"Selection source only: make ids unique so yue2 batch accepts the file"`
		Filename   string           `json:"filename,omitempty" maxLength:"120"`
	}
}

type exportIDIn struct {
	ID string `path:"id" doc:"Export id"`
}

// ---- backups ----

type createBackupIn struct{ IdemHeader }
type backupRunOut struct{ Body backup.Run }
type backupListingOut struct{ Body backup.Listing }

// ---- audit ----

type auditEntry struct {
	ID          string       `json:"id"`
	At          string       `json:"at"`
	Principal   PrincipalRef `json:"principal"`
	Transport   string       `json:"transport"`
	Client      string       `json:"client"`
	Op          string       `json:"op"`
	Method      string       `json:"method"`
	Status      int          `json:"status"`
	ErrorCode   *string      `json:"error_code"`
	Idempotency string       `json:"idempotency"`
	CreatedIDs  []string     `json:"created_ids" nullable:"false"`
	ArgsSHA256  string       `json:"args_sha256"`
	DurationMS  int64        `json:"duration_ms"`
}

type getAuditIn struct {
	PrincipalKind []string `query:"principal_kind,explode" nullable:"false" enum:"human,device,agent,system"`
	PrincipalID   string   `query:"principal_id" doc:"A Principal.Key(); agents may only pass agent: or device: keys"`
	Op            []string `query:"op,explode" nullable:"false"`
	ExcludeOp     []string `query:"exclude_op,explode" nullable:"false"`
	Since         string   `query:"since" doc:"RFC 3339"`
	Until         string   `query:"until" doc:"RFC 3339"`
	StatusMin     int      `query:"status_min" minimum:"100" maximum:"599"`
	Limit         int      `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Cursor        string   `query:"cursor"`
}
type auditOut struct {
	Body struct {
		Items      []auditEntry `json:"items" nullable:"false"`
		NextCursor *string      `json:"next_cursor"`
	}
}

// ---- tokens and devices ----

// TokenInfo is a token as listed; it never carries the token or its hash.
type TokenInfo struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Prefix     string   `json:"prefix"`
	Scopes     []string `json:"scopes" nullable:"false"`
	CreatedAt  string   `json:"created_at"`
	LastUsedAt *string  `json:"last_used_at"`
	RevokedAt  *string  `json:"revoked_at"`
}

// CreatedToken is TokenInfo plus the token, shown once.
type CreatedToken struct {
	TokenInfo
	Token *string `json:"token" doc:"The new token; null on an idempotent replay"`
}

// Device is a paired device as listed (human-only operations).
type Device struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Prefix     string   `json:"prefix"`
	Scopes     []string `json:"scopes" nullable:"false"`
	UserAgent  string   `json:"user_agent"`
	PairedBy   string   `json:"paired_by"`
	CreatedAt  string   `json:"created_at"`
	LastSeenAt *string  `json:"last_seen_at"`
	ExpiresAt  string   `json:"expires_at"`
	RevokedAt  *string  `json:"revoked_at"`
}

// DeviceSelf is the caller's own device, nothing else.
type DeviceSelf struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	RevokedAt *string `json:"revoked_at"`
}

// PairingCodeOut is a freshly minted pairing code; the nulls appear on an idempotent replay.
type PairingCodeOut struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Code      *string `json:"code" doc:"Crockford base32, shown once"`
	PairURL   *string `json:"pair_url" doc:"<public url>/pair#<code>"`
	ExpiresAt string  `json:"expires_at"`
}

type tokenListOut struct {
	Body struct {
		Items []TokenInfo `json:"items" nullable:"false"`
	}
}
type tokenOut struct{ Body TokenInfo }
type createTokenIn struct {
	IdemHeader
	Body struct {
		Name   string   `json:"name" minLength:"1" maxLength:"80"`
		Scopes []string `json:"scopes" minItems:"1" maxItems:"4" uniqueItems:"true" enum:"read,write,generate,admin"`
	}
}
type createdTokenOut struct{ Body CreatedToken }
type tokenIDIn struct {
	ID string `path:"id" doc:"Token id"`
}

type deviceListOut struct {
	Body struct {
		Items []Device `json:"items" nullable:"false"`
	}
}
type deviceOut struct{ Body Device }
type deviceIDIn struct {
	ID string `path:"id" doc:"Device id"`
}
type createPairingIn struct {
	IdemHeader
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"80" doc:"Label of the device being paired"`
	}
}
type pairingOut struct{ Body PairingCodeOut }
type deviceSelfOut struct {
	SetCookie http.Cookie `header:"Set-Cookie" doc:"Clears the device cookie"`
	Body      DeviceSelf
}
