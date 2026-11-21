package ears

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/nicolasacchi/metamusic/internal/ears/tierb"
	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
)

// Tier B analyzers (M5b C337.1) and their current definitions. A stored row is current when its version starts with
// "<definition> " (D-M2b-12); a definition bump makes the sweep recompute the row from the stored artifact (D-EARS-9).
const (
	anTierBRaw       = "tier_b_raw"
	anTierBDiction   = "tier_b_diction"
	anTierBAdherence = "tier_b_adherence"

	tierBRawDef       = "mm-tierb-raw/1"
	tierBDictionDef   = "mm-diction/1"
	tierBAdherenceDef = "mm-adherence/1"
	tierBGoTag        = "go1"
)

// Tier B states of a take's listening check (C337.4).
const (
	TierBNone    = "none"
	TierBPending = "pending"
	TierBOK      = "ok"
	TierBError   = "error"
	TierBSkipped = "skipped"
	TierBMissing = "missing"
)

// Measured-section outcomes recorded in the diction row.
const (
	measuredWritten = "written" // this analysis wrote the track's measured sections
	measuredFailed  = "failed"  // the alignment was too weak: no measured sections, flag alignment_failed
	measuredKept    = "kept"    // the track already had measured sections
	measuredNone    = "none"    // nothing to place (no lyric lines, or no diction)
)

// ErrNoTierBDocument is returned by AnalyzeTierB for a track that has no analysis*.json artifact.
var ErrNoTierBDocument = errors.New("ears: no tier B document for this track")

// ErrBadTierBDocument is returned by AnalyzeTierB when the stored document does not parse or no longer matches its row.
var ErrBadTierBDocument = errors.New("ears: tier B document is not valid")

// TierBOutcome reports one AnalyzeTierB call.
type TierBOutcome struct {
	TrackID  string
	Status   string   // the document's status: ok | error
	Wrote    []string // analyzers whose row this call inserted (a row that already existed is not listed)
	Measured string   // written | failed | kept | none
	Sections int      // measured sections inserted
}

// tierBRawDoc is the metrics_json of a tier_b_raw row: everything of the document except the words and the pitch contour.
type tierBRawDoc struct {
	Status    string          `json:"status"`
	Code      *string         `json:"code"`
	Stages    tierb.Stages    `json:"stages"`
	Models    tierb.Models    `json:"models"`
	Timings   tierb.Timings   `json:"timings"`
	Resources tierb.Resources `json:"resources"`
	Language  string          `json:"language"`
	Forced    bool            `json:"forced"`
	Counts    tierBCounts     `json:"counts"`
	Artifact  TierBArtifact   `json:"artifact"`
}

// tierBCounts are the sizes the flags and the read surface need without the words.
type tierBCounts struct {
	Words    int `json:"words"`
	Segments int `json:"segments"`
	RefWords int `json:"ref_words"`
}

// TierBArtifact names the stored document a result was computed from.
type TierBArtifact struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// storedSection is one section of the tier_b_diction row: the summary plus its lines with their word outcomes.
type storedSection struct {
	TierBSection
	Lines []tierb.LineResult `json:"lines"`
}

// tierBMeasuredNote records what the analysis did about measured sections.
type tierBMeasuredNote struct {
	Status   string `json:"status"`
	Sections int    `json:"sections"`
}

// tierBDictionDoc is the metrics_json of a tier_b_diction row. The path $.overall.cer is frozen (C346.3).
type tierBDictionDoc struct {
	Overall  TierBDictionOverall `json:"overall"`
	Sections []storedSection     `json:"sections"`
	Mangled  []tierb.Mangled     `json:"mangled"`
	Measured tierBMeasuredNote   `json:"measured"`
}

func round3(v float64) float64 { return r3(v) }

func dictionDocOf(res tierb.DictionResult, secs []yue2.Section) tierBDictionDoc {
	label := make(map[int]string, len(secs))
	for _, s := range secs {
		label[s.Idx] = s.Label
	}
	d := tierBDictionDoc{
		Overall: TierBDictionOverall{CER: round3(res.CER), WER: round3(res.WER), RefRunes: res.RefRunes, CharEdits: res.CharEdits,
			RefWords: res.RefWords, WordEdits: res.WordEdits},
		Sections: make([]storedSection, 0, len(res.Sections)),
		Mangled:  res.Mangled,
	}
	for _, s := range res.Sections {
		d.Sections = append(d.Sections, storedSection{
			TierBSection: TierBSection{Section: s.Section, Label: label[s.Section], CER: round3(s.CER), WER: round3(s.WER),
				RefWords: s.RefWords, WordEdits: s.WordEdits, LineCount: len(s.Lines)},
			Lines: s.Lines,
		})
	}
	return d
}

// rawVersion is "mm-tierb-raw/1 <asr model>@<sha12>": a different model gives a different row.
func rawVersion(r tierb.Raw) string {
	m := "none"
	if r.Models.ASR != nil {
		sha := r.Models.ASR.SHA256
		m = r.Models.ASR.Name + "@" + sha[:min(12, len(sha))]
	}
	return tierBRawDef + " " + m
}

// readTierBDocument returns the newest analysis*.json artifact of the track with its bytes, checked against its row.
func (s *Service) readTierBDocument(ctx context.Context, trackID string) (library.ArtifactRef, []byte, error) {
	var a library.ArtifactRef
	err := s.o.Store.R.QueryRowContext(ctx, `SELECT name, path, sha256, bytes FROM artifacts WHERE track_id = ? AND name LIKE 'analysis%.json'
		ORDER BY created_at DESC, name DESC LIMIT 1`, trackID).Scan(&a.Name, &a.Path, &a.SHA256, &a.Bytes)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return a, nil, ErrNoTierBDocument
	case err != nil:
		return a, nil, fmt.Errorf("ears: tier B artifact: %w", err)
	}
	if a.Bytes > tierb.MaxDocBytes {
		return a, nil, fmt.Errorf("%w: %d bytes", ErrBadTierBDocument, a.Bytes)
	}
	f, err := s.o.Data.Open(a.Path)
	if err != nil {
		return a, nil, fmt.Errorf("ears: tier B artifact: %w", err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, tierb.MaxDocBytes+1))
	if err != nil {
		return a, nil, fmt.Errorf("ears: tier B artifact: %w", err)
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != a.SHA256 {
		return a, nil, fmt.Errorf("%w: %s does not match its stored sha256", ErrBadTierBDocument, a.Name)
	}
	return a, b, nil
}

// AnalyzeTierB computes and stores the listening check of one track from its stored analysis*.json (C337.1). It writes, in one
// transaction and with ON CONFLICT DO NOTHING, the tier_b_raw row, the tier_b_diction row (only for a document with status ok
// and a recipe with lyric lines) and, once per track, the measured sections. A document with status error writes the raw row
// only. It never creates GPU work and is idempotent. GET paths never call it.
func (s *Service) AnalyzeTierB(ctx context.Context, trackID string) (TierBOutcome, error) {
	out := TierBOutcome{TrackID: trackID, Measured: measuredNone}
	if err := s.wired(); err != nil {
		return out, err
	}
	var lyrics string
	switch err := s.o.Store.R.QueryRowContext(ctx, `SELECT r.lyrics FROM tracks t JOIN recipes r ON r.id = t.recipe_id WHERE t.id = ?`, trackID).Scan(&lyrics); {
	case errors.Is(err, sql.ErrNoRows):
		return out, library.ErrNotFound
	case err != nil:
		return out, fmt.Errorf("ears: tier B lyrics: %w", err)
	}
	art, doc, err := s.readTierBDocument(ctx, trackID)
	if err != nil {
		return out, err
	}
	raw, err := tierb.Parse(doc)
	if err != nil {
		return out, fmt.Errorf("%w: %s: %w", ErrBadTierBDocument, art.Name, err)
	}
	out.Status = raw.Status
	secs := yue2.LyricsSections(lyrics)
	ref := tierb.RefWords(lyrics)

	rd := tierBRawDoc{Status: raw.Status, Code: raw.Code, Stages: raw.Stages, Models: raw.Models, Timings: raw.Timings,
		Resources: raw.Resources, Counts: tierBCounts{RefWords: len(ref)}, Artifact: TierBArtifact{SHA256: art.SHA256, Bytes: art.Bytes}}
	if raw.ASR != nil {
		rd.Language, rd.Forced = raw.ASR.Language, raw.ASR.Forced
		rd.Counts.Words, rd.Counts.Segments = len(raw.ASR.Words), len(raw.ASR.Segments)
	}

	var diction *tierBDictionDoc
	var measured []tierb.Measured
	var aligned bool
	if raw.Status == tierb.StatusOK && len(ref) > 0 {
		dd := dictionDocOf(tierb.Diction(ref, raw), secs)
		diction = &dd
		measured, aligned = tierb.MeasuredSections(ref, secs, tierb.AlignRaw(ref, raw), raw)
	}

	tx, err := s.o.Store.W.BeginTx(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("ears: tier B: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	insert := func(analyzer, version string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO analyses(track_id, analyzer, version, tier, metrics_json, created_at) VALUES (?, ?, ?, 'B', ?, ?)
			ON CONFLICT DO NOTHING`, trackID, analyzer, version, string(b), s.now())
		if err != nil {
			return fmt.Errorf("ears: tier B insert %s: %w", analyzer, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			out.Wrote = append(out.Wrote, analyzer)
		}
		return nil
	}
	if err := insert(anTierBRaw, rawVersion(raw), rd); err != nil {
		return out, err
	}
	if diction != nil {
		switch {
		case !aligned:
			diction.Measured = tierBMeasuredNote{Status: measuredFailed}
		default:
			var have int
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sections WHERE track_id = ? AND source = 'measured')`, trackID).Scan(&have); err != nil {
				return out, fmt.Errorf("ears: tier B sections: %w", err)
			}
			if have == 1 {
				diction.Measured = tierBMeasuredNote{Status: measuredKept}
				break
			}
			ns := make([]library.NewSection, 0, len(measured))
			for _, m := range measured {
				ns = append(ns, library.NewSection{Idx: m.Idx, Kind: m.Kind, Label: m.Label, Raw: m.Raw, Lines: m.Lines,
					StartS: m.StartS, EndS: m.EndS, Confidence: m.Confidence})
			}
			n, err := s.o.Library.AddMeasuredSections(ctx, tx, trackID, ns)
			if err != nil {
				return out, err
			}
			diction.Measured = tierBMeasuredNote{Status: measuredWritten, Sections: n}
		}
		out.Measured, out.Sections = diction.Measured.Status, diction.Measured.Sections
		if err := insert(anTierBDiction, tierBDictionDef+" "+tierBGoTag, diction); err != nil {
			return out, err
		}
	}
	if err := tx.Commit(); err != nil {
		return out, fmt.Errorf("ears: tier B commit: %w", err)
	}
	return out, nil
}
