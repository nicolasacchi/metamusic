// Package morningtest is TEST-ONLY: a fake morning.SideBuilder that needs no media tools. It must be imported only from
// _test.go files and test rigs.
package morningtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/nicolasacchi/metamusic/internal/morning"
)

// Fake is a deterministic morning.SideBuilder. The result depends only on the side's layout (track ids, gains, positions), so
// the same plan builds the same "file". With Data set it also writes a small stand-in file (mode 0444) at the claimed path, so
// resolvers and sweeps have something to find; it never reads a master.
type Fake struct {
	// Data is the data root stand-in files are written under; nil writes nothing.
	Data *os.Root
	// Err, when set, is returned by every build after the call is recorded.
	Err error
	// Fail, when set, decides per input; a non-nil error is returned.
	Fail func(in morning.SideInput) error

	mu    sync.Mutex
	calls []morning.SideInput
}

var _ morning.SideBuilder = (*Fake)(nil)

// Calls returns a copy of the recorded inputs, oldest first.
func (f *Fake) Calls() []morning.SideInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]morning.SideInput(nil), f.calls...)
}

// BuildSide records the call and returns the deterministic result.
func (f *Fake) BuildSide(ctx context.Context, in morning.SideInput) (morning.SideResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, in)
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return morning.SideResult{}, &morning.BuildError{Code: morning.CodeInterrupted, Msg: "build cancelled", Err: err}
	}
	if f.Err != nil {
		return morning.SideResult{}, f.Err
	}
	if f.Fail != nil {
		if err := f.Fail(in); err != nil {
			return morning.SideResult{}, err
		}
	}
	var sb strings.Builder
	res := morning.SideResult{Samples: in.Side.Samples}
	for _, c := range in.Side.Chapters {
		fmt.Fprintf(&sb, "%d %s %d %d %d %d\n", c.N, c.TrackID, c.GainCDB, c.Start, c.AudioStart, c.End)
		res.Chapters = append(res.Chapters, morning.ChapterLoudness{N: c.N, LUFS: morning.TargetLUFS, TruePeak: morning.CeilingTP - 1})
	}
	sum := sha256.Sum256([]byte(sb.String()))
	res.SHA256 = hex.EncodeToString(sum[:])
	res.RelPath = path.Join("derived/morning", res.SHA256[:2], res.SHA256+".flac")
	res.ByteRatio = morning.SideByteRatio
	res.Bytes = int64(float64(in.Side.Samples) * 6 * res.ByteRatio)
	if f.Data != nil {
		if err := f.Data.MkdirAll(path.Dir(res.RelPath), 0o755); err != nil {
			return morning.SideResult{}, err
		}
		w, err := f.Data.OpenFile(res.RelPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o444)
		switch {
		case err == nil:
			_, werr := w.WriteString("fake side " + res.SHA256 + "\n")
			if cerr := w.Close(); werr != nil || cerr != nil {
				return morning.SideResult{}, fmt.Errorf("morningtest: write stand-in: %w", errors.Join(werr, cerr))
			}
		case !errors.Is(err, fs.ErrExist):
			return morning.SideResult{}, err
		}
	}
	return res, nil
}
