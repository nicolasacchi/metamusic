package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// museTextMethods are the methods of muses.Service that return Muse text or the ids of live Muses (C133, C144.2).
var museTextMethods = []string{"Get", "Ref", "Refs", "List", "LiveIDs"}

// museReaderAllowed are the packages that may call them: the L1/L2 readers, L3's one new reader, `songs` (its existing reads)
// and the composition roots (C133). Helper subtrees (rigs, fixtures) are exempt.
var museReaderAllowed = []string{"internal/muses", "internal/board", "internal/songview", "internal/recordview", "internal/nextactions", "internal/songs",
	"internal/api", "internal/app", "cmd/metamusic"}

const musesImport = mod + "internal/muses"

// museServiceMatcher reports whether a type expression is *muses.Service / muses.Service in this file, and
// whether a right-hand side is muses.New(...).
type museMatcher struct{ local string }

func (m museMatcher) isService(e ast.Expr) bool {
	if s, ok := e.(*ast.StarExpr); ok {
		e = s.X
	}
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Service" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == m.local
}

func (m museMatcher) isNew(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "New" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == m.local
}

// collect adds to names every identifier declared in root with the muses service type (fields, parameters, vars) or
// assigned from muses.New.
func (m museMatcher) collect(root ast.Node, names map[string]bool) {
	ast.Inspect(root, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.Field:
			if m.isService(v.Type) {
				for _, id := range v.Names {
					names[id.Name] = true
				}
			}
		case *ast.ValueSpec:
			if v.Type != nil && m.isService(v.Type) {
				for _, id := range v.Names {
					names[id.Name] = true
				}
			}
		case *ast.AssignStmt:
			for i, r := range v.Rhs {
				if lhs, ok := v.Lhs[min(i, len(v.Lhs)-1)].(*ast.Ident); ok && m.isNew(r) {
					names[lhs.Name] = true
				}
			}
		}
		return true
	})
}

// pkgMuseNames is what the files of one package declare with the muses service type: struct field names (matched against
// `x.field.Method()` anywhere in the package) and package-level variables (matched against a bare identifier).
type pkgMuseNames struct{ fields, vars map[string]bool }

func museFile(fset *token.FileSet, path, src string) (f *ast.File, m museMatcher, err error) {
	f, err = parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return nil, m, err
	}
	for _, im := range f.Imports {
		if strings.Trim(im.Path.Value, `"`) == musesImport {
			m.local = "muses"
			if im.Name != nil {
				m.local = im.Name.Name
			}
		}
	}
	return f, m, nil
}

// museReaderCalls returns the call sites of muses.Service text readers in the files of one package (path to source). The
// receiver must be a struct field of that type declared in the package, a package-level variable of that type, or a
// parameter or local of the enclosing function with that type (or assigned from muses.New), in a file that imports muses. A
// package that merely imports muses and calls something else (provenance, httpserver) is not a reader.
func museReaderCalls(files map[string]string) []string {
	fset := token.NewFileSet()
	type parsedFile struct {
		f *ast.File
		m museMatcher
	}
	parsed := map[string]parsedFile{}
	names := pkgMuseNames{fields: map[string]bool{}, vars: map[string]bool{}}
	var out []string
	for path, src := range files {
		f, m, err := museFile(fset, path, src)
		if err != nil {
			out = append(out, path+": unparsable: "+err.Error())
			continue
		}
		parsed[path] = parsedFile{f, m}
		if m.local == "" { // a file that does not import muses declares no value of its type, but it may call a field of one
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if st, ok := n.(*ast.StructType); ok {
				for _, fl := range st.Fields.List {
					if m.isService(fl.Type) {
						for _, id := range fl.Names {
							names.fields[id.Name] = true
						}
					}
				}
			}
			return true
		})
		for _, d := range f.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.VAR {
				for _, sp := range gd.Specs {
					if vs := sp.(*ast.ValueSpec); vs.Type != nil && m.isService(vs.Type) {
						for _, id := range vs.Names {
							names.vars[id.Name] = true
						}
					}
				}
			}
		}
	}
	for path, pf := range parsed {
		for _, d := range pf.f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			local := map[string]bool{}
			pf.m.collect(fd, local)
			ast.Inspect(fd, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !slices.Contains(museTextMethods, sel.Sel.Name) {
					return true
				}
				recv, hit := "", false
				switch x := sel.X.(type) {
				case *ast.Ident:
					recv = x.Name
					hit = local[recv] || names.vars[recv]
				case *ast.SelectorExpr:
					recv = x.Sel.Name
					hit = names.fields[recv]
				}
				if hit {
					out = append(out, path+": calls "+recv+"."+sel.Sel.Name+" at "+fset.Position(call.Pos()).String())
				}
				return true
			})
		}
	}
	return out
}

// byDir groups files (slash paths) by directory.
func byDir(files map[string]string) map[string]map[string]string {
	out := map[string]map[string]string{}
	for path, src := range files {
		dir := path[:strings.LastIndex(path, "/")]
		if out[dir] == nil {
			out[dir] = map[string]string{}
		}
		out[dir][path] = src
	}
	return out
}

// museReaderOffenders scans files (slash paths relative to the repo root) and reports callers outside the allowlist.
func museReaderOffenders(files map[string]string) []string {
	var off []string
	for dir, pkg := range byDir(files) {
		if helperSubtree(dir) {
			continue
		}
		allowed := false
		for _, a := range museReaderAllowed {
			allowed = allowed || under(dir, a)
		}
		if allowed {
			continue
		}
		off = append(off, museReaderCalls(pkg)...)
	}
	slices.Sort(off)
	return off
}

// TestMuseReaders_L3 is the AST guard of C133/C144.2: L1 and L2 name their Muse-text readers (muses, board, songview,
// recordview); L3 adds exactly one, nextactions. atlas, features and sequence reach muses transitively through songs but never
// call it.
func TestMuseReaders_L3(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	all := map[string]string{}
	for p, s := range goFiles(t, filepath.Join(root, "internal")) {
		all["internal/"+p] = s
	}
	for p, s := range goFiles(t, filepath.Join(root, "cmd")) {
		all["cmd/"+p] = s
	}
	if len(all) < 100 {
		t.Fatalf("scanned only %d files", len(all))
	}
	if off := museReaderOffenders(all); len(off) > 0 {
		t.Errorf("Muse text is read outside the allowlist %v: %v", museReaderAllowed, off)
	}
	// the scan is not vacuous: the shipped readers are found where they are allowed
	found := map[string]bool{}
	for dir, pkg := range byDir(all) {
		if len(museReaderCalls(pkg)) > 0 {
			found[dir] = true
		}
	}
	for _, want := range []string{"internal/songview", "internal/recordview", "internal/api"} {
		if !found[want] {
			t.Errorf("no Muse-text call found in %s: the scan misses a known reader (found %v)", want, found)
		}
	}
	// the allowlist is exactly the plan's: L3 adds one reader, nextactions
	if got := strings.Join(museReaderAllowed, ","); got != "internal/muses,internal/board,internal/songview,internal/recordview,internal/nextactions,internal/songs,internal/api,internal/app,cmd/metamusic" {
		t.Errorf("the Muse-text reader allowlist changed: %s", got)
	}
	// the checker bites
	const hdr = "package x\nimport \"github.com/nicolasacchi/metamusic/internal/muses\"\n"
	plant := map[string]string{
		"internal/atlas/a.go":           hdr + "type S struct{ m *muses.Service }\nfunc (s *S) f() { s.m.Refs(nil, nil) }\n",
		"internal/features/b.go":        hdr + "func f(m *muses.Service) { m.Get(nil, \"\") }\n",
		"internal/sequence/c.go":        hdr + "func f() { m := muses.New(nil, nil, nil); m.LiveIDs(nil) }\n",
		"internal/provenance/d.go":      hdr + "type S struct{ Muses *muses.Service }\nfunc (s *S) f() { s.Muses.EnsureReferenceTx() }\n",
		"internal/nextactions/e.go":     hdr + "func f(m *muses.Service) { m.Refs(nil, nil) }\n",
		"internal/songs/songstest/h.go": hdr + "func f(m *muses.Service) { m.List(nil) }\n",
		"internal/atlas/g.go":           "package x\nfunc f(m interface{ List() }) { m.List() }\n",
		"internal/httpserver/i.go":      hdr + "type O struct{ Muses *muses.Service }\nfunc f(o O) { _ = o.Muses }\n",
		"internal/sequence/j.go":        hdr + "func f(m *muses.Service) { m.Ref(nil, \"\") }\n",
	}
	off := museReaderOffenders(plant)
	got := map[string]bool{}
	for _, o := range off {
		got[o[:strings.Index(o, ":")]] = true
	}
	for _, want := range []string{"internal/atlas/a.go", "internal/features/b.go", "internal/sequence/c.go", "internal/sequence/j.go"} {
		if !got[want] {
			t.Errorf("planted offender %s was not found (found %v)", want, off)
		}
	}
	for _, ok := range []string{"internal/provenance/d.go", "internal/nextactions/e.go", "internal/songs/songstest/h.go", "internal/atlas/g.go", "internal/httpserver/i.go"} {
		if got[ok] {
			t.Errorf("%s was reported but is allowed: %v", ok, off)
		}
	}
}
