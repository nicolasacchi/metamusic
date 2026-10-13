// Package exports writes export files, records them and mints download links
// (contract C33). M2a-B4a freezes the signatures; M2a-B6 implements them.
package exports

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/export"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/playlists"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// RatingsReader supplies human stars for the MP3 POPM frame; *judgments.Service implements it.
type RatingsReader interface {
	HumanStars(ctx context.Context, trackIDs []string) (map[string]int, error)
}

// Options builds a Service.
type Options struct {
	Store     *store.Store
	Data      *os.Root
	Lib       *library.Service
	Playlists *playlists.Service
	Ratings   RatingsReader
	Source    export.Source // NewDBSource unless a test injects one
	Signer    *serve.Signer
	Now       func() time.Time
	NewID     func() string
}

// Link is a signed download link.
type Link struct {
	URL       string `json:"url" doc:"Signed, relative /media/ URL"`
	ExpiresAt string `json:"expires_at" doc:"RFC 3339 UTC"`
}

// Rename records an id changed by the unique-id rule.
type Rename struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ABCFile is an abc_path target of a bundle.
type ABCFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Validation is the batch-validator outcome of a JSONL export.
type Validation struct {
	OK     bool          `json:"ok"`
	Fatal  bool          `json:"fatal"`
	Issues []ExportIssue `json:"issues" nullable:"false"`
}

// ExportIssue is one validator issue.
type ExportIssue struct {
	Code  string `json:"code"`
	Field string `json:"field,omitempty"`
	Line  int    `json:"line,omitempty"`
	Fatal bool   `json:"fatal,omitempty"`
}

// JSONLInfo describes a songs.jsonl export.
type JSONLInfo struct {
	Rows          int           `json:"rows"`
	RawRows       int           `json:"raw_rows"`
	GeneratedRows int           `json:"generated_rows"`
	Renamed       []Rename      `json:"renamed" nullable:"false"`
	Style         StyleInfo     `json:"style"`
	Validation    Validation    `json:"validation"`
	ABCFiles      []ABCFile     `json:"abc_files" nullable:"false"`
	Warnings      []export.Note `json:"warnings" nullable:"false"`
	Content       *string       `json:"content" doc:"The JSONL text when it is at most 60000 bytes, else null"`
}

// StyleInfo is the detected JSON style of an export.
type StyleInfo struct {
	KeyOrder    []string `json:"key_order" nullable:"false"`
	ItemSep     string   `json:"item_sep"`
	KeySep      string   `json:"key_sep"`
	EnsureASCII bool     `json:"ensure_ascii"`
}

// ZIPEntry names a track's entry in a playlist ZIP.
type ZIPEntry struct {
	TrackID string `json:"track_id"`
	Entry   string `json:"entry"`
}

// ZIPInfo describes a playlist ZIP export.
type ZIPInfo struct {
	Entries    int        `json:"entries"`
	Tracks     []ZIPEntry `json:"tracks" nullable:"false"`
	MissingMP3 []string   `json:"missing_mp3" nullable:"false"`
}

// Export is one export record with a fresh link.
type Export struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind" enum:"songs_jsonl,songs_bundle,playlist_zip"`
	Filename  string     `json:"filename"`
	SHA256    string     `json:"sha256"`
	Bytes     int64      `json:"bytes"`
	CreatedAt string     `json:"created_at"`
	Download  Link       `json:"download"`
	JSONL     *JSONLInfo `json:"jsonl,omitempty"`
	ZIP       *ZIPInfo   `json:"zip,omitempty"`
}

// ExportSource is one importable JSONL path available for a "source" export.
type ExportSource struct {
	Path        string `json:"path"`
	Kind        string `json:"kind" enum:"jsonl"`
	Lines       int    `json:"lines"`
	Recipes     int    `json:"recipes"`
	FirstSeenAt string `json:"first_seen_at"`
}

// Service implements C33's exports.
type Service struct{ o Options }

// New builds the exports service.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Source == nil {
		o.Source = NewDBSource(o.Store, o.Lib, o.Playlists)
	}
	return &Service{o: o}
}

// NewDBSource is the database-backed export.Source.
func NewDBSource(st *store.Store, lib *library.Service, pl *playlists.Service) export.Source {
	return &dbSource{st: st, lib: lib, pl: pl}
}

type dbSource struct {
	st  *store.Store
	lib *library.Service
	pl  *playlists.Service
}

// STUB(M2a-B6): every dbSource method.

func (d *dbSource) JSONLSourceRows(context.Context, string) ([]export.SourceRow, error) {
	return nil, errors.ErrUnsupported
}

func (d *dbSource) SelectRecipes(context.Context, export.TrackFilter) ([]export.RecipeRow, error) {
	return nil, errors.ErrUnsupported
}

func (d *dbSource) SelectDrafts(context.Context, export.DraftFilter) ([]export.DraftRow, error) {
	return nil, errors.ErrUnsupported
}

func (d *dbSource) StyleSamples(context.Context) ([][]byte, error) { return nil, errors.ErrUnsupported }

// JSONL exports a selection as songs.jsonl (or a bundle when ABC files are needed).
func (s *Service) JSONL(ctx context.Context, p auth.Principal, sel export.Selection, filename string) (Export, error) {
	// STUB(M2a-B6)
	return Export{}, errors.ErrUnsupported
}

// PlaylistZIP exports a playlist as a ZIP of tagged MP3 files.
func (s *Service) PlaylistZIP(ctx context.Context, p auth.Principal, playlistID string) (Export, error) {
	// STUB(M2a-B6)
	return Export{}, errors.ErrUnsupported
}

// Get returns an export with a fresh link; EXPORT_EXPIRED when swept.
func (s *Service) Get(ctx context.Context, p auth.Principal, id string) (Export, error) {
	// STUB(M2a-B6)
	return Export{}, errors.ErrUnsupported
}

// Sources lists the JSONL source paths with line and recipe counts.
func (s *Service) Sources(ctx context.Context) ([]ExportSource, error) {
	// STUB(M2a-B6)
	return nil, errors.ErrUnsupported
}

// Resolve maps a media address (ext jsonl|zip) to a stored export file.
func (s *Service) Resolve(ctx context.Context, sha, ext string) (serve.Object, error) {
	// STUB(M2a-B6)
	return serve.Object{}, serve.ErrNotFound
}

// Sweep removes export files whose newest row is older than olderThan.
func (s *Service) Sweep(ctx context.Context, olderThan time.Duration) (int, error) {
	// STUB(M2a-B6)
	return 0, nil
}
