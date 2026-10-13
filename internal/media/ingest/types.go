// Package ingest freezes a source FLAC into the library (C15): copy, verify,
// analyse, tag once, MP3 rendition, atomic name claim.
package ingest

import (
	"io/fs"
	"time"

	"github.com/nicolasacchi/metamusic/internal/analysis"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
)

// ArtifactSrc is one result-dir artifact to keep under takes/<TrackID>/.
type ArtifactSrc struct {
	Name   string  // safe top-level name (C16.1)
	File   fs.File // opened by the importer via inbox.OpenRegular; never a path
	SHA256 string  // from the inventory
}

// Request is one ingest. The importer owns and closes Source and artifact files.
type Request struct {
	TrackID, Title, Slug, RecipeID, RequestID string
	Seed                                      int64
	Source                                    fs.File // regular file, identity-checked by the importer
	SourceSHA256                              string  // from the inventory; re-verified while copying
	RecordedAt                                time.Time
	Artifacts                                 []ArtifactSrc // result-dir files and/or score.abc (Name "score.abc")
	// Reference marks a recipe-less FLAC take: the freeze writes MM_REFERENCE=1 in place of MM_SEED (D-M2c-12).
	Reference bool
}

// LossyInfo is the decoded shape of a lossy (MP3) master; IngestLossy fills it.
type LossyInfo struct {
	SampleRate, Channels int
	TotalSamples         uint64
	DurationS            float64
}

// Rendition is a derived or kept file.
type Rendition struct {
	Kind, RelPath, SHA256 string
	Bytes                 int64
	Params                map[string]any
}

// Result is what a successful Ingest produced.
type Result struct {
	MasterRelPath, MasterSHA256, FramesSHA256, AudioMD5 string
	MasterBytes                                         int64
	Info                                                flacmeta.Info // post-freeze
	MP3, Peaks                                          Rendition
	Loudness                                            analysis.Loudness
	Spectrum                                            analysis.Spectrum
	ToolVersion                                         string // Analyzer.ToolVersion()
	ReplayGainDB, ReplayGainPeak                        float64
	Artifacts                                           []Rendition // Kind "artifact"
	Created                                             []string    // DataDir-relative paths THIS call claimed (step 14); Discard's only input
	Lossy                                               *LossyInfo  // non-nil for IngestLossy results
}
