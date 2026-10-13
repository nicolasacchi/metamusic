package export

import (
	"context"
	"errors"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// ErrUnknownSource is returned by Source.JSONLSourceRows for a path with no jsonl_line rows.
var ErrUnknownSource = errors.New("export: unknown source path")

// TrackFilter selects tracks (Selection kind "tracks").
type TrackFilter struct {
	Status     []string
	MinStars   *int
	Tags       []string
	PlaylistID string
	TrackIDs   []string
}

// DraftFilter selects drafts (Selection kind "drafts").
type DraftFilter struct {
	Status                       []string // default backlog
	Collection, Origin, GroupKey string
	DraftIDs                     []string
}

// Selection says what to export.
type Selection struct {
	Kind       string // "source" | "tracks" | "drafts"
	SourcePath string // Kind=source: a recipe_sources.source_path of raw_kind jsonl_line
	Tracks     TrackFilter
	Drafts     DraftFilter
	ForBatch   bool // Kind=source only; tracks and drafts always apply the unique-id rule (D-M2a-20)
}

// Source is the database view Build reads. exports.DBSource is the only implementation.
type Source interface {
	// JSONLSourceRows returns every jsonl_line recipe_sources row with that path, all import runs,
	// ordered by (line_no, import_run_id); ErrUnknownSource when the path has none.
	JSONLSourceRows(ctx context.Context, path string) ([]SourceRow, error)
	// SelectRecipes returns one row per distinct recipe of the tracks matching f.
	SelectRecipes(ctx context.Context, f TrackFilter) ([]RecipeRow, error)
	// UnknownRecipeTracks returns the ids of the tracks matching f whose recipe is unknown (reference
	// tracks, C59): SelectRecipes leaves them out, Build names them in a recipe_unknown_excluded note.
	UnknownRecipeTracks(ctx context.Context, f TrackFilter) ([]string, error)
	// SelectDrafts returns the drafts matching f.
	SelectDrafts(ctx context.Context, f DraftFilter) ([]DraftRow, error)
	// StyleSamples returns the raw bytes of every jsonl_line source (DetectStyle input).
	StyleSamples(ctx context.Context) ([][]byte, error)
}

// SourceRow is one recorded line of an imported JSONL file.
type SourceRow struct {
	LineNo      int
	Raw         []byte
	ImportRunID string // UUIDv7: time-ordered
	RecipeID    string
	ABC         string // the recipe's inlined abc when Raw carries abc_path, else ""
}

// RawSource is one jsonl_line occurrence of a recipe.
type RawSource struct {
	Path        string
	LineNo      int
	Raw         []byte
	ImportRunID string
}

// RecipeRow is one distinct recipe of a track selection.
type RecipeRow struct {
	RecipeID, Hash, RequestID string
	Request                   yue2.Request // canonical request (abc inlined)
	FirstRecordedAt           string       // earliest take's recorded_at
	JSONLSources              []RawSource  // ordered by (path, line_no)
}

// DraftRow is one selected draft.
type DraftRow struct {
	DraftID, GroupKey, RequestID string
	Request                      yue2.Request
}

// Note is a non-fatal observation about an export (raw_not_batch_valid, abc_missing, ...).
type Note struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Row is one exported line.
type Row struct {
	Bytes       []byte
	RecipeHash  string
	RequestID   string
	Raw         bool
	RenamedFrom string
	ABCPath     string
}

// Result is the outcome of Build.
type Result struct {
	JSONL      []byte            // rows joined with "\n", trailing "\n"
	ABCFiles   map[string][]byte // abc_path targets referenced by raw rows, relative to the JSONL
	Rows       []Row
	Style      Style
	Validation yue2.BatchReport // ValidateBatch(JSONL, ABCFiles as fs.FS)
	Warnings   []Note
}
