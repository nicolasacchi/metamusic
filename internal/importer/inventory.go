package importer

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/library"
)

// Inventory is the dry-run result: the report plus the plan Apply executes.
type Inventory struct {
	Report  Report
	b       *builder
	entries []string
	todo    []todoRow
}

type todoRow struct {
	audio, candidates string
}

// PlanDigest is the digest a reviewed dry-run binds an apply to.
func (inv *Inventory) PlanDigest() string { return inv.Report.PlanDigest }

// PlanEntries are the canonical plan entries the digest covers (for diffs).
func (inv *Inventory) PlanEntries() []string { return inv.entries }

// NeedsMapping reports whether mapping.todo.csv would list anything.
func (inv *Inventory) NeedsMapping() bool { return len(inv.todo) > 0 }

// builder holds the state of one Inventory call.
type builder struct {
	im  *Importer
	ctx context.Context
	now time.Time
	iss issues
	wc  map[string]int
	wcn map[string]int

	snap      library.LibrarySnapshot
	exNodes   []*node
	trackByID map[string]*library.SnapTrack
	mapping   *Mapping

	byPath  map[string]*entry
	entries []*entry
	rdirs   []*resultDir

	srcFiles   []*sourceFile
	recipeMap  map[string]*recipe
	recipeList []*recipe
	byReqID    map[string][]*recipe
	bySlug     map[string][]*recipe
	byDBID     map[string]*recipe
	byTitle    map[string][]*recipe
	collFiles  []*collFile
	refFiles   []*refFile

	audioFiles []*audioFile
	groups     []*audioGroup

	takes  []*plannedTake
	drafts []*plannedDraft
	colls  []*plannedColl
	meta   []*MetaReport

	sidecarOf map[string][]string // audio id -> mp3 sidecar refs
}

// Inventory scans the inbox and reads library state; it writes nothing.
func (im *Importer) Inventory(ctx context.Context) (*Inventory, error) {
	b := &builder{
		im: im, ctx: ctx, now: im.now(),
		wc: map[string]int{}, wcn: map[string]int{},
		byPath: map[string]*entry{}, recipeMap: map[string]*recipe{},
	}
	if im.lib != nil {
		snap, err := im.lib.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		b.snap = snap
	} else {
		b.snap = library.LibrarySnapshot{
			Recipes: map[string]library.SnapRecipe{}, AudioIDs: map[string]string{}, Drafts: map[string]library.SnapDraft{},
			SourceKeys: map[string]bool{}, RefKeys: map[string]string{}, Collections: map[string][]string{},
			MasterPaths: map[string]bool{}, DerivedPaths: map[string]bool{},
		}
	}
	b.exNodes = b.existingNodes()
	b.trackByID = map[string]*library.SnapTrack{}
	for i := range b.snap.Tracks {
		b.trackByID[b.snap.Tracks[i].ID] = &b.snap.Tracks[i]
	}
	mp, err := im.loadMapping()
	if err != nil {
		return nil, err
	}
	b.mapping = mp
	for _, e := range mp.Errors {
		b.iss.fail(e.Path, e.Line, e.Code, e.Message)
	}

	if err := b.walk(); err != nil {
		return nil, err
	}
	b.detectResultDirs()
	b.markUnsettled()
	b.classifyFiles()
	b.readAllAudio()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.parseRecipes()
	b.resolveABCSources()
	b.finishRecipes()
	b.markSubsets()
	b.classifyMP3()
	b.groupAudio()
	b.buildMatchIndexes()
	b.matchGroups()
	b.attachRefs()
	b.placeTakes()
	b.placeScores()
	b.planLineage()
	b.placeDrafts()
	b.planMothers()
	b.predictFulfilment()
	b.placeCollections()
	return b.assemble(), nil
}

func (b *builder) readAllAudio() {
	for _, e := range b.entries {
		if !e.flac || e.failed && e.kind != kIgnUnsettled {
			continue
		}
		if e.kind == kPendingFLAC || e.kind == kIgnUnsettled {
			b.readAudio(e)
		}
	}
}

// classifyMP3 settles *.mp3: a sidecar of a FLAC, or an orphan (never silently dropped).
func (b *builder) classifyMP3() {
	stems := map[string]map[string]bool{}
	byStem := map[string][]*audioFile{}
	for _, af := range b.audioFiles {
		if af.status != "" {
			continue
		}
		if stems[af.stem] == nil {
			stems[af.stem] = map[string]bool{}
		}
		stems[af.stem][af.audioID] = true
		byStem[af.stem] = append(byStem[af.stem], af)
	}
	b.sidecarOf = map[string][]string{}
	for _, e := range b.entries {
		if e.kind != kPendingMP3 {
			continue
		}
		stem := stemOf(e.path)
		switch ids := stems[stem]; len(ids) {
		case 1:
			e.kind = kIgnMP3Sidecar
			for id := range ids {
				b.sidecarOf[id] = append(b.sidecarOf[id], e.ref())
			}
		case 0:
			e.kind = kIgnOrphanMP3
			b.iss.warn(e.ref(), 0, "orphan_mp3", "no FLAC with this name")
		default:
			e.kind = kIgnOrphanMP3
			var c []string
			for _, af := range byStem[stem] {
				c = append(c, af.e.ref())
			}
			b.iss.warn(e.ref(), 0, "orphan_mp3", "ambiguous: matches different audio: "+strings.Join(c, "; "))
		}
	}
}

func canon(v map[string]any) string {
	out, _ := json.Marshal(v)
	return string(out)
}

func (r trackRef) key(b *builder) string {
	if r.planned != nil {
		return "take:" + r.planned.g.id
	}
	return "track:" + r.existing
}

// assemble builds the report, plan entries, digest and mapping todo.
func (b *builder) assemble() *Inventory {
	inv := &Inventory{b: b}
	rep := &inv.Report
	*rep = Report{
		Format: ReportFormat, ReportID: b.im.newID(), GeneratedAt: b.now.Format(time.RFC3339),
		Inbox: b.im.o.InboxLabel, DryRun: true,
		RecipeSources: []SourceReport{}, Recipes: []RecipeReport{}, Audio: []AudioReport{}, Duplicates: []DupReport{},
		Drafts: []DraftReport{}, Metadata: []MetaReport{}, Ignored: []IgnoredReport{},
	}
	s := &rep.Summary
	s.ByKind = map[string]int{}

	// files
	for _, e := range b.entries {
		s.Files++
		s.Bytes += e.size
		kind := e.kind
		if strings.HasPrefix(kind, "pending:") { // a read failure left the file unclassified
			kind = "unread:" + strings.TrimPrefix(kind, "pending:")
		}
		s.ByKind[kind]++
		if e.ignored() {
			s.Ignored++
			reason := strings.TrimPrefix(e.kind, ignPrefix)
			if reason == "unsettled" {
				s.Unsettled++
			}
			rep.Ignored = append(rep.Ignored, IgnoredReport{Path: e.ref(), Reason: reason, Bytes: e.size})
		}
	}

	// recipe sources
	for _, sf := range b.srcFiles {
		sf.report.Path = sf.e.ref()
		rep.RecipeSources = append(rep.RecipeSources, sf.report)
		s.RecipesValid += sf.report.Valid
		s.RecipeLinesInvalid += len(sf.report.Invalid)
		s.RecipeSources += len(sf.occ)
	}
	s.RecipesUnique = len(b.recipeList)

	// recipes and plan entries
	var entries []string
	hasTrack := map[*recipe]bool{}
	for _, t := range b.takes {
		hasTrack[t.recipe] = true
	}
	for _, r := range b.recipeList {
		placement := "draft_backlog"
		switch {
		case hasTrack[r]:
			placement = "track"
		case r.inDB:
			placement = "already_imported"
		case r.draft != nil && r.draft.origin == "varianti_import":
			placement = "draft_varianti"
		}
		if r.inDB {
			s.RecipesAlreadyImprted++
		}
		rr := RecipeReport{RequestID: r.reqID(), Seed: r.seed(), Hash: r.hash, Placement: placement, GroupKey: r.groupKey,
			Collections: append([]string{}, r.collections...), HasReference: len(r.refs) > 0, Sources: []string{}}
		for _, oc := range r.occ {
			rr.Sources = append(rr.Sources, oc.key())
			if !b.snap.SourceKeys[fmt.Sprintf("%s\x00%s\x00%d\x00%s", r.hash, oc.file.sourcePath(), oc.line, oc.rawSHA)] {
				entries = append(entries, canon(map[string]any{"k": "source", "hash": r.hash, "path": oc.file.sourcePath(), "line": oc.line, "raw_sha256": oc.rawSHA}))
			}
		}
		rep.Recipes = append(rep.Recipes, rr)
		entries = append(entries, canon(map[string]any{"k": "recipe", "hash": r.hash, "placement": placement}))
		for _, a := range r.refs {
			if _, ok := b.snap.RefKeys[r.hash+"\x00"+a.file.sourcePath()]; !ok {
				s.RefsAttached++
				entries = append(entries, canon(map[string]any{"k": "ref", "hash": r.hash, "source": a.file.sourcePath(), "sha256": hexSHA(refJSON(a.row))}))
			} else {
				s.RefsAttached++
			}
		}
	}

	// drafts
	var draftTodo []todoRow
	for _, d := range b.drafts {
		if d.fulfilled {
			s.DraftsFulfilled++
		}
		if !d.create {
			continue
		}
		s.DraftsNew++
		var mother *string
		mkey := ""
		if !d.mother.zero() {
			l := d.mother.label(b)
			mother, mkey = &l, d.mother.key(b)
		}
		rep.Drafts = append(rep.Drafts, DraftReport{RequestID: d.r.reqID(), Seed: d.r.seed(), Origin: d.origin, GroupKey: d.r.groupKey, Mother: mother})
		entries = append(entries, canon(map[string]any{"k": "draft", "hash": d.r.hash, "origin": d.origin, "group_key": d.r.groupKey, "label": d.label, "mother": mkey}))
		if d.origin == "varianti_import" && d.mother.zero() && !d.noMother {
			draftTodo = append(draftTodo, todoRow{audio: "draft:" + d.r.reqID()})
		}
	}

	// collections
	for _, c := range b.colls {
		var hs []string
		for _, r := range c.members {
			hs = append(hs, r.hash)
		}
		sort.Strings(hs)
		entries = append(entries, canon(map[string]any{"k": "collection", "name": c.name, "members": hs}))
	}
	s.Collections = len(b.colls)
	for _, m := range b.meta {
		rep.Metadata = append(rep.Metadata, *m)
	}

	// audio
	groupOf := map[*audioFile]*audioGroup{}
	for _, g := range b.groups {
		for _, m := range g.members {
			groupOf[m] = g
		}
	}
	for _, af := range b.audioFiles {
		s.AudioFiles++
		ar := AudioReport{Path: af.e.ref(), Bytes: af.e.size, Candidates: []string{}, Artifacts: []string{}, Sidecars: []string{}}
		switch af.status {
		case "unsettled", "invalid":
			ar.Status = af.status
			if af.status == "invalid" {
				s.AudioInvalid++
			}
			rep.Audio = append(rep.Audio, ar)
			continue
		}
		g := groupOf[af]
		ar.SHA256, ar.AudioID = af.e.sha, g.id
		si := af.info.StreamInfo
		ar.DurationS, ar.SampleRate, ar.Bits, ar.Channels = af.info.Duration(), int(si.SampleRate), int(si.BitsPerSample), int(si.Channels)
		ar.Sidecars = append(ar.Sidecars, b.sidecarOf[g.id]...)
		if len(g.members) > 1 && af != g.rep {
			ar.Status = "duplicate"
			rep.Audio = append(rep.Audio, ar)
			continue
		}
		ar.Status = g.status
		ar.Candidates = append(ar.Candidates, g.candidates...)
		if g.status == "new" {
			t := g.take
			ar.Match = &MatchReport{Rule: g.rule(), RequestID: g.recipe.reqID(), RecipeHash: g.recipe.hash}
			ar.Title = t.title
			if !t.parent.zero() {
				ar.Parent = &ParentReport{Rule: t.rule, Of: t.parent.label(b)}
			}
			if t.score != nil && t.scoreABC != nil {
				ar.Score = t.score.ref()
			}
			var an []string
			for _, a := range t.artifacts {
				ar.Artifacts = append(ar.Artifacts, a.name)
				an = append(an, a.name+":"+a.e.sha)
			}
			pk := ""
			if !t.parent.zero() {
				pk = t.parent.key(b)
			}
			scoreSHA := ""
			if t.scoreABC != nil {
				scoreSHA = hexSHA(t.scoreABC)
			}
			entries = append(entries, canon(map[string]any{
				"k": "take", "audio_id": g.id, "path": g.rep.e.sourcePath(), "sha256": g.rep.e.sha, "rule": g.rule(), "hash": g.recipe.hash,
				"title": t.title, "parent": pk, "parent_rule": t.rule, "score": scoreSHA, "artifacts": an,
			}))
			s.TracksNew++
			s.AudioMatched++
			if !t.parent.zero() {
				s.SiblingsInferred++
			}
			if scoreSHA != "" {
				s.ScoresAttached++
			}
		}
		switch g.status {
		case "ambiguous":
			s.AudioAmbiguous++
			inv.todo = append(inv.todo, todoRow{audio: mappingKey(g), candidates: strings.Join(g.candidates, "; ")})
		case "unmatched":
			s.AudioUnmatched++
			inv.todo = append(inv.todo, todoRow{audio: mappingKey(g), candidates: strings.Join(g.candidates, "; ")})
		case "skipped":
			s.AudioSkipped++
		case "already_imported":
			s.AudioAlreadyImported++
		}
		rep.Audio = append(rep.Audio, ar)
	}
	for _, g := range b.groups {
		s.AudioIdentities++
		if len(g.members) > 1 {
			s.DuplicateGroups++
			d := DupReport{AudioID: g.id, Representative: g.rep.e.ref(), Rule: g.rule(), Paths: []string{}}
			for _, m := range g.members {
				d.Paths = append(d.Paths, m.e.ref())
			}
			rep.Duplicates = append(rep.Duplicates, d)
		}
	}

	inv.todo = append(inv.todo, draftTodo...)

	// orphans, issues
	rep.LibraryOrphans = b.im.libraryOrphans(b.snap.MasterPaths, b.snap.DerivedPaths)
	s.LibraryOrphans = len(rep.LibraryOrphans)
	rep.Warnings, rep.Errors = append([]Issue{}, b.iss.warnings...), append([]Issue{}, b.iss.errors...)
	s.Warnings, s.Errors = len(rep.Warnings), len(rep.Errors)

	sort.Strings(entries)
	inv.entries = entries
	rep.PlanEntries = entries
	rep.PlanDigest = digestOf(entries)
	return inv
}

// mappingKey is how the mapping template names an audio identity.
func mappingKey(g *audioGroup) string {
	p := g.rep.e.path
	if utf8.ValidString(p) && cleanInboxPath(p) && strings.IndexFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) < 0 {
		return p
	}
	return "sha256:" + g.rep.e.sha
}

// WriteMappingTemplate writes mapping.todo.csv: the mapping columns plus a
// trailing candidates column (ignored on read), pre-filled for every audio
// that needs a decision and every varianti draft without a mother (C16.6).
func WriteMappingTemplate(w io.Writer, inv *Inventory) error {
	if _, err := io.WriteString(w, "# metamusic import mapping template: set request_id (and seed), '-' to skip an audio, or parent for a draft row.\n"); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"audio", "request_id", "seed", "title", "parent", "candidates"}); err != nil {
		return err
	}
	for _, r := range inv.todo {
		if err := cw.Write([]string{r.audio, "", "", "", "", r.candidates}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
