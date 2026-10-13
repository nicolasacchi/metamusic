package importer

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
)

// Match rules in precedence order (C16.3).
var ruleNames = []string{"mapping", "result_dir", "tag", "filename_id", "sidecar_json", "slug", "slug_seed", "title_slug"}

// audioGroup is one audio identity (all byte-identical and re-tagged copies).
type audioGroup struct {
	id      string
	members []*audioFile // sorted by path

	existingTrack string // track id when the identity is already in the library
	status        string // new | already_imported | unmatched | ambiguous | skipped
	ruleNo        int    // 1-based index into ruleNames
	recipe        *recipe
	rep           *audioFile
	candidates    []string
	title         string
	mapRow        *MapRow // row that supplied title/parent
	take          *plannedTake
}

func (g *audioGroup) rule() string {
	if g.status == "reference" {
		return library.RuleReference
	}
	if g.ruleNo == 0 {
		return ""
	}
	return ruleNames[g.ruleNo-1]
}

// memberResult is what the rules said about one file.
type memberResult struct {
	ruleNo  int
	recipes []*recipe
	skip    bool
	// reference: a mapping row sent this file to the reference library (C61)
	reference bool
}

// isMP3 reports whether the group is a standalone MP3 identity.
func (g *audioGroup) isMP3() bool { return len(g.members) > 0 && g.members[0].mp3 }

// format is "mp3" or "flac".
func (g *audioGroup) format() string {
	if g.isMP3() {
		return "mp3"
	}
	return "flac"
}

var seedHintRe = regexp.MustCompile(`(?i)^(.+)[-_](?:seed|s)?(\d+)$`)

func uniqueRecipes(rs []*recipe) []*recipe {
	seen := map[string]bool{}
	var out []*recipe
	for _, r := range rs {
		if !seen[r.hash] {
			seen[r.hash] = true
			out = append(out, r)
		}
	}
	return out
}

// buildMatchIndexes prepares lookups used by the rules.
func (b *builder) buildMatchIndexes() {
	b.bySlug = map[string][]*recipe{}
	b.byDBID = map[string]*recipe{}
	b.byTitle = map[string][]*recipe{}
	for _, r := range b.recipeList {
		if s := yue2.Slug(r.reqID()); s != "" {
			b.bySlug[s] = append(b.bySlug[s], r)
		}
		if r.dbID != "" {
			b.byDBID[r.dbID] = r
		}
		if r.title != "" {
			if s := yue2.Slug(r.title); s != "" {
				b.byTitle[s] = append(b.byTitle[s], r)
			}
		}
	}
}

func (b *builder) sidecarRecipes(af *audioFile) []*recipe {
	dir := path.Dir(af.e.path)
	var out []*recipe
	for _, r := range b.recipeList {
		for _, sc := range r.sidecars {
			if stemOf(sc.path) != af.stem {
				continue
			}
			sd := path.Dir(sc.path)
			inRequests := false
			for _, comp := range strings.Split(sd, "/") {
				if comp == "requests" {
					inRequests = true
				}
			}
			if sd == dir || inRequests {
				out = append(out, r)
			}
		}
	}
	return uniqueRecipes(out)
}

func (b *builder) evalMember(af *audioFile) memberResult {
	// 1 mapping
	row := b.mapping.ByPath[af.e.path]
	if row == nil {
		row = b.mapping.BySHA[af.e.sha]
	}
	if row != nil {
		if row.Skip {
			return memberResult{ruleNo: 1, skip: true}
		}
		if row.RequestID == RefTarget {
			// rules 2-8 only tell whether the row overrides a match
			if m := b.evalRules(af); len(m.recipes) == 1 {
				b.iss.warn(af.e.ref(), row.Line, "reference_overrides_match", "the mapping row sends this audio to the reference library although a recipe matches")
			}
			return memberResult{ruleNo: 1, reference: true}
		}
		if row.RequestID != "" {
			var c []*recipe
			for _, r := range b.byReqID[row.RequestID] {
				if row.Seed == nil || r.seed() == *row.Seed {
					c = append(c, r)
				}
			}
			if len(c) > 0 {
				return memberResult{ruleNo: 1, recipes: c}
			}
			b.iss.warn(af.e.ref(), row.Line, "mapping_no_recipe", "mapping row names no known recipe")
		}
	}
	return b.evalRules(af)
}

// evalRules applies match rules 2-8 (C16.3).
func (b *builder) evalRules(af *audioFile) memberResult {
	// 2 result dir
	if af.rd != nil {
		if r := b.recipeMap[af.rd.parsed.Hash]; r != nil {
			return memberResult{ruleNo: 2, recipes: []*recipe{r}}
		}
	}
	// Rules 2..8: the first rule that yields exactly one recipe wins (C16.3);
	// an ambiguous rule is remembered and reported only when no later rule
	// resolves.
	var amb *memberResult
	try := func(rule int, c []*recipe) (memberResult, bool) {
		switch {
		case len(c) == 1:
			return memberResult{ruleNo: rule, recipes: c}, true
		case len(c) > 1 && amb == nil:
			amb = &memberResult{ruleNo: rule, recipes: c}
		}
		return memberResult{}, false
	}
	// 3 tag: MM_REQUEST_ID, then MM_RECIPE_ID, then COMMENT/DESCRIPTION
	if id := af.tag("MM_REQUEST_ID"); id != "" {
		c := b.byReqID[id]
		if s := af.tag("MM_SEED"); s != "" && len(c) > 1 {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				var f []*recipe
				for _, r := range c {
					if r.seed() == n {
						f = append(f, r)
					}
				}
				if len(f) > 0 {
					c = f
				}
			}
		}
		if m, ok := try(3, c); ok {
			return m
		}
	}
	if id := af.tag("MM_RECIPE_ID"); id != "" {
		if r := b.byDBID[id]; r != nil {
			if m, ok := try(3, []*recipe{r}); ok {
				return m
			}
		}
	}
	for _, k := range []string{"COMMENT", "DESCRIPTION"} {
		for _, v := range af.tags[k] {
			if m, ok := try(3, b.byReqID[strings.TrimSpace(v)]); ok {
				return m
			}
		}
	}
	// 4 filename id
	if m, ok := try(4, b.byReqID[af.stem]); ok {
		return m
	}
	// 5 sidecar json
	if m, ok := try(5, b.sidecarRecipes(af)); ok {
		return m
	}
	// 6 slug
	stemSlug := yue2.Slug(af.stem)
	if stemSlug != "" {
		if m, ok := try(6, uniqueRecipes(b.bySlug[stemSlug])); ok {
			return m
		}
	}
	// 7 slug + seed hint
	if m := seedHintRe.FindStringSubmatch(af.stem); m != nil {
		if n, err := strconv.ParseInt(m[2], 10, 64); err == nil {
			var c []*recipe
			for _, r := range b.bySlug[yue2.Slug(m[1])] {
				if r.seed() == n {
					c = append(c, r)
				}
			}
			if mr, ok := try(7, uniqueRecipes(c)); ok {
				return mr
			}
		}
	}
	// 8 title slug
	var c []*recipe
	c = append(c, b.byTitle[stemSlug]...)
	if t := af.tag("TITLE"); t != "" {
		if ts := yue2.Slug(t); ts != "" && ts != stemSlug {
			c = append(c, b.byTitle[ts]...)
		}
	}
	if m, ok := try(8, uniqueRecipes(c)); ok {
		return m
	}
	if amb != nil {
		return *amb
	}
	return memberResult{}
}

// groupAudio forms identity groups from the valid FLACs.
func (b *builder) groupAudio() {
	byID := map[string]*audioGroup{}
	for _, af := range b.audioFiles {
		if af.status != "" {
			continue
		}
		g := byID[af.audioID]
		if g == nil {
			g = &audioGroup{id: af.audioID}
			byID[af.audioID] = g
			b.groups = append(b.groups, g)
		}
		g.members = append(g.members, af)
	}
	for _, g := range b.groups {
		sort.Slice(g.members, func(i, j int) bool { return g.members[i].e.path < g.members[j].e.path })
	}
	sort.Slice(b.groups, func(i, j int) bool { return b.groups[i].members[0].e.path < b.groups[j].members[0].e.path })

	// A non-zero STREAMINFO MD5 shared by different identities: possible re-encode.
	md5s := map[string][]*audioGroup{}
	for _, g := range b.groups {
		m := g.members[0].md5
		if strings.Trim(m, "0") != "" {
			md5s[m] = append(md5s[m], g)
		}
	}
	keys := make([]string, 0, len(md5s))
	for k := range md5s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if gs := md5s[k]; len(gs) > 1 {
			var ps []string
			for _, g := range gs {
				ps = append(ps, g.members[0].e.ref())
			}
			b.iss.warn(gs[0].members[0].e.ref(), 0, "possible_reencode", "same STREAMINFO MD5 as: "+strings.Join(ps[1:], "; "))
		}
	}
}

// matchGroups decides, for every identity, the recipe it belongs to.
func (b *builder) matchGroups() {
	for _, g := range b.groups {
		if id, ok := b.snap.AudioIDs[g.id]; ok {
			g.existingTrack, g.status = id, "already_imported"
			g.rep = g.members[0]
			continue
		}
		if g.isMP3() {
			// a standalone MP3 never matches a recipe; classifyMP3 recorded the decision
			g.rep = g.members[0]
			g.status = b.lossyStatus(g)
			if g.status == "skipped" {
				g.ruleNo = 1
			}
			continue
		}
		type res struct {
			af *audioFile
			memberResult
		}
		var results []res
		skip := false
		for _, af := range g.members {
			r := b.evalMember(af)
			if r.skip {
				skip = true
			}
			if r.ruleNo != 0 {
				results = append(results, res{af, r})
			}
		}
		g.rep = g.members[0]
		if skip {
			g.status, g.ruleNo = "skipped", 1
			continue
		}
		for _, r := range results {
			if r.reference {
				g.status, g.ruleNo = "reference", 1
				break
			}
		}
		if g.status == "reference" {
			continue
		}
		// strongest rule that yields exactly one recipe on any member
		best := 0
		for _, r := range results {
			if len(r.recipes) == 1 && (best == 0 || r.ruleNo < best) {
				best = r.ruleNo
			}
		}
		if best != 0 {
			var winners []*recipe
			var rep *audioFile
			for _, r := range results {
				if len(r.recipes) == 1 && r.ruleNo == best {
					winners = append(winners, r.recipes[0])
					if rep == nil {
						rep = r.af
					}
				}
			}
			winners = uniqueRecipes(winners)
			if len(winners) == 1 {
				g.status, g.ruleNo, g.recipe, g.rep = "new", best, winners[0], rep
				continue
			}
			g.status, g.ruleNo = "ambiguous", best
			g.candidates = recipeLabels(winners)
			continue
		}
		if len(results) > 0 { // every hit was ambiguous
			var all []*recipe
			low := results[0].ruleNo
			for _, r := range results {
				if r.ruleNo < low {
					low = r.ruleNo
				}
			}
			for _, r := range results {
				if r.ruleNo == low {
					all = append(all, r.recipes...)
				}
			}
			g.status, g.ruleNo = "ambiguous", low
			g.candidates = recipeLabels(uniqueRecipes(all))
			continue
		}
		g.status = "unmatched"
		g.candidates = b.nearRecipes(g)
		if b.im.o.ReferenceUnmatched {
			g.status = "reference"
		}
	}
}

// lossyStatus is the group status of a standalone MP3 identity, from the decisions classifyMP3 took.
func (b *builder) lossyStatus(g *audioGroup) string {
	st := "unmatched"
	for _, m := range g.members {
		switch m.lossyDec {
		case "skipped":
			return "skipped"
		case "reference":
			st = "reference"
		}
	}
	return st
}

func recipeLabels(rs []*recipe) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, fmt.Sprintf("%s#%d", r.reqID(), r.seed()))
	}
	sort.Strings(out)
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

// nearRecipes lists up to five recipes close to the audio's name (group_slug
// hits, the same group key slug prefix, Levenshtein <= 3 on slugs).
func (b *builder) nearRecipes(g *audioGroup) []string {
	type cand struct {
		r *recipe
		d int
	}
	var cs []cand
	slugs := map[string]bool{}
	for _, af := range g.members {
		if s := yue2.Slug(af.stem); s != "" {
			slugs[s] = true
		}
		if t := af.tag("TITLE"); t != "" {
			if s := yue2.Slug(t); s != "" {
				slugs[s] = true
			}
		}
	}
	for _, r := range b.recipeList {
		rs := yue2.Slug(r.reqID())
		gs := yue2.Slug(r.groupKey)
		best := -1
		for s := range slugs {
			d := -1
			switch {
			case gs != "" && s == gs:
				d = 0
			case gs != "" && len(s) >= 4 && (strings.HasPrefix(gs, s) || strings.HasPrefix(s, gs)):
				d = 1
			default:
				if l := levenshtein(s, rs); l <= 3 {
					d = l
				}
			}
			if d >= 0 && (best < 0 || d < best) {
				best = d
			}
		}
		if best >= 0 {
			cs = append(cs, cand{r, best})
		}
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].d != cs[j].d {
			return cs[i].d < cs[j].d
		}
		if cs[i].r.reqID() != cs[j].r.reqID() {
			return cs[i].r.reqID() < cs[j].r.reqID()
		}
		return cs[i].r.seed() < cs[j].r.seed()
	})
	var out []string
	for _, c := range cs {
		out = append(out, fmt.Sprintf("%s#%d", c.r.reqID(), c.r.seed()))
		if len(out) == 5 {
			break
		}
	}
	return out
}

func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			c := 1
			if ra[i-1] == rb[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
