package importer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/importer/inbox"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/ingest"
)

const heartbeatEvery = 30 * time.Second

// applyState is the mutable state of one Apply call.
type applyState struct {
	im        *Importer
	b         *builder
	runID     string
	recipeIDs map[string]string // recipe hash -> recipes.id
	draftIDs  map[string]string // recipe hash -> recipe_drafts.id
	result    ApplyResult
	issues    issues
	eff       map[*plannedTake]effLineage
	committed map[*plannedTake]string
}

type effLineage struct {
	root string
	gen  int
}

// Apply executes inv. The caller passes the digest of the reviewed dry-run;
// a different current plan is refused with ErrPlanChanged unless Force.
func (im *Importer) Apply(ctx context.Context, inv *Inventory, ao ApplyOptions) (*RunResult, error) {
	if im.st == nil || im.lib == nil || im.ing == nil || im.data == nil {
		return nil, errors.New("importer: apply needs a store, library, ingester and data root")
	}
	if !ao.Force && ao.ExpectDigest != inv.Report.PlanDigest {
		return nil, &PlanChangedError{Expected: ao.ExpectDigest, Actual: inv.Report.PlanDigest}
	}
	runID, err := im.lib.BeginRun(ctx, im.o.Principal, im.o.InboxLabel, inv.Report.PlanDigest)
	if err != nil {
		return nil, err
	}
	st := &applyState{
		im: im, b: inv.b, runID: runID,
		recipeIDs: map[string]string{}, draftIDs: map[string]string{},
		eff: map[*plannedTake]effLineage{}, committed: map[*plannedTake]string{},
	}

	hbCtx, stopHB := context.WithCancel(ctx)
	var hbWG sync.WaitGroup
	hbWG.Add(1)
	go func() {
		defer hbWG.Done()
		t := time.NewTicker(heartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				_ = im.lib.Heartbeat(hbCtx, runID)
			}
		}
	}()
	stopAll := func() { stopHB(); hbWG.Wait() }

	im.sweepWorkDirs()
	runErr := st.run(ctx)
	stopAll()

	rep := inv.Report
	rep.ReportID, rep.DryRun, rep.GeneratedAt = runID, false, im.now().Format(time.RFC3339)
	rep.Errors = append(append([]Issue{}, rep.Errors...), st.issues.errors...)
	rep.Warnings = append(append([]Issue{}, rep.Warnings...), st.issues.warnings...)
	rep.Summary.Errors, rep.Summary.Warnings = len(rep.Errors), len(rep.Warnings)
	rep.Result = &st.result
	res := &RunResult{RunID: runID, Report: &rep, TracksImported: st.result.TracksImported, TracksFailed: st.result.TracksFailed, Errors: rep.Summary.Errors}

	// Finish uses a context that survives cancellation of ctx so the run row never stays 'running'.
	fctx := context.WithoutCancel(ctx)
	if runErr != nil {
		res.State = "failed"
		_ = im.lib.FinishRun(fctx, runID, "failed", nil, nil, runErr)
		return res, runErr
	}
	rel, werr := im.writeReportFile(&rep, im.now())
	if werr != nil {
		res.State = "failed"
		_ = im.lib.FinishRun(fctx, runID, "failed", nil, nil, werr)
		return res, werr
	}
	res.ReportPath = rel
	sum, _ := json.Marshal(struct {
		Summary
		TracksImported int `json:"tracks_imported"`
		TracksFailed   int `json:"tracks_failed"`
	}{rep.Summary, st.result.TracksImported, st.result.TracksFailed})
	repJSON, _ := json.Marshal(&rep)
	if err := im.lib.FinishRun(fctx, runID, "completed", sum, repJSON, nil); err != nil {
		res.State = "failed"
		return res, err
	}
	res.State = "completed"
	return res, nil
}

// sweepWorkDirs removes ingest work dirs older than 24 h (apply only).
func (im *Importer) sweepWorkDirs() {
	ents, err := fs.ReadDir(im.data.FS(), "incoming")
	if err != nil {
		return
	}
	cut := im.now().Add(-24 * time.Hour)
	for _, d := range ents {
		if !d.IsDir() || !strings.HasPrefix(d.Name(), "ingest-") {
			continue
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(cut) {
			_ = im.data.RemoveAll(path.Join("incoming", d.Name()))
		}
	}
}

func (s *applyState) tx(ctx context.Context, f func(tx *sql.Tx) error) error {
	tx, err := s.im.st.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := f(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *applyState) run(ctx context.Context) error {
	b := s.b
	lib := s.im.lib

	// 1. recipes and every source occurrence, one tx per source file
	for _, sf := range b.srcFiles {
		if err := s.tx(ctx, func(tx *sql.Tx) error {
			for _, oc := range sf.occ {
				r := oc.r
				id, ok := s.recipeIDs[r.hash]
				if !ok {
					var err error
					if id, _, err = lib.InsertRecipe(ctx, tx, r.parsed); err != nil {
						return err
					}
					s.recipeIDs[r.hash] = id
				}
				if _, err := lib.InsertRecipeSource(ctx, tx, id, oc.ref(), s.runID); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return fmt.Errorf("importer: recipes: %w", err)
		}
	}

	// 2. references
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		for _, r := range b.recipeList {
			for _, a := range r.refs {
				if _, ok := b.snap.RefKeys[r.hash+"\x00"+a.file.sourcePath()]; ok {
					continue
				}
				if err := lib.InsertRefs(ctx, tx, s.recipeIDs[r.hash], a.file.sourcePath(), a.row.extra); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("importer: references: %w", err)
	}

	// 3. drafts for every recipe without a committed track
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		for _, d := range b.drafts {
			if !d.create {
				s.draftIDs[d.r.hash] = d.existingID
				continue
			}
			id, created, err := lib.InsertDraft(ctx, tx, library.NewDraft{
				Origin: d.origin, SourceRecipeID: s.recipeIDs[d.r.hash], ParentTrackID: d.mother.existing,
				GroupKey: d.r.groupKey, Label: d.label,
			})
			if err != nil {
				return err
			}
			if created {
				s.result.DraftsCreated++
			}
			s.draftIDs[d.r.hash] = id
		}
		return nil
	}); err != nil {
		return fmt.Errorf("importer: drafts: %w", err)
	}

	// 4. collections (reference drafts)
	if err := s.tx(ctx, func(tx *sql.Tx) error {
		for _, c := range b.colls {
			for _, r := range c.members {
				id := s.draftIDs[r.hash]
				if id == "" {
					if ex, ok := b.snap.Drafts[r.dbID]; ok && r.inDB {
						id = ex.ID
					}
				}
				if id == "" {
					continue
				}
				if err := lib.AddToCollection(ctx, tx, id, c.name, c.source); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("importer: collections: %w", err)
	}

	// 5. takes
	if err := s.takes(ctx); err != nil {
		return err
	}

	// 6. mothers
	return s.tx(ctx, func(tx *sql.Tx) error {
		now := s.im.now().Format(time.RFC3339)
		for _, d := range b.drafts {
			mid := d.mother.existing
			if d.mother.planned != nil {
				mid = s.committed[d.mother.planned] // "" when that take was not imported
			}
			if mid == "" {
				continue
			}
			res, err := tx.ExecContext(ctx, `UPDATE recipe_drafts SET parent_track_id=?, updated_at=? WHERE id=? AND parent_track_id IS NULL AND revision = 1`,
				mid, now, s.draftIDs[d.r.hash])
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				s.result.MothersLinked += int(n)
			} else if edited := s.scalarInt(ctx, tx, `SELECT COUNT(*) FROM recipe_drafts WHERE id=? AND parent_track_id IS NULL AND revision > 1`, s.draftIDs[d.r.hash]); edited > 0 {
				path, line := s.draftLoc(s.draftIDs[d.r.hash])
				s.issues.warn(path, line, "draft_edited_not_relinked", "the draft was edited; its mother is left as it is")
			}
		}
		n, err := s.relinkMothers(ctx, tx, now)
		s.result.MothersLinked += n
		return err
	})
}

// relinkMothers fills NULL mothers of varianti drafts with the same rules as
// planMothers (C16.4): a draft's own recipe's takes never count, and a draft
// with a "-" mapping row never gets a mother.
func (s *applyState) relinkMothers(ctx context.Context, tx *sql.Tx, now string) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT t.root_track_id, t.recipe_id, r.request_id FROM tracks t JOIN recipes r ON r.id = t.recipe_id`)
	if err != nil {
		return 0, err
	}
	type tr struct{ root, recipe, group string }
	var trs []tr
	for rows.Next() {
		var x tr
		var rid string
		if err := rows.Scan(&x.root, &x.recipe, &rid); err != nil {
			_ = rows.Close()
			return 0, err
		}
		x.group = yue2.GroupKey(rid)
		trs = append(trs, x)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id, group_key, request_id, COALESCE(source_recipe_id,''), revision FROM recipe_drafts WHERE origin='varianti_import' AND parent_track_id IS NULL AND group_key IS NOT NULL`)
	if err != nil {
		return 0, err
	}
	type link struct{ draft, mother string }
	var links []link
	var edited []string // drafts a human edited: never relinked (M2a C27)
	for rows.Next() {
		var id, g, rid, src string
		var rev int
		if err := rows.Scan(&id, &g, &rid, &src, &rev); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if m := s.b.mapping.Drafts[rid]; m != nil && m.Parent == "-" {
			continue
		}
		roots := map[string]bool{}
		for _, x := range trs {
			if x.group == g && (src == "" || x.recipe != src) {
				roots[x.root] = true
			}
		}
		if len(roots) == 1 {
			if rev > 1 {
				edited = append(edited, id)
				continue
			}
			for m := range roots {
				links = append(links, link{id, m})
			}
		}
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, id := range edited {
		path, line := s.draftLoc(id)
		s.issues.warn(path, line, "draft_edited_not_relinked", "the draft was edited; its mother is left as it is")
	}
	n := 0
	for _, l := range links {
		res, err := tx.ExecContext(ctx, `UPDATE recipe_drafts SET parent_track_id=?, updated_at=? WHERE id=? AND parent_track_id IS NULL AND revision = 1`, l.mother, now, l.draft)
		if err != nil {
			return n, err
		}
		c, _ := res.RowsAffected()
		n += int(c)
	}
	return n, nil
}

func (s *applyState) scalarInt(ctx context.Context, tx *sql.Tx, q string, a ...any) int {
	var n int
	if err := tx.QueryRowContext(ctx, q, a...).Scan(&n); err != nil {
		return 0
	}
	return n
}

// draftLoc finds where this run saw the recipe of a stored draft ("" and 0 when it did not).
func (s *applyState) draftLoc(draftID string) (string, int) {
	for _, d := range s.b.drafts {
		if s.draftIDs[d.r.hash] == draftID && len(d.r.occ) > 0 {
			return d.r.occ[0].file.ref(), d.r.occ[0].line
		}
	}
	return "", 0
}

// takeOutcome is one finished ingest.
type takeOutcome struct {
	res ingest.Result
	err error
}

func (s *applyState) takes(ctx context.Context) error {
	b := s.b
	ordered := append([]*plannedTake{}, b.takes...)
	for _, t := range ordered {
		t.id = s.im.newID()
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].generation < ordered[j].generation })
	if len(ordered) == 0 {
		return nil
	}
	results := make([]chan takeOutcome, len(ordered))
	for i := range results {
		results[i] = make(chan takeOutcome, 1)
	}
	jobs := s.im.o.Jobs
	sem := make(chan struct{}, jobs)
	launchCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	next := 0 // first outcome not yet handled
	defer func() {
		cancel()
		wg.Wait()
		// outcomes that succeeded but were never committed leave frozen
		// masters on disk: discard them
		for ; next < len(results); next++ {
			select {
			case out := <-results[next]:
				if out.err == nil {
					_ = s.im.ing.Discard(out.res)
				}
			default:
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i, t := range ordered {
			select {
			case sem <- struct{}{}:
			case <-launchCtx.Done():
				for j := i; j < len(ordered); j++ {
					results[j] <- takeOutcome{err: launchCtx.Err()}
				}
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				results[i] <- s.ingestOne(launchCtx, t)
			}()
		}
	}()

	for i, t := range ordered {
		var out takeOutcome
		select {
		case out = <-results[i]:
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			if out.err == nil {
				_ = s.im.ing.Discard(out.res)
			}
			next = i + 1
			return err
		}
		next = i + 1
		s.commitTake(ctx, t, out)
	}
	return nil
}

// ingestOne opens the inbox files through the identity guard and ingests them.
func (s *applyState) ingestOne(ctx context.Context, t *plannedTake) takeOutcome {
	var closers []fs.File
	defer func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}()
	open := func(e *entry) (fs.File, error) {
		f, err := s.im.src.OpenRegular(e.path, e.id)
		if err != nil {
			return nil, err
		}
		closers = append(closers, f)
		return f, nil
	}
	src, err := open(t.g.rep.e)
	if err != nil {
		return takeOutcome{err: sourceChanged(err)}
	}
	if t.reference() {
		req := ingest.Request{
			TrackID: t.id, Title: t.title, Slug: t.slug, RequestID: t.ref.requestID,
			// the stub row is written with the take, after the ingest: its generated id does not exist yet,
			// so the tags and the request carry the deterministic stub request id
			RecipeID: t.ref.requestID,
			Seed:     0, Source: src, SourceSHA256: t.g.rep.e.sha, RecordedAt: t.recordedAt, Reference: !t.g.isMP3(),
		}
		var res ingest.Result
		var err error
		if t.g.isMP3() {
			res, err = s.im.ing.IngestLossy(ctx, req)
		} else {
			res, err = s.im.ing.Ingest(ctx, req)
		}
		return takeOutcome{res: res, err: err}
	}
	var arts []ingest.ArtifactSrc
	for _, a := range t.artifacts {
		f, err := open(a.e)
		if err != nil {
			return takeOutcome{err: sourceChanged(err)}
		}
		sha := a.e.sha
		if a.bomStripped {
			sf, err := stripBOMFile(f)
			if err != nil {
				return takeOutcome{err: sourceChanged(err)}
			}
			f, sha = sf, a.sha
		}
		arts = append(arts, ingest.ArtifactSrc{Name: a.name, File: f, SHA256: sha})
	}
	req := ingest.Request{
		TrackID: t.id, Title: t.title, Slug: t.slug, RecipeID: s.recipeIDs[t.recipe.hash], RequestID: t.recipe.reqID(),
		Seed: t.recipe.seed(), Source: src, SourceSHA256: t.g.rep.e.sha, RecordedAt: t.recordedAt, Artifacts: arts,
	}
	res, err := s.im.ing.Ingest(ctx, req)
	return takeOutcome{res: res, err: err}
}

// stripBOMFile is the content of f without a leading UTF-8 BOM, in memory
// (scores are small). The caller still closes f.
func stripBOMFile(f fs.File) (fs.File, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, 1<<24))
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	return &memFile{Reader: bytes.NewReader(data), info: memInfo{FileInfo: st, size: int64(len(data))}}, nil
}

type memFile struct {
	*bytes.Reader
	info memInfo
}

func (m *memFile) Stat() (fs.FileInfo, error) { return m.info, nil }
func (m *memFile) Close() error               { return nil }

type memInfo struct {
	fs.FileInfo
	size int64
}

func (m memInfo) Size() int64 { return m.size }

func sourceChanged(err error) error {
	if errors.Is(err, inbox.ErrChanged) {
		return &ingest.Error{Code: ingest.CodeSourceChanged, Detail: "inbox file changed since the scan", Err: err}
	}
	return &ingest.Error{Code: ingest.CodeIO, Detail: "inbox file could not be opened", Err: err}
}

func failCode(err error) (code, msg string) {
	var ie *ingest.Error
	if errors.As(err, &ie) {
		// Detail may carry tool output with absolute paths (C16.7): code only
		return ie.Code, "ingest failed: " + ie.Code
	}
	return "INGEST_FAILED", "ingest failed"
}

// commitTake writes one take's rows in a single transaction.
func (s *applyState) commitTake(ctx context.Context, t *plannedTake, out takeOutcome) {
	path := t.g.rep.e.ref()
	tr := TakeResult{Path: path, AudioID: t.g.id}
	fail := func(code, msg string) {
		tr.Status, tr.Code = "failed", code
		s.result.Takes = append(s.result.Takes, tr)
		s.result.TracksFailed++
		s.issues.fail(path, 0, code, msg)
	}
	if out.err != nil {
		code, msg := failCode(out.err)
		fail(code, msg)
		return
	}
	// effective lineage: a take whose planned parent failed becomes a root
	parentID, rootID, gen := "", t.id, 0
	rule := t.rule
	switch {
	case t.parent.planned != nil:
		if pid, ok := s.committed[t.parent.planned]; ok {
			e := s.eff[t.parent.planned]
			parentID, rootID, gen = pid, e.root, e.gen+1
		} else {
			s.issues.warn(path, 0, "parent_failed", "planned parent was not imported; take stays a root")
			rule = ""
		}
	case t.parent.existing != "":
		parentID, rootID, gen = t.parent.existing, t.root.existing, t.generation
	}
	if t.reference() && t.g.isMP3() && out.res.Lossy == nil {
		_ = s.im.ing.Discard(out.res)
		fail("INGEST_FAILED", "ingest failed")
		return
	}
	nt := s.newTake(t, out.res, parentID, rootID, gen, rule)
	var ful library.Fulfilment
	nt.Out = &ful
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if t.reference() {
			// the stub recipe is inserted in the take's transaction: a failed ingest or insert leaves none
			rid, _, err := s.im.lib.InsertReferenceRecipe(ctx, tx, library.NewReferenceRecipe{Title: t.title, Slug: t.slug, AudioKey: t.g.id})
			if err != nil {
				return err
			}
			nt.RecipeID = rid
		}
		return s.im.lib.InsertTake(ctx, tx, nt)
	})
	if err != nil {
		_ = s.im.ing.Discard(out.res)
		fail("DB_INSERT", "database insert failed")
		return
	}
	s.committed[t] = t.id
	s.eff[t] = effLineage{root: rootID, gen: gen}
	tr.Status, tr.TrackID = "imported", t.id
	s.result.Takes = append(s.result.Takes, tr)
	s.result.TracksImported++
	for _, did := range ful.EditedSkipped {
		path, line := s.draftLoc(did)
		s.issues.warn(path, line, "draft_edited_not_fulfilled", "the draft was edited; it stays in the backlog")
	}
	for _, did := range ful.CoreDrafts { // empty for a reference take
		for _, d := range s.b.drafts {
			if s.draftIDs[d.r.hash] == did {
				s.issues.warn(d.r.occ[0].file.ref(), d.r.occ[0].line, "fulfilled_by_core_match", "differs in: "+strings.Join(differingKeys(t.recipe, d.r), ", "))
			}
		}
	}
}

func (s *applyState) newTake(t *plannedTake, res ingest.Result, parentID, rootID string, gen int, rule string) library.NewTake {
	si := res.Info.StreamInfo
	nt := library.NewTake{
		ID: t.id, Title: t.title, Slug: t.slug,
		ParentTrackID: parentID, RootTrackID: rootID, Generation: gen,
		DurationS: res.Info.Duration(), SampleRate: int(si.SampleRate), BitsPerSample: int(si.BitsPerSample), Channels: int(si.Channels),
		TotalSamples: int64(si.TotalSamples), //nolint:gosec // FLAC totals are 36 bits
		MasterPath:   res.MasterRelPath, MasterSHA256: res.MasterSHA256, MasterBytes: res.MasterBytes,
		SourceSHA256: t.g.rep.e.sha, FramesSHA256: res.FramesSHA256, AudioMD5: res.AudioMD5,
		TruncatedABC: t.truncABC, TruncatedSemantic: t.truncSem,
		ImportRule: t.g.rule(), ImportRunID: s.runID, PrincipalID: s.im.o.Principal, RecordedAt: t.recordedAt,
		Loudness: res.Loudness, Spectrum: res.Spectrum, ToolVersion: res.ToolVersion, ReplayGainDB: res.ReplayGainDB,
		ScoreABC: t.scoreABC,
	}
	if t.reference() {
		nt.Reference = true
		if l := res.Lossy; l != nil {
			nt.Lossy = true
			nt.DurationS, nt.SampleRate, nt.Channels, nt.BitsPerSample = l.DurationS, l.SampleRate, l.Channels, 16
			nt.TotalSamples = int64(l.TotalSamples) //nolint:gosec // an MP3's sample count is far below 2^63
		}
	} else {
		nt.RecipeID = s.recipeIDs[t.recipe.hash]
	}
	// ingest names the MP3 "mp3"; the library's rendition kind is "mp3_320" (C11)
	for _, r := range []struct {
		kind string
		r    ingest.Rendition
	}{{"mp3_320", res.MP3}, {"peaks", res.Peaks}} {
		r, kind := r.r, r.kind
		if nt.Lossy && kind == "mp3_320" {
			continue // a lossy master is its own MP3
		}
		nt.Renditions = append(nt.Renditions, library.NewRendition{Kind: kind, SHA256: r.SHA256, Bytes: r.Bytes, Path: r.RelPath, Params: r.Params})
	}
	for _, a := range res.Artifacts {
		nt.Artifacts = append(nt.Artifacts, library.NewArtifact{Name: path.Base(a.RelPath), SHA256: a.SHA256, Path: a.RelPath, Bytes: a.Bytes})
	}
	if parentID != "" && rule != "" {
		nt.Mutation = &library.NewMutation{Operator: "inferred_sibling", Inferred: rule != "mapping", Rule: rule}
	}
	return nt
}
