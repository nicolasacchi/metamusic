package dsp_test

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/dsp"
	"github.com/nicolasacchi/metamusic/internal/dsp/dsptest"
)

// dspFiles parses every non-test Go file under internal/dsp (this package and its subtree), in sorted order.
func dspFiles(t *testing.T) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		files[filepath.ToSlash(path)] = f
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 10 {
		t.Fatalf("only %d dsp files found: the scan is looking in the wrong place", len(files))
	}
	return files
}

// importName is the local name under which f imports path ("" when it does not).
func importName(f *ast.File, path string) string {
	for _, im := range f.Imports {
		if strings.Trim(im.Path.Value, `"`) == path {
			if im.Name != nil {
				return im.Name.Name
			}
			return filepath.Base(path)
		}
	}
	return ""
}

// selectors calls fn for every pkg.Name selector expression in f where pkg is the local name of an import.
func selectors(f *ast.File, local string, fn func(name string, pos token.Pos)) {
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == local {
				fn(sel.Sel.Name, sel.Pos())
			}
		}
		return true
	})
}

var bannedMath = map[string]bool{
	"Exp": true, "Exp2": true, "Expm1": true, "Log": true, "Log2": true, "Log10": true, "Log1p": true, "Pow": true,
	"Sinh": true, "Cosh": true, "Tanh": true, "Cbrt": true, "FMA": true,
}

// TestDSP_BannedMath is the go/ast scan of D-T1-3(a): no host-dependent math function anywhere under internal/dsp. The
// functions' own replacements are dsp.Exp, dsp.Log and dsp.Pow.
func TestDSP_BannedMath(t *testing.T) {
	t.Parallel()
	for path, f := range dspFiles(t) {
		for _, imp := range []string{"math", "math/cmplx"} {
			if local := importName(f, imp); local != "" && local != "_" {
				selectors(f, local, func(name string, _ token.Pos) {
					if bannedMath[name] || (imp == "math/cmplx" && (name == "Exp" || name == "Log" || name == "Pow")) {
						t.Errorf("%s uses %s.%s (banned in internal/dsp, D-T1-3a)", path, imp, name)
					}
				})
			}
		}
	}
}

var randMethodsBanned = map[string]bool{
	"Float64": true, "Float32": true, "NormFloat64": true, "ExpFloat64": true, "Int": true, "IntN": true, "Int32": true, "Int32N": true,
	"Int64": true, "Int64N": true, "Uint32": true, "Uint32N": true, "Uint64N": true, "UintN": true, "Perm": true, "Shuffle": true,
	"N": true, "Seed": true, "New": true, "NewSource": true, "NewZipf": true, "Read": true,
}

// TestDSP_RandOnlyPCG: math/rand (either version) is imported by dsp/rng.go only, and rng.go uses nothing of it but NewPCG
// and the Uint64 method (D-T1-3b); crypto/rand is not imported at all.
func TestDSP_RandOnlyPCG(t *testing.T) {
	t.Parallel()
	for path, f := range dspFiles(t) {
		for _, imp := range []string{"math/rand", "math/rand/v2", "crypto/rand"} {
			local := importName(f, imp)
			if local == "" {
				continue
			}
			if path != "rng.go" || imp == "crypto/rand" {
				t.Errorf("%s imports %s: only rng.go may import math/rand/v2", path, imp)
				continue
			}
			selectors(f, "rand", func(name string, _ token.Pos) {
				if name != "NewPCG" && name != "PCG" {
					t.Errorf("rng.go uses rand.%s: only NewPCG (and the PCG type) are allowed", name)
				}
			})
		}
	}
	// the methods of the generator: nothing but Uint64 is ever called on it
	ast.Inspect(dspFiles(t)["rng.go"], func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && randMethodsBanned[sel.Sel.Name] {
			if call := sel.X; call != nil {
				if id, ok := sel.X.(*ast.SelectorExpr); ok && id.Sel.Name == "p" {
					t.Errorf("rng.go calls the PCG's %s: only Uint64 is allowed", sel.Sel.Name)
				}
			}
		}
		return true
	})
}

// TestOps_NoGoroutinesNoClock is the go/ast scan of C205.7 over internal/dsp/...: no go statement, no clock and no
// scheduler (D-T1-3e); time may be imported for nothing but a Duration type.
func TestOps_NoGoroutinesNoClock(t *testing.T) {
	t.Parallel()
	clock := map[string]bool{"Now": true, "Since": true, "Until": true, "Sleep": true, "After": true, "AfterFunc": true, "Tick": true,
		"NewTimer": true, "NewTicker": true}
	for path, f := range dspFiles(t) {
		ast.Inspect(f, func(n ast.Node) bool {
			if _, ok := n.(*ast.GoStmt); ok {
				t.Errorf("%s starts a goroutine", path)
			}
			return true
		})
		if local := importName(f, "time"); local != "" {
			selectors(f, local, func(name string, _ token.Pos) {
				if clock[name] {
					t.Errorf("%s uses time.%s", path, name)
				}
			})
		}
	}
}

func hashFloats(x []float64) string {
	h := sha256.New()
	var b [8]byte
	for _, v := range x {
		binary.LittleEndian.PutUint64(b[:], math.Float64bits(v))
		h.Write(b[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// dspGolden are hashes of the primitives' outputs on fixed inputs.
func dspGolden() map[string]string {
	n := 2048
	re, im := dsptest.Noise(1, n, 1), make([]float64, n)
	dsp.FFT(re, im)
	r := dsp.NewRand(42)
	rnd := make([]float64, 512)
	for i := range rnd {
		rnd[i] = r.Float64()
		if i%2 == 1 {
			rnd[i] = r.Norm()
		}
	}
	x := dsptest.Sine(16000, 440, 0.5, 0.25)
	return map[string]string{
		"fft":      hashFloats(append(re, im...)),
		"rand":     hashFloats(rnd),
		"resample": hashFloats(dsp.Resample(x, 22050.0/16000, 32)),
		"conv":     hashFloats(dsp.ConvolveUP(x, dsptest.Noise(2, 700, 1), 256, nil)),
	}
}

const dspGoldenPath = "testdata/dsp-golden.txt"

// TestDSP_Golden pins the FFT, the PCG conversions, the resampler and the convolution bit for bit (amd64, GOAMD64=v1).
func TestDSP_Golden(t *testing.T) {
	dsptest.RequireGolden(t)
	got := dspGolden()
	names := make([]string, 0, len(got))
	for k := range got {
		names = append(names, k)
	}
	sort.Strings(names)
	if *update {
		var sb strings.Builder
		sb.WriteString("# sha256 of the little-endian bit patterns of each primitive's output on fixed inputs. linux/amd64, GOAMD64=v1.\n")
		for _, k := range names {
			sb.WriteString(k + " " + got[k] + "\n")
		}
		if err := os.WriteFile(dspGoldenPath, []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(dspGoldenPath)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0][0] != '#' {
			want[f[0]] = f[1]
		}
	}
	for _, k := range names {
		if got[k] != want[k] {
			t.Errorf("%s: hash %s, golden %s", k, got[k], want[k])
		}
	}
}

// TestDeterminism_FMAOff re-executes the goldens under GODEBUG=cpu.fma=off, which disables the run-time FMA paths of
// math.Exp and math.FMA, and requires the same bits. It fails if the runtime does not know the setting (stderr "unknown cpu
// feature": a GOAMD64=v3 binary, or a typo) so the check can never pass vacuously.
func TestDeterminism_FMAOff(t *testing.T) {
	dsptest.RequireGolden(t)
	dsptest.ReexecFMAOff(t, `^(TestMath_Golden|TestDSP_Golden)$`)
}
