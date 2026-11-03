// Package ops holds the transform operators. This file is the registration seam of C225.5: every kind registers itself from
// its own file in init(), and the dispatcher (D5's run.go) looks kinds up only through Lookup, never through a switch, so a
// later kind (master, widen, binaural) edits no frozen or shared file.
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/pcm"
)

// RunIn is what an operator receives: the decoded inputs, the resolved parameters from job.json, the seed, the target
// loudness and the work meter.
type RunIn struct {
	Rate       int
	Inputs     []pcm.Buffer
	Params     json.RawMessage
	Alignment  json.RawMessage
	Seed       int64
	TargetLUFS float64
	RefLUFS    float64
	Meter      *dsp.Meter
}

// RunOut is an operator's result: the buffer before the child writes it, and the kind-specific report object that goes into
// transform.json.
type RunOut struct {
	Buffer pcm.Buffer
	Report json.RawMessage
}

// RunFunc renders one kind. It is single-goroutine, reads no clock and returns dsp.ErrBudget when the meter says so.
type RunFunc func(ctx context.Context, in RunIn) (RunOut, error)

// UnitsFunc is the work-unit model of the kind: the units Run will spend for these input and output frames.
type UnitsFunc func(inFrames, outFrames int64, rate int, params json.RawMessage) int64

type entry struct {
	run   RunFunc
	units UnitsFunc
}

var (
	mu  sync.Mutex
	reg = map[string]entry{}
)

// Register adds a kind at init time. It panics on an empty kind, a nil function or a duplicate.
func Register(kind string, fn RunFunc, units UnitsFunc) {
	mu.Lock()
	defer mu.Unlock()
	if kind == "" || fn == nil || units == nil {
		panic(fmt.Sprintf("ops: Register(%q) needs a kind, a run function and a units function", kind))
	}
	if _, dup := reg[kind]; dup {
		panic(fmt.Sprintf("ops: kind %q registered twice", kind))
	}
	reg[kind] = entry{run: fn, units: units}
}

// Lookup returns the run and units functions of kind.
func Lookup(kind string) (RunFunc, UnitsFunc, bool) {
	mu.Lock()
	defer mu.Unlock()
	e, ok := reg[kind]
	return e.run, e.units, ok
}

// Kinds returns the registered kinds, sorted by name: the determinism and property suites iterate it, so a new kind is
// covered by registering.
func Kinds() []string {
	mu.Lock()
	defer mu.Unlock()
	out := make([]string, 0, len(reg))
	for k := range reg {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
