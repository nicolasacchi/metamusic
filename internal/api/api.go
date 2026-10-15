package api

import (
	"bytes"
	"encoding/json"
	"github.com/nicolasacchi/metamusic/internal/notify"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/backup"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/connectors"
	"github.com/nicolasacchi/metamusic/internal/drafts"
	"github.com/nicolasacchi/metamusic/internal/duels"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/exports"
	"github.com/nicolasacchi/metamusic/internal/generation"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/judgments"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/lineage"
	"github.com/nicolasacchi/metamusic/internal/listening"
	"github.com/nicolasacchi/metamusic/internal/media/clip"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/media/tagged"
	"github.com/nicolasacchi/metamusic/internal/oidc"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/problem"
	"github.com/nicolasacchi/metamusic/internal/prompts"
	"github.com/nicolasacchi/metamusic/internal/songwriting"
	"github.com/nicolasacchi/metamusic/internal/spend"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
)

// SpecVersion is the OpenAPI document version. It is fixed (not the build
// version) so the committed openapi.json is reproducible.
const SpecVersion = "0.1.0"

// Deps are the collaborators operations need.
type Deps struct {
	Store           *store.Store
	Tokens          *auth.TokenStore
	Cfg             config.Config
	Version, Commit string
	Started         time.Time

	// M1 (nil-safe: registration never dereferences them; handlers answer 503).
	Library *library.Service
	Signer  *serve.Signer
	Probe   *serve.Object
	Now     func() time.Time // nil => time.Now; fixes the clock in tests

	// M2a (C28). Nil dependencies make the matching operations answer 503 UNAVAILABLE
	// once their handlers exist; registration never dereferences them.
	Judgments *judgments.Service
	Drafts    *drafts.Service
	Playlists *playlists.Service
	Exports   *exports.Service
	Backups   *backup.Service
	Context   *trackctx.Builder
	Listening *listening.Service
	Audit     *audit.Store
	Devices   *auth.DeviceStore
	Notify    notify.Notifier // nil => no notifications

	// M2b (C53). Concrete services; a nil one makes the matching operations answer 503 UNAVAILABLE {component}.
	Duels *duels.Service
	Ears  *ears.Service
	Clips *clip.Service

	// M3 (C81). Concrete services; a nil one makes the matching operations answer 503 UNAVAILABLE {component}
	// (the component names are generation, spend, jobs, provider and lineage).
	Generation *generation.Service
	Spend      *spend.Service
	Jobs       *jobs.Service
	Provider   *jobs.ProviderAdmin
	Lineage    *lineage.Service

	// M4a (C114). Concrete services; a nil one makes the matching operations answer 503 UNAVAILABLE {component}
	// (the component names are connectors, subsonic, covers, downloads, prompts and playlists; OIDC only fills
	// OIDCStatus.keys and may be nil while hosted connectors are off).
	Connectors *connectors.Store
	OIDC       *oidc.Verifier
	Subsonic   *auth.SubsonicStore
	Covers     *cover.Service
	Downloads  *tagged.Service
	Prompts    prompts.Sources

	// Songwriting holds the duration targets (settings table); nil makes its operations answer 503 UNAVAILABLE.
	Songwriting *songwriting.Service

	// NewID returns a fresh identifier; nil => uuid.NewV7().String(). Tests inject a deterministic sequence.
	NewID func() string
}

// NewIDFunc returns d.NewID, or the UUIDv7 generator when unset.
func (d Deps) NewIDFunc() func() string {
	if d.NewID != nil {
		return d.NewID
	}
	return func() string { return uuid.NewV7().String() }
}

func sortOps(ops []OpInfo) {
	slices.SortFunc(ops, func(a, b OpInfo) int {
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		return strings.Compare(a.Method, b.Method)
	})
}

// HumaConfig is the production Huma configuration, shared with tests that must
// exercise the same response shapes.
func HumaConfig() huma.Config {
	cfg := huma.DefaultConfig("metamusic", SpecVersion)
	cfg.OpenAPIPath = "/api/openapi"
	cfg.DocsPath = "/api/docs"
	cfg.SchemasPath = ""
	cfg.CreateHooks = nil
	cfg.Components.Schemas = huma.NewMapRegistry("#/components/schemas/", schemaNamer)
	return cfg
}

// wireNames gives schema components of package-local Go names a name that is
// unambiguous in the one OpenAPI namespace (Go type -> component name).
var wireNames = map[string]string{
	"internal/backup.Run":               "BackupRun",
	"internal/backup.RunStats":          "BackupRunStats",
	"internal/backup.Listing":           "BackupListing",
	"internal/backup.Snapshot":          "BackupSnapshot",
	"internal/backup.Schedule":          "BackupSchedule",
	"internal/exports.Link":             "DownloadLink",
	"internal/exports.Rename":           "ExportRename",
	"internal/exports.ABCFile":          "ExportABCFile",
	"internal/exports.Validation":       "ExportValidation",
	"internal/exports.StyleInfo":        "ExportStyle",
	"internal/exports.JSONLInfo":        "ExportJSONLInfo",
	"internal/exports.ZIPInfo":          "ExportZIPInfo",
	"internal/exports.ZIPEntry":         "ExportZIPEntry",
	"internal/export.Note":              "ExportNote",
	"internal/playlists.Item":           "PlaylistItem",
	"internal/trackctx.Bundle":          "ContextBundle",
	"internal/trackctx.Budget":          "ContextBudget",
	"internal/engine/yue2.Language":     "LintLanguage",
	"internal/judgments.SectionRef":     "CommentSection",
	"internal/judgments.TagSourceCount": "TagSourceCounts",
	"internal/library.BackupStats":      "BackupStatus",
	"internal/library.JudgmentStats":    "JudgmentCounters",
	"internal/drafts.UploadDraft":       "UploadedDraft",
	"internal/drafts.UploadIssue":       "UploadLineIssue",
	// M2b: package-local names that are too generic for the one OpenAPI namespace.
	"internal/duels.Page":               "DuelPage",
	"internal/duels.NextResult":         "NextDuel",
	"internal/duels.Window":             "ClipWindow",
	"internal/duels.Slots":              "DuelSlots",
	"internal/duels.SlotsSummary":       "DuelSlotsSummary",
	"internal/ladder.Counts":            "VerdictCounts",
	"internal/ears.Side":                "ComparisonSide",
	"internal/ears.Deltas":              "ComparisonDeltas",
	"internal/ears.Flag":                "AnalysisFlag",
	"internal/ears.Structure":           "TrackStructure",
	"internal/ears.Provenance":          "AnalysisProvenance",
	"internal/ears.SeedPair":            "SeedPair",
	"internal/analysis.Span":            "TimeSpan",
	"internal/analysis/tokens.Stream":   "TokenStream",
	"internal/analysis/tokens.Repeat":   "TokenRepeat",
	"internal/analysis/tokens.Periodic": "TokenPeriodic",
}

func schemaNamer(t reflect.Type, hint string) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if n, ok := wireNames[strings.TrimPrefix(t.PkgPath(), modulePath+"/")+"."+t.Name()]; ok {
		return n
	}
	return huma.DefaultSchemaNamer(t, hint)
}

const modulePath = "github.com/nicolasacchi/metamusic"

// New builds the Huma API on mux and registers every operation.
func New(mux *http.ServeMux, d Deps) huma.API {
	a := humago.New(mux, HumaConfig())
	registerSystem(a, d)
	registerLibrary(a, d)
	registerMedia(a, d)
	registerDraftsM2a(a, d)
	registerStats(a, d)
	registerImports(a, d)
	registerDevices(a, d)
	registerJudgments(a, d)
	registerContext(a, d)
	registerListen(a, d)
	registerPlaylists(a, d)
	registerExports(a, d)
	registerBackups(a, d)
	registerDuels(a, d)
	registerClips(a, d)
	registerEars(a, d)
	registerGenerationM3(a, d)
	registerBudgetM3(a, d)
	registerSongwriting(a, d)
	registerJobsM3(a, d)
	registerProviderM3(a, d)
	registerAudit(a, d)
	registerTokens(a, d)
	registerPairedDevices(a, d)
	registerOfflineM4a(a, d)
	registerDownloadM4a(a, d)
	registerPromptsM4a(a, d)
	registerConnectorsM4a(a, d)
	registerSubsonicClientsM4a(a, d)
	notFound(mux, "/api/")
	notFound(mux, "/human/api/")
	ValidateAliases(a)
	return a
}

// notFound makes unknown paths and wrong methods under prefix return problem+json.
func notFound(mux *http.ServeMux, prefix string) {
	mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		var allow []string
		for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			probe := r.Clone(r.Context())
			probe.Method = m
			if _, pat := mux.Handler(probe); pat != "" && pat != prefix {
				allow = append(allow, m)
			}
		}
		if len(allow) > 0 {
			w.Header().Set("Allow", strings.Join(allow, ", "))
			problem.Write(w, 405, "METHOD_NOT_ALLOWED", "method not allowed", map[string]any{"allow": allow})
			return
		}
		problem.Write(w, 404, "NOT_FOUND", "no such resource", nil)
	})
}

// OpenAPIJSON renders the spec deterministically (sorted keys, 2-space indent, trailing newline).
func OpenAPIJSON(a huma.API) ([]byte, error) {
	raw, err := json.Marshal(a.OpenAPI())
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}
