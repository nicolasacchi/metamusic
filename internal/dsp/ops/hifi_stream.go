package ops

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"

	"github.com/nicolasacchi/metamusic/internal/dsp/safety"
)

// SelfStreamFunc is a self-finishing kind (master, widen, binaural) that renders and finishes without holding its output. It does
// what the kind's RunFunc does, with the same samples and the same report, but the render goes block by block into a scratch spool,
// safety.FinishStream reads it and delivers the finished signal to sink in order, and the numbers the report takes from the finished
// signal are measured on the delivered blocks. Memory is the input and a few blocks; the output and the finish's working signals are
// on disk. The results are the rate and length of the signal and the operator's report. RunTo uses it when the job has a Scratch.
type SelfStreamFunc func(ctx context.Context, in RunIn, sc safety.Scratch, sink func(l, r []float64) error) (rate, frames int, report json.RawMessage, err error)

// selfStreamers are the self-finishing kinds with a streamed finish. A table like streamers and the registry.
var selfStreamers = map[string]SelfStreamFunc{KindMaster: streamMaster, KindWiden: streamWiden, KindBinaural: streamBinaural}

// renderAndFinish is the shared body of the self-finishing streams. render writes the pre-finish signal to the writer it is given,
// block by block, and returns its length; the result is finished by safety.FinishStream with fin, and every finished block goes to
// observe (when set) and then to sink. The spool is released on every path.
func renderAndFinish(ctx context.Context, sc safety.Scratch, rate int, fin safety.FinishIn, sink func(l, r []float64) error,
	render func(w func(l, r []float64) error) (frames int, err error), observe func(l, r []float64)) (safety.FinishReport, error) {
	raw, err := sc.NewSpool()
	if err != nil {
		return safety.FinishReport{}, err
	}
	defer func() { _ = raw.Close() }()
	frames, err := render(func(l, r []float64) error { return safety.PutFrames(raw, l, r) })
	if err != nil {
		return safety.FinishReport{}, err
	}
	return safety.FinishStream(ctx, safety.SpoolSource(rate, frames, raw), fin, sc, func(l, r []float64) error {
		if observe != nil {
			observe(l, r)
		}
		return sink(l, r)
	})
}

// putF64 writes v as little-endian float64 values: the format of a channel spool.
func putF64(w io.Writer, v []float64) error {
	buf := make([]byte, 8*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint64(buf[8*i:], math.Float64bits(x))
	}
	_, err := w.Write(buf)
	return err
}

// getF64 fills dst from a channel spool written by putF64.
func getF64(r io.Reader, dst []float64) error {
	buf := make([]byte, 8*len(dst))
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	for i := range dst {
		dst[i] = math.Float64frombits(binary.LittleEndian.Uint64(buf[8*i:]))
	}
	return nil
}

// heapScratch is a Scratch whose spools live in memory: what the in-memory entry points (Run, Ensemble) use when they share a
// streamed body with RunTo. A spool is read after it is written, as the interface says.
type heapScratch struct{}

type heapSpool struct{ buf bytes.Buffer }

func (heapScratch) NewSpool() (safety.Spool, error) { return &heapSpool{}, nil }
func (s *heapSpool) Write(p []byte) (int, error)    { return s.buf.Write(p) }
func (s *heapSpool) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.buf.Bytes())), nil
}
func (s *heapSpool) Close() error { s.buf = bytes.Buffer{}; return nil }
