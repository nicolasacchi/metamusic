package api

import (
	"reflect"

	"github.com/danielgtaylor/huma/v2"
)

// Types of the M4a operations (contract C113), frozen by M4a-B2. The owning lanes fill the handlers; they do not
// change these shapes (a needed change goes back through the plan and a Stage-1 fix commit).

// Body limits of the M4a operations that take a body (C113).
const bodyM4a = bodySmall

// nullableOf renders T (a method-less shadow of the real type) as an inline object schema with Nullable=true.
// Huma refuses nullable fields that point at a $ref object (the repo convention, see library.nullableObject).
func nullableOf[T any](r huma.Registry) *huma.Schema {
	cp := *huma.SchemaFromType(r, reflect.TypeFor[T]())
	cp.Nullable = true
	return &cp
}

// ---- offline set (C111) ----

// OfflineFlac is the lossless master of an offline-set item.
type OfflineFlac struct {
	SHA256        string `json:"sha256"`
	Bytes         int64  `json:"bytes"`
	SampleRate    int    `json:"sample_rate"`
	BitsPerSample int    `json:"bits_per_sample"`
}

type offlineFlacShape OfflineFlac

// OfflineFlacOrNull is an OfflineFlac that is null for a lossy reference track.
type OfflineFlacOrNull OfflineFlac

// Schema implements huma.SchemaProvider.
func (OfflineFlacOrNull) Schema(r huma.Registry) *huma.Schema { return nullableOf[offlineFlacShape](r) }

// OfflineMP3 is the MP3 rendition of an offline-set item (a lossy track's master, D-M4a-25).
type OfflineMP3 struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type offlineMP3Shape OfflineMP3

// OfflineMP3OrNull is an OfflineMP3 that is null for a track without an MP3 rendition.
type OfflineMP3OrNull OfflineMP3

// Schema implements huma.SchemaProvider.
func (OfflineMP3OrNull) Schema(r huma.Registry) *huma.Schema { return nullableOf[offlineMP3Shape](r) }

// OfflineItem is one track of the offline set with the media facts a device needs to plan downloads.
type OfflineItem struct {
	Position  int                `json:"position" minimum:"1"`
	TrackID   string             `json:"track_id"`
	Title     string             `json:"title"`
	Album     string             `json:"album" doc:"The lineage root's current title"`
	DurationS float64            `json:"duration_s"`
	Status    string             `json:"status" enum:"draft,to_relisten,final,discarded"`
	Flac      *OfflineFlacOrNull `json:"flac" doc:"null for a lossy reference track"`
	MP3       *OfflineMP3OrNull  `json:"mp3" doc:"null when the track has no MP3 rendition; for a lossy track the master itself"`
	Lossy     bool               `json:"lossy"`
}

// OfflineTotals are the sums over the items.
type OfflineTotals struct {
	Items     int   `json:"items"`
	FlacBytes int64 `json:"flac_bytes"`
	MP3Bytes  int64 `json:"mp3_bytes"`
}

// OfflineSet is the protected offline-set playlist with per-item media facts.
type OfflineSet struct {
	PlaylistID string        `json:"playlist_id"`
	Name       string        `json:"name"`
	Revision   int           `json:"revision" doc:"Send it with update_playlist (optimistic locking)"`
	UpdatedAt  string        `json:"updated_at"`
	LimitItems int           `json:"limit_items" doc:"At most this many tracks (100)"`
	Items      []OfflineItem `json:"items" nullable:"false"`
	Totals     OfflineTotals `json:"totals"`
}

type offlineSetOut struct{ Body OfflineSet }

// ---- downloads (C110.2) ----

// DownloadTags are the tags written into a tagged download.
type DownloadTags struct {
	Title       string  `json:"title"`
	Album       string  `json:"album"`
	TrackNumber int     `json:"track_number"`
	TrackTotal  int     `json:"track_total"`
	Date        string  `json:"date" doc:"YYYY-MM-DD"`
	Genre       *string `json:"genre" doc:"null when the track has no style terms"`
}

// TrackDownloadLink is a signed link to a track's download: a FLAC copy carrying the current tags, or for a lossy
// track its MP3 master unchanged. (The plan calls it DownloadLink; that schema name is M2a's export link.)
type TrackDownloadLink struct {
	TrackID   string       `json:"track_id"`
	URL       string       `json:"url" doc:"Signed, relative URL (/media/<key>.flac, or the master's /media/<sha>.mp3 for a lossy track)"`
	Filename  string       `json:"filename" doc:"Suggested file name, [a-z0-9._-]"`
	Bytes     int64        `json:"bytes"`
	SHA256    string       `json:"sha256" doc:"Of the downloaded file"`
	ExpiresAt string       `json:"expires_at" doc:"RFC 3339 UTC"`
	TTLS      int64        `json:"ttl_s" doc:"Link lifetime granted to the caller, seconds"`
	Tags      DownloadTags `json:"tags"`
}

type downloadLinkOut struct{ Body TrackDownloadLink }

// ---- prompts (C112.2) ----

// PromptArgument describes one argument of a prompt.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// PromptResource is a resource embedded in a prompt message.
type PromptResource struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mime_type"`
	Text     string `json:"text"`
}

// PromptContent is the content of one message: type "text" carries text, type "resource" carries resource.
type PromptContent struct {
	Type     string          `json:"type" enum:"text,resource"`
	Text     string          `json:"text,omitempty"`
	Resource *PromptResource `json:"resource,omitempty"`
}

// PromptMessage is one rendered message.
type PromptMessage struct {
	Role    string        `json:"role" enum:"user"`
	Content PromptContent `json:"content"`
}

// PromptView is a rendered prompt.
type PromptView struct {
	Name        string           `json:"name"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Version     string           `json:"version"`
	Arguments   []PromptArgument `json:"arguments" nullable:"false"`
	Messages    []PromptMessage  `json:"messages" nullable:"false"`
}

type getPromptIn struct {
	Name    string `path:"name" enum:"critique_track,write_variant,session_review" doc:"Prompt name"`
	TrackID string `query:"track_id" maxLength:"64" doc:"Required for critique_track and write_variant"`
	Goal    string `query:"goal" maxLength:"500" doc:"write_variant: what the variant should achieve"`
	Since   string `query:"since" maxLength:"64" doc:"session_review: RFC 3339 start (default now - 12 h; at most 30 days back)"`
}

type promptOut struct{ Body PromptView }

// ---- connector grants (C106, C113) ----

// OIDCClientInfo is one allow-listed OAuth client.
type OIDCClientInfo struct {
	ClientID string `json:"client_id"`
	Label    string `json:"label"`
}

// OIDCKeys is the state of the JWKS cache.
type OIDCKeys struct {
	Count     int     `json:"count"`
	FetchedAt *string `json:"fetched_at" doc:"RFC 3339 UTC; null before the first successful fetch"`
	LastError *string `json:"last_error" doc:"An error code, never a message"`
}

// OIDCStatus describes the hosted-connector configuration.
type OIDCStatus struct {
	Enabled          bool             `json:"enabled"`
	Issuer           *string          `json:"issuer" doc:"null while MM_OIDC_ISSUER is unset"`
	Resource         string           `json:"resource" doc:"<PublicURL>/mcp, the only accepted audience"`
	Alg              string           `json:"alg" example:"RS256"`
	RequiredScope    string           `json:"required_scope" example:"metamusic"`
	ScopesAdvertised []string         `json:"scopes_advertised" nullable:"false"`
	Clients          []OIDCClientInfo `json:"clients" nullable:"false"`
	Keys             OIDCKeys         `json:"keys"`
}

// GrantLimits are a grant's own spend limits; null means the global default applies.
type GrantLimits struct {
	DailyBudgetMicroUSD *int64  `json:"daily_budget_micro_usd"`
	DailyBudgetUSD      *string `json:"daily_budget_usd"`
	AutoApproveTakes    *int    `json:"auto_approve_takes"`
	AutoApproveMicroUSD *int64  `json:"auto_approve_micro_usd"`
	AutoApproveUSD      *string `json:"auto_approve_usd"`
}

// GrantEffective are the limits that apply to the grant right now (its own, else the global defaults).
type GrantEffective struct {
	DailyBudgetMicroUSD int64  `json:"daily_budget_micro_usd"`
	DailyBudgetUSD      string `json:"daily_budget_usd"`
	AutoApproveTakes    int    `json:"auto_approve_takes"`
	AutoApproveMicroUSD int64  `json:"auto_approve_micro_usd"`
	AutoApproveUSD      string `json:"auto_approve_usd"`
}

// ConnectorGrant is one hosted-connector grant. username and subject_hint are null for a non-human caller.
type ConnectorGrant struct {
	ID              string              `json:"id"`
	ClientID        string              `json:"client_id"`
	Label           string              `json:"label"`
	Username        *string             `json:"username" doc:"null for a non-human caller"`
	SubjectHint     *string             `json:"subject_hint" doc:"First 8 characters of the token subject; null for a non-human caller"`
	Scopes          []string            `json:"scopes" nullable:"false" enum:"read,write,generate"`
	GenerateEnabled bool                `json:"generate_enabled"`
	Limits          GrantLimits         `json:"limits"`
	Effective       GrantEffective      `json:"effective"`
	CreatedAt       string              `json:"created_at"`
	LastUsedAt      *string             `json:"last_used_at"`
	UpdatedAt       string              `json:"updated_at"`
	UpdatedBy       PrincipalRef        `json:"updated_by"`
	RevokedAt       *string             `json:"revoked_at"`
	RevokedBy       *PrincipalRefOrNull `json:"revoked_by"`
}

// ConnectorGrantList is the answer of list_connector_grants.
type ConnectorGrantList struct {
	OIDC  OIDCStatus       `json:"oidc"`
	Items []ConnectorGrant `json:"items" nullable:"false"`
}

type connectorListOut struct{ Body ConnectorGrantList }
type connectorOut struct{ Body ConnectorGrant }

// GrantLimitsIn is the whole limits object of set_connector_grant: all three members are required, null means
// the global default (a whole object avoids the absent-vs-null ambiguity).
type GrantLimitsIn struct {
	DailyBudgetMicroUSD *int64 `json:"daily_budget_micro_usd" required:"true" nullable:"true" minimum:"0" maximum:"1000000000"`
	AutoApproveTakes    *int   `json:"auto_approve_takes" required:"true" nullable:"true" minimum:"0" maximum:"100"`
	AutoApproveMicroUSD *int64 `json:"auto_approve_micro_usd" required:"true" nullable:"true" minimum:"0" maximum:"1000000000"`
}

type setConnectorGrantIn struct {
	ID   string `path:"id" doc:"Grant id"`
	Body struct {
		Scopes  []string       `json:"scopes,omitempty" enum:"read,write,generate" uniqueItems:"true" maxItems:"3" doc:"Must contain read"`
		Limits  *GrantLimitsIn `json:"limits,omitempty"`
		Revoked *bool          `json:"revoked,omitempty" doc:"true revokes the grant (the next request is refused CONNECTOR_REVOKED); false restores it"`
	}
}

// ---- Subsonic clients (C107, C113) ----

// SubsonicClient is one native-app credential row; it never carries key material.
type SubsonicClient struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Username      string       `json:"username"`
	Scopes        []string     `json:"scopes" nullable:"false" enum:"read,write"`
	DefaultFormat string       `json:"default_format" enum:"raw,mp3"`
	HasAPIKey     bool         `json:"has_api_key"`
	APIKeyPrefix  *string      `json:"api_key_prefix" doc:"First 8 characters after mms_"`
	HasPassword   bool         `json:"has_password"`
	CreatedAt     string       `json:"created_at"`
	CreatedBy     PrincipalRef `json:"created_by"`
	LastSeenAt    *string      `json:"last_seen_at"`
	LastClient    *string      `json:"last_client"`
	RevokedAt     *string      `json:"revoked_at"`
}

// SubsonicClientList is the answer of list_subsonic_clients.
type SubsonicClientList struct {
	ServerURL  string           `json:"server_url" doc:"What to enter in the app (the public URL)"`
	APIVersion string           `json:"api_version" example:"1.16.1"`
	Items      []SubsonicClient `json:"items" nullable:"false"`
}

// CreatedSubsonicClient is the answer of create_subsonic_client; the secrets are shown once (null on replay).
type CreatedSubsonicClient struct {
	Client    SubsonicClient `json:"client"`
	APIKey    *string        `json:"api_key" doc:"mms_..., shown once; null on an idempotent replay"`
	Password  *string        `json:"password" doc:"App password, shown once; null on an idempotent replay"`
	ServerURL string         `json:"server_url"`
	Username  string         `json:"username"`
}

type subsonicListOut struct{ Body SubsonicClientList }
type subsonicOut struct{ Body SubsonicClient }
type subsonicCreatedOut struct{ Body CreatedSubsonicClient }

type createSubsonicClientIn struct {
	IdemHeader
	Body struct {
		Name          string `json:"name" minLength:"1" maxLength:"80" doc:"Label of the app, e.g. its device"`
		Username      string `json:"username,omitempty" pattern:"^[a-z0-9][a-z0-9._-]{2,31}$" doc:"Default: a slug of the name"`
		Write         *bool  `json:"write,omitempty" doc:"May the app rate, star and bookmark? Default true"`
		DefaultFormat string `json:"default_format,omitempty" enum:"raw,mp3" doc:"Stream format when the app names none (default raw)"`
		APIKey        *bool  `json:"api_key,omitempty" doc:"Create an API key (default true)"`
		Password      *bool  `json:"password,omitempty" doc:"Create an app password for token authentication (default true)"`
	}
}

type subsonicIDIn struct {
	ID string `path:"id" doc:"Subsonic client id"`
}

// ---- amended: get_media_links cover, CapsView connectors ----

// CoverLinks are signed links to a track's deterministic cover art (C110.1).
type CoverLinks struct {
	URL512    string `json:"url_512" doc:"Signed, relative /media/<key>.png URL"`
	URL1024   string `json:"url_1024"`
	ExpiresAt string `json:"expires_at" doc:"RFC 3339 UTC"`
}

type coverLinksShape CoverLinks

// CoverLinksOrNull is a CoverLinks that is null while no cover service is wired.
type CoverLinksOrNull CoverLinks

// Schema implements huma.SchemaProvider.
func (CoverLinksOrNull) Schema(r huma.Registry) *huma.Schema { return nullableOf[coverLinksShape](r) }

// CapConnector is the spend limits of one connector grant inside CapsView (C106).
type CapConnector struct {
	GrantID             string  `json:"grant_id"`
	Label               string  `json:"label"`
	DailyBudgetMicroUSD *int64  `json:"daily_budget_micro_usd" doc:"null: the global agent daily cap applies"`
	DailyBudgetUSD      *string `json:"daily_budget_usd"`
	AutoApproveTakes    *int    `json:"auto_approve_takes"`
	AutoApproveMicroUSD *int64  `json:"auto_approve_micro_usd"`
	AutoApproveUSD      *string `json:"auto_approve_usd"`
}
