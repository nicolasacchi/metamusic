package recordview

import (
	"context"
	"fmt"

	"github.com/nicolasacchi/metamusic/internal/liner"
	"github.com/nicolasacchi/metamusic/internal/media/flacmeta"
	"github.com/nicolasacchi/metamusic/internal/records"
	"github.com/nicolasacchi/metamusic/internal/tape"
)

// lossySuffix is appended to the chapter name of a lossy source, so a lossy item is never presented as lossless (V6).
const lossySuffix = " (lossy source)"

// chapterName is the name a tape chapter carries for an item.
func chapterName(it tape.Item) string {
	if it.Lossy {
		return it.Title + lossySuffix
	}
	return it.Title
}

// snapshotFrom builds the tape input from loaded data. It reads Muse text only when curation.export_muse_names is on.
func (s *Service) snapshotFrom(ctx context.Context, d *data, includeLiner bool) (tape.Snapshot, error) {
	museNames := false
	if s.o.Curation != nil {
		var err error
		if museNames, err = s.o.Curation.ExportMuseNames(ctx); err != nil {
			return tape.Snapshot{}, fmt.Errorf("recordview: settings: %w", err)
		}
	}
	snap := tape.Snapshot{RecordID: d.rec.ID, Revision: d.rec.Revision, Status: string(d.rec.Status), Title: d.rec.Title, MuseNames: museNames,
		CoverInput: coverInput(d.rec, firstSongs(d.rs), d.briefs), Items: []tape.Item{}, Warnings: []tape.SlotWarning{}}
	for _, r := range d.rs {
		for _, w := range slotWarnings(d.rec, r) {
			if w == records.WarnPinIneligible || w == records.WarnUndecidedPick {
				snap.Warnings = append(snap.Warnings, tape.SlotWarning{SlotID: r.Slot.ID, Code: w})
			}
		}
		if !playable(r) {
			continue
		}
		ref, f := d.refs[r.Take.ID], d.facts[r.Take.ID]
		snap.Items = append(snap.Items, tape.Item{SlotID: r.Slot.ID, Position: r.Slot.Position, TakeID: r.Take.ID, Title: ref.Title,
			MasterRel: ref.MasterPath, MasterSHA256: ref.MasterSHA256, MasterBytes: f.Bytes, SampleRate: ref.SampleRate, Bits: f.Bits,
			Channels: ref.Channels, Lossy: r.Take.Lossy, DurationS: r.Take.DurationS, Measure: d.loud[r.Take.ID].Measure})
	}
	in, err := s.linerInput(ctx, d, liner.AudienceExport, museNames)
	if err != nil {
		return tape.Snapshot{}, err
	}
	l := liner.Build(in)
	snap.Tags = []flacmeta.Comment{{Key: "TITLE", Value: d.rec.Title}, {Key: "ALBUM", Value: d.rec.Title}, {Key: "ARTIST", Value: "metamusic"},
		{Key: "DATE", Value: linerDate(d.rec)}}
	if len(l.Summary.Families) > 0 {
		snap.Tags = append(snap.Tags, flacmeta.Comment{Key: "GENRE", Value: joinComma(l.Summary.Families)})
	}
	if d.rec.Subtitle != "" {
		snap.Tags = append(snap.Tags, flacmeta.Comment{Key: "SUBTITLE", Value: d.rec.Subtitle})
	}
	if includeLiner {
		snap.Description = l.Description()
		snap.Tags = append(snap.Tags, flacmeta.Comment{Key: "LINER_NOTES", Value: l.Markdown()})
	}
	return snap, nil
}

func joinComma(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// Snapshot builds the tape input: resolved Takes and masters (titles read now), slot warnings, loudness, the export-audience
// liner, the tag set and the cover input. It reads Muse text only when curation.export_muse_names is on.
func (s *Service) Snapshot(ctx context.Context, recordID string, o tape.SnapOptions) (tape.Snapshot, error) {
	rec, err := s.o.Records.Get(ctx, recordID)
	if err != nil {
		return tape.Snapshot{}, err
	}
	rs, err := s.o.Records.Resolved(ctx, recordID)
	if err != nil {
		return tape.Snapshot{}, err
	}
	d, err := s.load(ctx, rec, rs, nil)
	if err != nil {
		return tape.Snapshot{}, err
	}
	return s.snapshotFrom(ctx, d, o.IncludeLiner)
}

// preview is what Snapshot would build for the tape, without writing anything (D-L2-27 b).
func (s *Service) preview(ctx context.Context, d *data) (*Preview, error) {
	snap, err := s.snapshotFrom(ctx, d, true)
	if err != nil {
		return nil, err
	}
	p := &Preview{Chapters: []PreviewChapter{}, Tags: []PreviewTag{}, Description: snap.Description, Warnings: snap.Warnings}
	for _, it := range snap.Items {
		p.Chapters = append(p.Chapters, PreviewChapter{Position: it.Position, Name: chapterName(it)})
	}
	for _, t := range snap.Tags {
		p.Tags = append(p.Tags, PreviewTag{Key: t.Key, Value: t.Value})
	}
	return p, nil
}
