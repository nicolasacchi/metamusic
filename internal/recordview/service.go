package recordview

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nicolasacchi/metamusic/internal/curation"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// The batch readers recordview needs of the L1 and M2 services. Each answers any number of ids in one statement
// (TestBatchReaders_OneStatement, lane V2). The signatures are frozen by F2; lane V2 supplies the implementations (thin
// adapters over the L1 packages, written where the composition root can see both sides) and may add fields to the value
// types only through a freeze fix.

// SessionRef is a Session a Song belongs to.
type SessionRef struct{ ID, Kind, Name, Date string }

// SongBrief is the part of a Song a View needs: no takes, no Muse text. MuseID is an id only.
type SongBrief struct {
	ID, Title   string
	Angle       *string
	MuseID      *string
	State       string
	Retired     bool
	RetiredInto *string
	Hidden      bool
	RootTrackID string   // the Song's primary root id ("" for a takes-less draft Song)
	RootSeed    int64    // that root's recipe seed
	DraftID     *string  // for a takes-less draft Song
	Engine      string   // "" when unknown
	BPM         *float64 // the effective tempo (measured, else declared, else plan)
	BPMBasis    string   // "measured" | "declared" | "plan" | ""
}

// PickRow is a Song's song_picks row with its evidence.
type PickRow struct {
	SongID   string
	TakeID   *string
	Source   string
	Evidence json.RawMessage
}

// Loudness is a Take's stored loudness analysis: the integrated measure and the 1 Hz short-term series.
type Loudness struct {
	Measure      tape.Measure
	ShortTerm1Hz []float64
}

// SessionReader returns the Sessions of Songs, keyed by Song id.
type SessionReader interface {
	SessionsOf(ctx context.Context, songIDs []string) (map[string][]SessionRef, error)
}

// SoundReader returns the Sound families of Songs (their Pick Take's genre families), keyed by Song id.
type SoundReader interface {
	FamiliesOf(ctx context.Context, songIDs []string) (map[string][]string, error)
}

// SongReader returns the briefs of Songs, keyed by Song id; unknown ids are absent.
type SongReader interface {
	Briefs(ctx context.Context, songIDs []string) (map[string]SongBrief, error)
}

// PickReader returns the current Pick rows of Songs, keyed by Song id; a Song without a row is absent.
type PickReader interface {
	Current(ctx context.Context, songIDs []string) (map[string]PickRow, error)
}

// LoudnessReader returns the stored loudness of Takes, keyed by Take id; Takes without an analysis are absent.
type LoudnessReader interface {
	Loudness(ctx context.Context, takeIDs []string) (map[string]Loudness, error)
}

// TapeBriefs lists a Record's newest tapes; *tape.Service implements it.
type TapeBriefs interface {
	Brief(ctx context.Context, recordID string, limit int) ([]tape.Brief, error)
}

// CoverLinker renders the mosaic of a cover input at 512 and 1024 px and returns signed links to the files. recordview cannot
// import the media server (C118), so app.Build adapts cover.Service.EnsureMosaic and the link signer to it.
type CoverLinker interface {
	Links(ctx context.Context, in cover.Input) (CoverRef, error)
}

// Options configures New.
type Options struct {
	Store    *store.Store
	Records  *records.Service
	Muses    *muses.Service // Refs only: the one door to Muse text
	Sessions SessionReader
	Sounds   SoundReader
	Songs    SongReader
	Picks    PickReader
	Ears     LoudnessReader
	Curation *curation.Service
	Library  *library.Service // TrackRefs: Take titles, status, duration and the cover parts
	Covers   CoverLinker
	Tapes    TapeBriefs
	Now      func() time.Time
}

// Service is the read assembly of a Record. The method set is frozen by F2 (stub_v2.go holds the bodies until lane V2).
type Service struct {
	o   Options
	now func() time.Time
}

// New builds the service. A nil Now means time.Now.
func New(o Options) *Service {
	s := &Service{o: o, now: o.Now}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

var _ tape.Source = (*Service)(nil) // Snapshot implements the tape's Source
