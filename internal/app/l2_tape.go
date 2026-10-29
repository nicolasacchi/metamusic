package app

import (
	"context"

	"github.com/nicolasacchi/metamusic/internal/ears"
	"github.com/nicolasacchi/metamusic/internal/mediatool"
	"github.com/nicolasacchi/metamusic/internal/recordview"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// earsLoudness adapts Ears to tape.Loudness: it analyses the Takes whose loudness is missing (Ears runs only the missing
// analyzers) and reads the integrated loudness and true peak back. A Take that cannot be analysed, or whose loudness stays
// empty, is left out of the map, which the plan renders as unmeasured (gain 0, never an error).
type earsLoudness struct{ ears *ears.Service }

func (l earsLoudness) Ensure(ctx context.Context, takeIDs []string) (map[string]tape.Measure, error) {
	out := make(map[string]tape.Measure, len(takeIDs))
	if l.ears == nil {
		return out, nil
	}
	for _, id := range takeIDs {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		p, err := l.ears.Perception(ctx, id)
		if err != nil || p.LUFSI == nil || p.TruePeakDB == nil {
			if _, aerr := l.ears.AnalyzeTrack(ctx, id, ears.AnalyzeOptions{}); aerr != nil {
				continue
			}
			if p, err = l.ears.Perception(ctx, id); err != nil {
				continue
			}
		}
		if p.LUFSI != nil && p.TruePeakDB != nil {
			out[id] = tape.Measure{LUFS: *p.LUFSI, TruePeakDBTP: *p.TruePeakDB, OK: true}
		}
	}
	return out, nil
}

// tapeBriefsOf is the late-bound view of the tape service recordview lists tapes through.
func tapeBriefsOf(s *l2Services) recordview.TapeBriefs { return &lateTapeBriefs{s: s} }

type lateTapeBriefs struct{ s *l2Services }

func (l *lateTapeBriefs) Brief(ctx context.Context, recordID string, limit int) ([]tape.Brief, error) {
	if l.s.Tapes == nil {
		return nil, nil
	}
	return l.s.Tapes.Brief(ctx, recordID, limit)
}

// buildL2Tape wires the tape service, its pipeline and its resolver.
func buildL2Tape(in l2Inputs, s *l2Services, o L2Options) {
	pipe := o.Pipeline
	if pipe == nil {
		pipe = tape.NewPipeline(tape.PipelineOptions{
			Tools: mediatool.Tools{FFmpeg: in.cfg.FFmpeg, FLAC: in.cfg.FLAC, Metaflac: in.cfg.Metaflac}, Data: in.data, DataDir: in.cfg.DataDir,
			Log: o.Log, Version: in.version,
		})
	}
	var loud tape.Loudness = earsLoudness{ears: in.ears}
	if o.Loudness != nil {
		loud = o.Loudness
	}
	flac := ""
	if o.FlacVersion != nil {
		flac = *o.FlacVersion
	} else {
		flac = flacVersion(context.Background(), in.cfg.FLAC)
	}
	s.Tapes = tape.New(tape.Options{
		Store: in.st, Tools: mediatool.Tools{FFmpeg: in.cfg.FFmpeg, FLAC: in.cfg.FLAC, Metaflac: in.cfg.Metaflac}, Source: s.RecordView, Loudness: loud,
		Covers: in.covers, Data: in.data, Pipeline: pipe, Signer: in.signer, Now: in.now, NewID: in.newID, Owner: o.Owner, Log: o.Log,
		FlacVersion: flac, BuildVersion: in.version,
	})
	s.Resolvers = append(s.Resolvers, s.Tapes)
}
