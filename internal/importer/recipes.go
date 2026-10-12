package importer

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"sort"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/engine/yue2"
	"github.com/nicolasacchi/metamusic/internal/library"
)

// occurrence is one place a recipe appears in the inbox.
type occurrence struct {
	r      *recipe
	file   *entry
	line   int
	raw    []byte
	rawSHA string
	kind   string // jsonl_line | json_array_item | json_file
}

func (o *occurrence) ref() library.SourceRef {
	return library.SourceRef{Path: o.file.sourcePath(), Line: o.line, Raw: o.raw, RawKind: o.kind}
}

func (o *occurrence) key() string {
	return fmt.Sprintf("%s:%d", o.file.ref(), o.line)
}

// recipe is one unique canonical request.
type recipe struct {
	hash      string
	parsed    yue2.Parsed
	occ       []*occurrence
	title     string // per-song JSON title (title_slug rule)
	sidecars  []*entry
	resultDir *resultDir
	dbID      string
	inDB      bool

	refs        []attachedRef
	collections []string
	draft       *plannedDraft
	groupKey    string
}

func (r *recipe) reqID() string { return r.parsed.Request.ID }
func (r *recipe) seed() int64   { return r.parsed.Request.Seed }

type attachedRef struct {
	file *entry
	row  *refRow
}

// sourceFile is a file that carries recipes.
type sourceFile struct {
	e      *entry
	report SourceReport
	occ    []*occurrence
	subset bool
}

// collFile is a collection JSON.
type collFile struct {
	e    *entry
	name string
	ids  []string
}

// refFile is a reference index CSV.
type refFile struct {
	e      *entry
	rows   []refRow
	header []string
}

func (b *builder) recipeFor(p yue2.Parsed) *recipe {
	if r, ok := b.recipeMap[p.Hash]; ok {
		return r
	}
	r := &recipe{hash: p.Hash, parsed: p}
	b.recipeMap[p.Hash] = r
	return r
}

// abcResolver confines abc_path to regular files inside the inbox (C16.2).
func (b *builder) abcResolver(from *entry) func(string) ([]byte, error) {
	return func(rel string) ([]byte, error) {
		if !cleanInboxPath(rel) {
			return nil, errors.New("unresolved")
		}
		target := path.Join(path.Dir(from.path), rel)
		te := b.byPath[target]
		if te == nil {
			return nil, errors.New("unresolved")
		}
		switch te.kind {
		case kPendingABC, kScoreABC, kABCSource:
		default:
			return nil, errors.New("unresolved")
		}
		if te.size > capABC {
			return nil, errors.New("unresolved")
		}
		data := b.readEntry(te, capABC)
		if data == nil {
			return nil, errors.New("unresolved")
		}
		b.bomWarn(te, data)
		data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
		if !utf8.Valid(data) {
			return nil, errors.New("unresolved")
		}
		te.kind = kABCSource
		return data, nil
	}
}

// parseRecipes turns every recipe-bearing file into occurrences.
func (b *builder) parseRecipes() {
	for _, e := range b.entries {
		if b.ctx.Err() != nil || e.failed {
			continue
		}
		switch e.kind {
		case kRecipesJSONL:
			b.parseJSONL(e)
		case kRecipesJSONArray:
			b.parseArray(e)
		case kRequestJSON:
			b.parseSingle(e)
		}
	}
	for _, sf := range b.srcFiles {
		sort.Slice(sf.report.Invalid, func(i, j int) bool { return sf.report.Invalid[i].Line < sf.report.Invalid[j].Line })
	}
}

func (b *builder) newSource(e *entry) *sourceFile {
	sf := &sourceFile{e: e, report: SourceReport{Path: e.ref(), Kind: e.kind, Invalid: []InvalidLine{}, Warnings: []string{}}}
	b.srcFiles = append(b.srcFiles, sf)
	return sf
}

func (b *builder) opts(e *entry, allowTitle bool) yue2.ParseOptions {
	return yue2.ParseOptions{ResolveABCPath: b.abcResolver(e), AllowTitle: allowTitle}
}

// addLine parses one raw request and records the occurrence. Returns false when invalid.
func (b *builder) addLine(sf *sourceFile, line int, raw []byte, kind string, allowTitle bool) *recipe {
	e := sf.e
	p, err := yue2.ParseRequest(raw, b.opts(e, allowTitle))
	if err != nil {
		code, detail := "invalid", ""
		var pe *yue2.ParseError
		if errors.As(err, &pe) {
			code, detail = pe.Code, pe.Detail
		}
		sf.report.Invalid = append(sf.report.Invalid, InvalidLine{Line: line, Code: code})
		b.iss.fail(e.ref(), line, code, detail)
		return nil
	}
	for _, w := range p.Warnings {
		b.warnCounted(e.ref(), line, w)
	}
	r := b.recipeFor(p)
	if p.Title != "" && r.title == "" {
		r.title = p.Title
	}
	oc := &occurrence{r: r, file: e, line: line, raw: append([]byte(nil), raw...), rawSHA: hexSHA(raw), kind: kind}
	r.occ = append(r.occ, oc)
	sf.occ = append(sf.occ, oc)
	sf.report.Valid++
	return r
}

func (b *builder) parseJSONL(e *entry) {
	data := e.data
	if data == nil {
		return
	}
	sf := b.newSource(e)
	if bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		data = data[3:]
		e.bomSeen = true
		b.iss.warn(e.ref(), 0, "bom", "")
		sf.report.Warnings = append(sf.report.Warnings, "bom")
	}
	rest := data
	lineNo := 0
	for len(rest) > 0 {
		var line []byte
		i := bytes.IndexByte(rest, '\n')
		terminated := i >= 0
		if terminated {
			line, rest = rest[:i], rest[i+1:]
		} else {
			line, rest = rest, nil
		}
		lineNo++
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if !terminated {
			b.iss.warn(e.ref(), lineNo, "unterminated_last_line", "line skipped until the file ends with a newline")
			sf.report.Warnings = append(sf.report.Warnings, "unterminated_last_line")
			continue
		}
		sf.report.Lines++
		if len(line) > capJSONLLine {
			sf.report.Invalid = append(sf.report.Invalid, InvalidLine{Line: lineNo, Code: "line_too_long"})
			b.iss.fail(e.ref(), lineNo, "line_too_long", "line exceeds 1 MiB")
			continue
		}
		b.addLine(sf, lineNo, line, "jsonl_line", false)
	}
}

func (b *builder) parseArray(e *entry) {
	sf := b.newSource(e)
	for i, el := range e.arr {
		sf.report.Lines++
		b.addLine(sf, i+1, bytes.TrimSpace(el), "json_array_item", false)
	}
}

func (b *builder) parseSingle(e *entry) {
	data := e.data
	if data == nil {
		return
	}
	sf := b.newSource(e)
	sf.report.Lines = 1
	if bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		data = data[3:]
		e.bomSeen = true
		b.iss.warn(e.ref(), 0, "bom", "")
	}
	r := b.addLine(sf, 0, data, "json_file", true)
	if r == nil {
		return
	}
	if e.rd != nil {
		r.resultDir = e.rd
	} else {
		r.sidecars = append(r.sidecars, e)
	}
}

// finishRecipes sorts recipes, flags id conflicts and marks recipes already in the library.
func (b *builder) finishRecipes() {
	b.recipeList = b.recipeList[:0]
	for _, r := range b.recipeMap {
		sort.SliceStable(r.occ, func(i, j int) bool {
			if r.occ[i].file.path != r.occ[j].file.path {
				return r.occ[i].file.path < r.occ[j].file.path
			}
			return r.occ[i].line < r.occ[j].line
		})
		r.groupKey = yue2.GroupKey(r.reqID())
		if s, ok := b.snap.Recipes[r.hash]; ok {
			r.inDB, r.dbID = true, s.ID
		}
		b.recipeList = append(b.recipeList, r)
	}
	sort.Slice(b.recipeList, func(i, j int) bool {
		a, c := b.recipeList[i], b.recipeList[j]
		if a.reqID() != c.reqID() {
			return a.reqID() < c.reqID()
		}
		if a.seed() != c.seed() {
			return a.seed() < c.seed()
		}
		return a.hash < c.hash
	})
	b.byReqID = map[string][]*recipe{}
	for _, r := range b.recipeList {
		b.byReqID[r.reqID()] = append(b.byReqID[r.reqID()], r)
	}
	ids := make([]string, 0, len(b.byReqID))
	for id := range b.byReqID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if rs := b.byReqID[id]; len(rs) > 1 {
			b.iss.warn(rs[0].occ[0].file.ref(), rs[0].occ[0].line, "id_conflict", fmt.Sprintf("%d recipes share one request id", len(rs)))
		}
	}
}

// markSubsets finds JSONL files whose every valid line duplicates a line of
// another recipe source (a pilot subset) — C16.4 collections.
func (b *builder) markSubsets() {
	lines := map[*sourceFile]map[string]bool{}
	for _, sf := range b.srcFiles {
		if sf.e.kind != kRecipesJSONL {
			continue
		}
		set := map[string]bool{}
		for _, oc := range sf.occ {
			set[oc.rawSHA] = true
		}
		lines[sf] = set
	}
	for _, sf := range b.srcFiles {
		set := lines[sf]
		if sf.e.kind != kRecipesJSONL || len(set) == 0 {
			continue
		}
		for _, other := range b.srcFiles {
			oset := lines[other]
			if other == sf || oset == nil {
				continue
			}
			sub := true
			for k := range set {
				if !oset[k] {
					sub = false
					break
				}
			}
			if sub && (len(oset) > len(set) || (len(oset) == len(set) && other.e.path < sf.e.path)) {
				sf.subset = true
				break
			}
		}
	}
}

// resolveABCSources reads standalone .abc files as scores and checks them.
func (b *builder) resolveABCSources() {
	for _, e := range b.entries {
		if e.kind != kPendingABC || e.failed {
			continue
		}
		data := b.readEntry(e, capABC)
		if data == nil {
			continue
		}
		e.kind = kScoreABC
		b.bomWarn(e, data)
		if !utf8.Valid(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))) {
			b.iss.warn(e.ref(), 0, "score_unparsable", "not valid UTF-8")
		}
	}
	for _, e := range b.entries {
		if e.kind == kScoreABC && e.rd != nil && e.data == nil {
			b.bomWarn(e, b.readEntry(e, capABC))
		}
	}
}
