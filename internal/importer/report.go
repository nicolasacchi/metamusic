package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// ReportFormat is the value of Report.Format.
const ReportFormat = "mm.inventory/1"

// Issue is one warning or error. Paths are inbox-relative (or a sha256:/name:
// key for non-UTF-8 names); messages never contain reference or recipe values.
type Issue struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Summary holds the counts of C16.7.
type Summary struct {
	Files                 int            `json:"files"`
	Bytes                 int64          `json:"bytes"`
	ByKind                map[string]int `json:"by_kind"`
	RecipesValid          int            `json:"recipes_valid"`
	RecipesUnique         int            `json:"recipes_unique"`
	RecipeSources         int            `json:"recipe_sources"`
	RecipeLinesInvalid    int            `json:"recipe_lines_invalid"`
	RecipesAlreadyImprted int            `json:"recipes_already_imported"`
	AudioFiles            int            `json:"audio_files"`
	AudioIdentities       int            `json:"audio_identities"`
	AudioMatched          int            `json:"audio_matched"`
	AudioAmbiguous        int            `json:"audio_ambiguous"`
	AudioUnmatched        int            `json:"audio_unmatched"`
	AudioSkipped          int            `json:"audio_skipped"`
	AudioAlreadyImported  int            `json:"audio_already_imported"`
	AudioInvalid          int            `json:"audio_invalid"`
	DuplicateGroups       int            `json:"duplicate_groups"`
	TracksNew             int            `json:"tracks_new"`
	DraftsNew             int            `json:"drafts_new"`
	DraftsFulfilled       int            `json:"drafts_fulfilled"`
	SiblingsInferred      int            `json:"siblings_inferred"`
	ScoresAttached        int            `json:"scores_attached"`
	RefsAttached          int            `json:"refs_attached"`
	Collections           int            `json:"collections"`
	Ignored               int            `json:"ignored"`
	Unsettled             int            `json:"unsettled"`
	Warnings              int            `json:"warnings"`
	Errors                int            `json:"errors"`
	LibraryOrphans        int            `json:"library_orphans"`
}

// SourceReport describes one recipe source file.
type SourceReport struct {
	Path     string        `json:"path"`
	Kind     string        `json:"kind"`
	Lines    int           `json:"lines"`
	Valid    int           `json:"valid"`
	Invalid  []InvalidLine `json:"invalid"`
	Warnings []string      `json:"warnings"`
}

// InvalidLine is a rejected recipe line (code only, never field values).
type InvalidLine struct {
	Line int    `json:"line"`
	Code string `json:"code"`
}

// RecipeReport is one unique recipe.
type RecipeReport struct {
	RequestID    string   `json:"request_id"`
	Seed         int64    `json:"seed"`
	Hash         string   `json:"hash"`
	Sources      []string `json:"sources"`
	Placement    string   `json:"placement"`
	GroupKey     string   `json:"group_key"`
	Collections  []string `json:"collections"`
	HasReference bool     `json:"has_reference"`
}

// MatchReport is the rule that matched an audio identity to a recipe.
type MatchReport struct {
	Rule       string `json:"rule"`
	RequestID  string `json:"request_id"`
	RecipeHash string `json:"recipe_hash"`
}

// ParentReport is a proposed lineage edge.
type ParentReport struct {
	Rule string `json:"rule"`
	Of   string `json:"of"`
}

// AudioReport is one FLAC file.
type AudioReport struct {
	Path       string        `json:"path"`
	SHA256     string        `json:"sha256,omitempty"`
	AudioID    string        `json:"audio_id,omitempty"`
	Bytes      int64         `json:"bytes"`
	DurationS  float64       `json:"duration_s,omitempty"`
	SampleRate int           `json:"sample_rate,omitempty"`
	Bits       int           `json:"bits,omitempty"`
	Channels   int           `json:"channels,omitempty"`
	Status     string        `json:"status"`
	Match      *MatchReport  `json:"match,omitempty"`
	Candidates []string      `json:"candidates"`
	Title      string        `json:"title,omitempty"`
	Parent     *ParentReport `json:"parent,omitempty"`
	Score      string        `json:"score,omitempty"`
	Artifacts  []string      `json:"artifacts"`
	Sidecars   []string      `json:"sidecars"`
}

// DupReport is a duplicate_audio group.
type DupReport struct {
	AudioID        string   `json:"audio_id"`
	Paths          []string `json:"paths"`
	Representative string   `json:"representative"`
	Rule           string   `json:"rule"`
}

// DraftReport is a draft the plan creates.
type DraftReport struct {
	RequestID string  `json:"request_id"`
	Seed      int64   `json:"seed"`
	Origin    string  `json:"origin"`
	GroupKey  string  `json:"group_key"`
	Mother    *string `json:"mother"`
}

// MetaReport describes a reference index or collection file.
type MetaReport struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Rows      int    `json:"rows"`
	Matched   int    `json:"matched"`
	Unmatched int    `json:"unmatched"`
}

// IgnoredReport is a file the importer skipped.
type IgnoredReport struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Bytes  int64  `json:"bytes"`
}

// TakeResult is the outcome of one planned take (applied reports only).
type TakeResult struct {
	Path    string `json:"path"`
	AudioID string `json:"audio_id"`
	TrackID string `json:"track_id,omitempty"`
	Status  string `json:"status"` // imported | failed
	Code    string `json:"code,omitempty"`
}

// ApplyResult is appended to the report of an applied run.
type ApplyResult struct {
	TracksImported int          `json:"tracks_imported"`
	TracksFailed   int          `json:"tracks_failed"`
	DraftsCreated  int          `json:"drafts_created"`
	MothersLinked  int          `json:"mothers_linked"`
	Takes          []TakeResult `json:"takes"`
}

// Report is the JSON inventory (format mm.inventory/1).
type Report struct {
	Format         string          `json:"format"`
	ReportID       string          `json:"report_id"`
	GeneratedAt    string          `json:"generated_at"`
	Inbox          string          `json:"inbox"`
	DryRun         bool            `json:"dry_run"`
	PlanDigest     string          `json:"plan_digest"`
	Summary        Summary         `json:"summary"`
	RecipeSources  []SourceReport  `json:"recipe_sources"`
	Recipes        []RecipeReport  `json:"recipes"`
	Audio          []AudioReport   `json:"audio"`
	Duplicates     []DupReport     `json:"duplicates"`
	Drafts         []DraftReport   `json:"drafts"`
	Metadata       []MetaReport    `json:"metadata"`
	Ignored        []IgnoredReport `json:"ignored"`
	Warnings       []Issue         `json:"warnings"`
	Errors         []Issue         `json:"errors"`
	LibraryOrphans []string        `json:"library_orphans"`
	PlanEntries    []string        `json:"plan_entries,omitempty"`
	Result         *ApplyResult    `json:"result,omitempty"`
}

// DryRunFiles names the files WriteDryRun created (DataDir-relative).
type DryRunFiles struct {
	Report      string
	MappingTodo string
}

const (
	importDir  = "import"
	reportsDir = "import/reports"
	todoName   = "import/mapping.todo.csv"
)

// reportFileName builds <yyyymmddThhmmssZ>-<report_id>[-dry-run].json.
func reportFileName(at time.Time, id string, dry bool) string {
	n := at.UTC().Format("20060102T150405Z") + "-" + id
	if dry {
		n += "-dry-run"
	}
	return n + ".json"
}

// writeReportFile creates the report with O_EXCL, mode 0600, under
// import/reports/. It returns the DataDir-relative path.
func (im *Importer) writeReportFile(rep *Report, at time.Time) (string, error) {
	if im.data == nil {
		return "", errors.New("importer: no data root")
	}
	if err := im.data.MkdirAll(reportsDir, 0o700); err != nil {
		return "", fmt.Errorf("importer: create %s: %w", reportsDir, err)
	}
	if err := im.data.Chmod(importDir, 0o700); err != nil {
		return "", fmt.Errorf("importer: chmod %s: %w", importDir, err)
	}
	b, err := json.MarshalIndent(rep, "", " ")
	if err != nil {
		return "", err
	}
	rel := path.Join(reportsDir, reportFileName(at, rep.ReportID, rep.DryRun))
	f, err := im.data.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("importer: create report: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return rel, nil
}

// WriteDryRun writes the dry-run report and mapping.todo.csv (rows only when
// something needs the operator; see Inventory.NeedsMapping), both under
// <DataDir>/import/. Nothing else is touched.
func (im *Importer) WriteDryRun(inv *Inventory) (DryRunFiles, error) {
	var out DryRunFiles
	rel, err := im.writeReportFile(&inv.Report, im.now())
	if err != nil {
		return out, err
	}
	out.Report = rel
	f, err := im.data.OpenFile(todoName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return out, fmt.Errorf("importer: create mapping template: %w", err)
	}
	if err := WriteMappingTemplate(f, inv); err != nil {
		_ = f.Close()
		return out, err
	}
	if err := f.Close(); err != nil {
		return out, err
	}
	out.MappingTodo = todoName
	return out, nil
}

// libraryOrphans lists files under audio/masters and derived (except
// derived/probe) that no track or rendition references.
func (im *Importer) libraryOrphans(masters, derived map[string]bool) []string {
	out := []string{}
	if im.data == nil {
		return out
	}
	fsys := im.data.FS()
	for _, top := range []string{"audio/masters", "derived"} {
		_ = fs.WalkDir(fsys, top, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // a missing or unreadable tree has no orphans to report
			}
			if d.IsDir() {
				if p == "derived/probe" {
					return fs.SkipDir
				}
				return nil
			}
			if !masters[p] && !derived[p] {
				out = append(out, p)
			}
			return nil
		})
	}
	sort.Strings(out)
	return out
}

// issues collects warnings and errors in discovery order.
type issues struct{ warnings, errors []Issue }

func (s *issues) warn(p string, line int, code, msg string) {
	s.warnings = append(s.warnings, Issue{Path: p, Line: line, Code: code, Message: msg})
}

func (s *issues) fail(p string, line int, code, msg string) {
	s.errors = append(s.errors, Issue{Path: p, Line: line, Code: code, Message: msg})
}

// FormatText renders the summary as aligned text for the CLI. Every path is
// escaped (C16.7).
func (r *Report) FormatText(w io.Writer) {
	s := r.Summary
	rows := []struct {
		k string
		v any
	}{
		{"files", s.Files}, {"recipes (unique)", s.RecipesUnique}, {"recipe sources", s.RecipeSources},
		{"invalid recipe lines", s.RecipeLinesInvalid}, {"recipes already imported", s.RecipesAlreadyImprted},
		{"audio files", s.AudioFiles}, {"audio identities", s.AudioIdentities},
		{"duplicate groups", s.DuplicateGroups}, {"tracks to import", s.TracksNew},
		{"audio already imported", s.AudioAlreadyImported}, {"audio unmatched", s.AudioUnmatched},
		{"audio ambiguous", s.AudioAmbiguous}, {"audio skipped", s.AudioSkipped}, {"audio invalid", s.AudioInvalid},
		{"drafts to create", s.DraftsNew}, {"drafts to fulfil", s.DraftsFulfilled},
		{"sibling edges", s.SiblingsInferred}, {"scores attached", s.ScoresAttached},
		{"references attached", s.RefsAttached}, {"collections", s.Collections},
		{"ignored", s.Ignored}, {"unsettled", s.Unsettled}, {"warnings", s.Warnings}, {"errors", s.Errors},
		{"library orphans", s.LibraryOrphans},
	}
	for _, x := range rows {
		fmt.Fprintf(w, "%-26s %v\n", x.k, x.v)
	}
	for _, st := range []string{"unmatched", "ambiguous"} {
		var list []string
		for _, a := range r.Audio {
			if a.Status == st {
				c := ""
				if len(a.Candidates) > 0 {
					c = "  (candidates: " + Escape(strings.Join(a.Candidates, ", ")) + ")"
				}
				list = append(list, "  "+Escape(a.Path)+c)
			}
		}
		if len(list) > 0 {
			fmt.Fprintf(w, "\n%s audio:\n%s\n", st, strings.Join(list, "\n"))
		}
	}
	for _, e := range r.Errors {
		fmt.Fprintf(w, "error: %s:%d %s\n", Escape(e.Path), e.Line, e.Code)
	}
}
