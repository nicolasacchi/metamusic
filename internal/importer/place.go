package importer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
)

// takeArtifact is a file kept with a take.
type takeArtifact struct {
	name string
	e    *entry
	// bomStripped: the kept bytes are the file without its UTF-8 BOM, so the
	// artifact and the scores row share one byte form (sha is that form's hash).
	bomStripped bool
	sha         string
}

// plannedTake is one audio identity that becomes a track.
type plannedTake struct {
	g          *audioGroup
	recipe     *recipe
	title      string
	slug       string
	recordedAt time.Time
	artifacts  []takeArtifact
	score      *entry
	scoreABC   []byte
	truncABC   *bool
	truncSem   *bool

	parent     trackRef
	rule       string
	root       trackRef
	generation int
	id         string // assigned at apply
}

// plannedDraft is the backlog draft of a recipe without a committed track.
type plannedDraft struct {
	r          *recipe
	origin     string
	label      string
	existingID string
	create     bool
	mother     trackRef
	noMother   bool // mapping said "-": no mother, no warning
	fulfilled  bool
}

// plannedColl is a draft collection.
type plannedColl struct {
	name, source string
	members      []*recipe
}

func humanize(s string) string {
	s = strings.TrimSpace(strings.NewReplacer("_", " ", "-", " ").Replace(s))
	if s == "" {
		return ""
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// takeTitle applies the C16.4 precedence.
func (b *builder) takeTitle(g *audioGroup, r *recipe) string {
	cand := ""
	switch {
	case g.mapRow != nil && strings.TrimSpace(g.mapRow.Title) != "":
		cand = g.mapRow.Title
	case r.title != "":
		cand = r.title
	case g.rep.tag("TITLE") != "":
		cand = g.rep.tag("TITLE")
	case g.rep.stem == "audio" || !utf8.ValidString(g.rep.stem):
		cand = humanize(r.reqID())
	default:
		cand = humanize(g.rep.stem)
	}
	cand = strings.TrimSpace(strings.Map(func(c rune) rune {
		if c < 0x20 || c == 0x7f {
			return ' '
		}
		return c
	}, strings.ToValidUTF8(cand, "�")))
	if cand == "" {
		cand = r.reqID()
	}
	return cutRunes(cand, 200)
}

// placeTakes plans one track per matched, new identity.
func (b *builder) placeTakes() {
	for _, g := range b.groups {
		// the mapping row (title/parent) of any member
		for _, m := range g.members {
			row := b.mapping.ByPath[m.e.path]
			if row == nil {
				row = b.mapping.BySHA[m.e.sha]
			}
			if row != nil && g.mapRow == nil {
				g.mapRow = row
			}
		}
		if g.status != "new" {
			continue
		}
		r := g.recipe
		t := &plannedTake{g: g, recipe: r, recordedAt: g.rep.e.mtime.UTC().Truncate(time.Second)}
		t.title = b.takeTitle(g, r)
		if t.slug = yue2.Slug(t.title); t.slug == "" {
			if t.slug = yue2.Slug(r.reqID()); t.slug == "" {
				t.slug = "track"
			}
		}
		g.title, g.take = t.title, t
		for _, m := range g.members {
			if m.rd == nil || b.recipeMap[m.rd.parsed.Hash] != r {
				continue
			}
			rd := m.rd
			for _, f := range rd.members {
				if f.rd != rd || f.artifact == "" || f.failed || f.sha == "" || f.kind == kIgnUnsettled {
					continue
				}
				t.artifacts = append(t.artifacts, takeArtifact{name: f.artifact, e: f})
			}
			if rd.score != nil && rd.score.data != nil && !rd.score.failed {
				t.score = rd.score
			}
			t.truncABC, t.truncSem = b.truncation(rd)
			break
		}
		sort.Slice(t.artifacts, func(i, j int) bool { return t.artifacts[i].name < t.artifacts[j].name })
		b.takes = append(b.takes, t)
	}
}

// truncation reads result.json's truncated object (C16.4).
func (b *builder) truncation(rd *resultDir) (abc, sem *bool) {
	if rd.result == nil || rd.result.data == nil {
		return nil, nil
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(bytes.TrimPrefix(rd.result.data, []byte("\xef\xbb\xbf")), &doc) != nil {
		b.iss.warn(rd.result.ref(), 0, "invalid_json", "")
		return nil, nil
	}
	raw, ok := doc["truncated"]
	if !ok {
		return nil, nil
	}
	var tr struct {
		ABC      *bool `json:"abc"`
		Semantic *bool `json:"semantic"`
	}
	if json.Unmarshal(raw, &tr) != nil || tr.ABC == nil || tr.Semantic == nil {
		b.iss.warn(rd.result.ref(), 0, "truncated_not_dict", "")
		return nil, nil
	}
	return tr.ABC, tr.Semantic
}

var scoreSeedRe = regexp.MustCompile(`^(.+)[-_](?:seed|s)(\d+)$`)

// placeScores attaches standalone ABC files to takes (C16.4).
func (b *builder) placeScores() {
	for _, e := range b.entries {
		if e.kind != kScoreABC || e.rd != nil || e.data == nil || e.failed {
			continue
		}
		name := stemOf(e.path)
		var hit []*plannedTake
		for _, t := range b.takes {
			for _, m := range t.g.members {
				if m.stem == name {
					hit = append(hit, t)
					break
				}
			}
		}
		if len(hit) == 0 {
			if m := scoreSeedRe.FindStringSubmatch(name); m != nil {
				if n, err := strconv.ParseInt(m[2], 10, 64); err == nil {
					for _, t := range b.takes {
						if t.recipe.reqID() == m[1] && t.recipe.seed() == n {
							hit = append(hit, t)
						}
					}
				}
			}
		}
		if len(hit) != 1 || hit[0].score != nil {
			b.iss.warn(e.ref(), 0, "score_unattached", "")
			continue
		}
		hit[0].score = e
	}
	for _, t := range b.takes {
		if t.score == nil {
			continue
		}
		b.bomWarn(t.score, t.score.data)
		data := bytes.TrimPrefix(t.score.data, []byte("\xef\xbb\xbf"))
		if _, err := yue2.ParseScore(data); err != nil {
			b.iss.warn(t.score.ref(), 0, "score_unparsable", "")
			t.score = nil
			continue
		}
		t.scoreABC = t.score.data
		stripped := len(t.score.data) != len(data)
		if stripped {
			t.scoreABC = data
		}
		// the score is also kept as takes/<id>/score.abc, in the same byte form
		have := false
		for i := range t.artifacts {
			if t.artifacts[i].name == "score.abc" {
				have = true
				if stripped && t.artifacts[i].e == t.score {
					t.artifacts[i].bomStripped, t.artifacts[i].sha = true, shaHex(data)
				}
			}
		}
		if !have && t.score.sha != "" {
			a := takeArtifact{name: "score.abc", e: t.score}
			if stripped {
				a.bomStripped, a.sha = true, shaHex(data)
			}
			t.artifacts = append(t.artifacts, a)
			sort.Slice(t.artifacts, func(i, j int) bool { return t.artifacts[i].name < t.artifacts[j].name })
		}
	}
}

func shaHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ---- refs, drafts, collections -------------------------------------------

func (b *builder) attachRefs() {
	for _, rf := range b.refFiles {
		meta := &MetaReport{Path: rf.e.ref(), Kind: "reference_index", Rows: len(rf.rows)}
		b.meta = append(b.meta, meta)
		for i := range rf.rows {
			row := &rf.rows[i]
			cands := b.byReqID[row.variantID]
			if len(cands) == 0 {
				meta.Unmatched++
				continue
			}
			var seed int64
			seedGiven := false
			if row.seed != "" {
				n, err := strconv.ParseInt(row.seed, 10, 64)
				if err != nil {
					b.iss.warn(rf.e.ref(), row.line, "ref_seed_mismatch", "")
					meta.Unmatched++
					continue
				}
				seed, seedGiven = n, true
			}
			matched := false
			for _, r := range cands {
				if seedGiven && r.seed() != seed {
					continue
				}
				matched = true
				dup := false
				for _, a := range r.refs {
					if a.file == rf.e {
						dup = true
					}
				}
				if !dup {
					r.refs = append(r.refs, attachedRef{file: rf.e, row: row})
				}
			}
			if matched {
				meta.Matched++
			} else {
				b.iss.warn(rf.e.ref(), row.line, "ref_seed_mismatch", "")
				meta.Unmatched++
			}
		}
	}
	for _, r := range b.recipeList {
		for _, a := range r.refs {
			key := r.hash + "\x00" + a.file.sourcePath()
			if old, ok := b.snap.RefKeys[key]; ok && old != hexSHA(refJSON(a.row)) {
				b.iss.warn(a.file.ref(), a.row.line, "ref_changed", "stored reference differs from the file")
			}
		}
	}
}

// refJSON is the stored form of a reference row: every column but variant_id and seed.
func refJSON(row *refRow) []byte {
	b, _ := json.Marshal(row.extra)
	return b
}

func hasVarianti(r *recipe) bool {
	for _, oc := range r.occ {
		for _, comp := range strings.Split(path.Dir(oc.file.path), "/") {
			if comp == "varianti" {
				return true
			}
		}
	}
	return false
}

// placeDrafts decides which recipes get a draft and their origin/label.
func (b *builder) placeDrafts() {
	committed := map[string]bool{}
	for _, t := range b.snap.Tracks {
		committed[t.RecipeID] = true
	}
	for _, r := range b.recipeList {
		if r.inDB && committed[r.dbID] {
			continue
		}
		d := &plannedDraft{r: r, origin: "import_backlog"}
		if hasVarianti(r) {
			d.origin = "varianti_import"
		}
		if r.inDB {
			if ex, ok := b.snap.Drafts[r.dbID]; ok {
				d.existingID = ex.ID
				d.origin = ex.Origin
			}
		}
		d.create = d.existingID == ""
		for _, a := range r.refs {
			if a.row.variantType != "" {
				d.label = a.row.variantType
				break
			}
		}
		if d.label == "" {
			if rest := strings.TrimPrefix(r.reqID(), r.groupKey); rest != r.reqID() && rest != "" {
				d.label = strings.TrimLeft(rest, "-_")
			}
		}
		r.draft = d
		b.drafts = append(b.drafts, d)
	}
}

// planMothers resolves the mother of every varianti draft (C16.4).
func (b *builder) planMothers() {
	nodes := append([]*node{}, b.exNodes...)
	var plNodes []*node
	for _, t := range b.takes {
		n := &node{pl: t, reqID: t.recipe.reqID(), at: t.recordedAt}
		nodes = append(nodes, n)
		plNodes = append(plNodes, n)
	}
	for _, d := range b.drafts {
		if d.origin != "varianti_import" {
			continue
		}
		if d.existingID != "" && b.snap.Drafts[d.r.dbID].ParentTrackID != "" {
			continue
		}
		if row := b.mapping.Drafts[d.r.reqID()]; row != nil && row.Parent != "" {
			if row.Parent == "-" {
				d.noMother = true
				continue
			}
			if ref, ok := b.resolveParent(row.Parent, b.exNodes, plNodes, b.trackByID); ok {
				d.mother = ref
				continue
			}
			b.iss.warn(d.r.occ[0].file.ref(), row.Line, "parent_unresolved", "draft mapping parent names no unique take")
		}
		roots := map[trackRef]bool{}
		for _, n := range nodes {
			if n.pl != nil && n.pl.recipe == d.r {
				continue
			}
			if n.ex != nil && n.ex.RecipeID == d.r.dbID && d.r.dbID != "" {
				continue
			}
			if yue2.GroupKey(n.reqID) == d.r.groupKey {
				roots[b.rootRefOf(n, b.trackByID)] = true
			}
		}
		switch len(roots) {
		case 0:
			b.iss.warn(d.r.occ[0].file.ref(), d.r.occ[0].line, "mother_not_found", "")
		case 1:
			for r := range roots {
				d.mother = r
			}
		default:
			b.iss.warn(d.r.occ[0].file.ref(), d.r.occ[0].line, "mother_ambiguous", "")
		}
	}
}

// predictFulfilment marks the drafts the planned takes will fulfil.
func (b *builder) predictFulfilment() {
	for _, t := range b.takes {
		r := t.recipe
		if r.draft != nil && !r.draft.fulfilled {
			r.draft.fulfilled = true
		}
		for _, o := range b.recipeList {
			if o == r || o.draft == nil || o.draft.fulfilled || o.parsed.CoreHash != r.parsed.CoreHash {
				continue
			}
			o.draft.fulfilled = true
			b.iss.warn(o.occ[0].file.ref(), o.occ[0].line, "fulfilled_by_core_match", "differs in: "+strings.Join(differingKeys(r, o), ", "))
		}
	}
}

// differingKeys names the canonical keys in which two same-core recipes differ.
func differingKeys(a, b *recipe) []string {
	var out []string
	x, y := a.parsed.Request, b.parsed.Request
	if (x.ABC == nil) != (y.ABC == nil) || (x.ABC != nil && *x.ABC != *y.ABC) {
		out = append(out, "abc")
	}
	if (x.CFGScale == nil) != (y.CFGScale == nil) || (x.CFGScale != nil && *x.CFGScale != *y.CFGScale) {
		out = append(out, "cfg_scale")
	}
	if len(out) == 0 {
		out = []string{"none"}
	}
	return out
}

// placeCollections builds the draft collections.
func (b *builder) placeCollections() {
	byName := map[string]*plannedColl{}
	order := []string{}
	get := func(name, source string) *plannedColl {
		if c, ok := byName[name]; ok {
			return c
		}
		c := &plannedColl{name: name, source: source}
		byName[name] = c
		order = append(order, name)
		return c
	}
	add := func(c *plannedColl, rs []*recipe) {
		for _, r := range rs {
			dup := false
			for _, m := range c.members {
				if m == r {
					dup = true
				}
			}
			if !dup {
				c.members = append(c.members, r)
				r.collections = append(r.collections, c.name)
			}
		}
	}
	coverSets := map[string]bool{}
	for _, cf := range b.collFiles {
		c := get(cf.name, cf.e.sourcePath())
		meta := &MetaReport{Path: cf.e.ref(), Kind: "collection", Rows: len(cf.ids)}
		b.meta = append(b.meta, meta)
		ids := map[string]bool{}
		for _, id := range cf.ids {
			ids[id] = true
			if rs := b.byReqID[id]; len(rs) > 0 {
				add(c, rs)
				meta.Matched++
			} else {
				meta.Unmatched++
			}
		}
		coverSets[setKey(ids)] = true
	}
	for _, sf := range b.srcFiles {
		if !sf.subset {
			continue
		}
		ids := map[string]bool{}
		var rs []*recipe
		for _, oc := range sf.occ {
			r := oc.r
			if r != nil {
				ids[r.reqID()] = true
				rs = append(rs, r)
			}
		}
		if coverSets[setKey(ids)] {
			continue
		}
		name := storableStem(sf.e.path)
		c := get(name, sf.e.sourcePath())
		meta := &MetaReport{Path: sf.e.ref(), Kind: "collection", Rows: len(sf.occ), Matched: len(rs)}
		b.meta = append(b.meta, meta)
		add(c, rs)
	}
	sort.Strings(order)
	for _, n := range order {
		b.colls = append(b.colls, byName[n])
	}
	for _, r := range b.recipeList {
		sort.Strings(r.collections)
	}
}

func setKey(m map[string]bool) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, "\x00")
}
