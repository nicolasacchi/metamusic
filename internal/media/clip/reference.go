package clip

import (
	"context"
	"errors"
)

// E2 seam (C165, D-E2-9): the reference clip renderer. A window of 2..30 s of a frozen master is cut raw (unity gain, no
// loudness match, no fades) and tiled end to end to exactly 30.000 s, so ACE-Step's own preprocessing of reference audio
// never applies and the recipe can pin the clip's bytes. It lives in the same package and cache as duel clips under a
// different key prefix. The signatures are frozen by the contract freeze (E2-F2); lane E2-CL fills the bodies.

// RefDef is the renderer definition string hashed into every reference key.
const RefDef = "mm-refclip/1"

// RefMS is the length of every reference clip: exactly 30.000 s, 1 440 000 frames at 48 kHz.
const RefMS = 30000

// Reference window bounds in milliseconds and the grid of its ends.
const (
	RefMinWindowMS = 2000
	RefMaxWindowMS = 30000
	RefGridMS      = 100
)

// ErrNotImplemented marks a seam whose body has not landed.
var ErrNotImplemented = errors.New("clip: not implemented yet")

// RefSpec is one reference clip: a window [FromMS, ToMS) of the master, 2..30 s on the 100 ms grid, inside the master.
type RefSpec struct {
	MasterSHA256, MasterPath string
	SampleRate, Channels     int
	FromMS, ToMS             int64
}

// RefObject is a rendered reference clip: the cache Object plus the content digest of its FLAC bytes, which is the
// clip_sha256 the recipe pins (C165 amended: the plan returned a bare Object, which has no content digest).
type RefObject struct {
	Object
	SHA256 string
}

// RefKey is the cache key of a reference clip: sha256 over RefDef, the master sha, the window, rate, channels, the tool
// version and the tile-rule version. Its prefix differs from Key's, so it never collides with a duel clip.
//
// STUB(E2-CL)
func RefKey(sp RefSpec, toolVersion string) string { panic("STUB(E2-CL): clip.RefKey") }

// Reference renders (or finds) the reference clip. The returned RefObject's SHA256 is the clip_sha256 pinned in the recipe.
//
// STUB(E2-CL)
func (s *Service) Reference(ctx context.Context, sp RefSpec) (RefObject, error) {
	return RefObject{}, ErrNotImplemented
}
