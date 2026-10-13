// Package library is the read/write service over recipes, tracks, drafts,
// media, analyses, import runs and device profiles (C17).
//
// Reference tracks (M2c) are tracks whose recipe is a stub with engine 'unknown'. Their recipes.seed is
// the placeholder 0, which is also a valid real seed: any code that reads seeds, groups tracks by
// request id or offers a track as a mother must check RecipeKnown / recipe_known first.
package library

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Service owns all library SQL. Writes are meant for the importer; reads back
// the registry operations.
type Service struct {
	st   *store.Store
	data *os.Root
	now  func() time.Time
	ids  func() string
	// legacyV2 makes InsertTake avoid every M2a column and table (revision,
	// track_tags), so FixtureV2 can build a real schema-version-2 database for
	// the upgrade test. Tests only; see UseLegacyV2Schema.
	legacyV2 bool
}

// New builds a Service. data may be nil for callers that never read files
// (writes, tests); a nil now means time.Now.
func New(st *store.Store, data *os.Root, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, data: data, now: now, ids: func() string { return uuid.NewV7().String() }}
}

// Sentinel errors.
var (
	ErrNotFound         = errors.New("library: not found")
	ErrMediaUnavailable = errors.New("library: media rendition missing")
	ErrImportInProgress = errors.New("library: import already in progress")
)

// ValidationError marks caller input that is well-formed JSON but not an
// acceptable value (HTTP 422).
type ValidationError struct{ Field, Msg string }

func (e *ValidationError) Error() string { return "library: invalid " + e.Field + ": " + e.Msg }

func invalid(field, format string, a ...any) error {
	return &ValidationError{Field: field, Msg: fmt.Sprintf(format, a...)}
}

// maxPageBytes caps the serialized size of any list page (C17).
const maxPageBytes = 90000

// UseIDFunc replaces the UUIDv7 generator for rows the service names itself
// (recipes, drafts, mutations, runs). Fixtures use it to get stable ids.
func (s *Service) UseIDFunc(f func() string) { s.ids = f }

func (s *Service) newID() string { return s.ids() }

// UseLegacyV2Schema makes InsertTake write against a schema-version-2 database
// (no draft revisions, no tag inheritance). It exists for librarytest.FixtureV2
// and the upgrade test; production code never calls it.
func (s *Service) UseLegacyV2Schema() { s.legacyV2 = true }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func (s *Service) nowStr() string { return ts(s.now()) }

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func oneOf(v string, set ...string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

// Go-validated enumerations (the schema deliberately has no CHECK for these).
var (
	enumRawKind  = []string{"jsonl_line", "json_array_item", "json_file"}
	enumOrigin   = []string{"import_backlog", "varianti_import", "editor", "agent", "jsonl_upload"}
	enumRendKind = []string{"mp3_320", "peaks"}
	enumQuality  = []string{"lossless", "high", "auto"}
)

// Rules a take can be matched by (tracks.import_rule), plus 'job' for M3.
var enumRule = []string{"mapping", "result_dir", "tag", "filename_id", "sidecar_json", "slug", "slug_seed", "title_slug", "job", RuleReference}

// SHA256Hex is the hex sha256 of b (exported for fixtures and importers).
func SHA256Hex(b []byte) string { return sha256Hex(b) }
