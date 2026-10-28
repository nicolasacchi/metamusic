package app

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/albums"
	"github.com/nicolasacchi/metamusic/internal/config"
	"github.com/nicolasacchi/metamusic/internal/crates"
	"github.com/nicolasacchi/metamusic/internal/curation"
	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/cover"
	mediaserve "github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/muses"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/recordview"
	"github.com/nicolasacchi/metamusic/internal/songs"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// L2Options tunes the curation services of Build (C118); every field is optional. Production leaves it zero.
type L2Options struct {
	// Log is the Syncer's and the tape runner's logger; nil => slog.Default().
	Log *slog.Logger
	// Owner identifies this process in the tape runner's lease; "" => the process owner of L1.
	Owner string
	// Pipeline replaces the production tape pipeline (test seam: tapetest.FakePipeline); nil => tape.NewPipeline.
	Pipeline tape.Pipeline
	// Loudness replaces the Ears adapter of the tape plan; nil => the adapter over Ears.
	Loudness tape.Loudness
	// FlacVersion overrides the first line of `flac --version`, which Build otherwise computes once; the empty string
	// means the encoder is missing and tape export answers 503 {component: flac}. Nil => compute. Rigs pass a constant so
	// no rig spawns a process.
	FlacVersion *string
}

// l2Services are the L2 services Build constructs.
type l2Services struct {
	Records    *records.Service
	Syncer     *records.Syncer
	RecordView *recordview.Service
	Crates     *crates.Service
	Tapes      *tape.Service
	Curation   *curation.Service
	Albums     *albums.Service
	// Resolvers are appended to the media chain after M4a's (the tape resolver, C128.7).
	Resolvers []mediaserve.Resolver
}

// l2Inputs is what the lane wiring files draw on.
type l2Inputs struct {
	cfg     config.Config
	st      *store.Store
	data    *os.Root
	lib     *library.Service
	signer  *mediaserve.Signer
	covers  *cover.Service
	muses   *muses.Service
	songs   *songs.Service
	ears    *ears.Service
	version string
	now     func() time.Time
	newID   func() string
}

// buildL2 constructs the L2 services in dependency order and returns them; each lane file wires its own part and may only
// change its own file (C118 hot-spot rules). The Syncer and the tape runner are built here and started by serve only.
func buildL2(in l2Inputs, o L2Options) *l2Services {
	if o.Owner == "" {
		o.Owner = processOwner()
	}
	s := &l2Services{}
	buildL2Curation(in, s) // the flags every other part reads
	buildL2Records(in, s, o)
	buildL2Catalog(in, s) // the Subsonic catalog reads Records and the flags
	buildL2RecordView(in, s)
	buildL2Crates(in, s)
	buildL2Tape(in, s, o)
	return s
}

// flacVersion is the first line of `flac --version`, or "" when the encoder is not on the path or does not answer.
func flacVersion(ctx context.Context, bin string) string {
	if _, err := exec.LookPath(bin); err != nil {
		return ""
	}
	out, err := mediatool.Run(ctx, bin, []string{"--version"}, mediatool.RunOpts{Timeout: 5 * time.Second, MaxStdout: 4096})
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(first)
}
