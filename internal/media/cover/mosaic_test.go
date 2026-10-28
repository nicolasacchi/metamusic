package cover_test

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/media/cover"
	"github.com/nicolasacchi/metamusic/internal/perftest"
)

func parts(n int) []cover.Part {
	out := make([]cover.Part, n)
	for i := range out {
		out[i] = cover.Part{RootID: "root-synthetic-" + strconv.Itoa(i), Seed: int64(1000 + 37*i)}
	}
	return out
}

func rec(id string, seed int64, n int) cover.Input {
	return cover.Input{Kind: cover.KindRecord, ID: id, Seed: seed, Parts: parts(n)}
}

type mosaicCase struct {
	Name   string `json:"name"`
	Parts  int    `json:"parts"`
	Size   int    `json:"size"`
	Digest string `json:"sha256"`
}

func TestCover_Mosaic_DeterministicPixels(t *testing.T) {
	t.Parallel()
	cases := []mosaicCase{{Name: "no parts", Parts: 0, Size: 128}, {Name: "three", Parts: 3, Size: 96}, {Name: "nine", Parts: 9, Size: 100}, {Name: "twelve", Parts: 12, Size: 128}}
	for i, c := range cases {
		in := rec("record-synthetic-1", 4242, c.Parts)
		a, b := cover.RenderMosaic(in, c.Size), cover.RenderMosaic(in, c.Size)
		if pixelDigest(a) != pixelDigest(b) {
			t.Fatalf("%s: two renders differ", c.Name)
		}
		if a.Bounds().Dx() != c.Size || a.Bounds().Dy() != c.Size || len(a.Palette) != 16 {
			t.Fatalf("%s: bounds %v palette %d", c.Name, a.Bounds(), len(a.Palette))
		}
		cases[i].Digest = pixelDigest(a)
	}
	const path = "testdata/cover_mosaic.golden.json"
	if *update {
		raw, _ := json.MarshalIndent(cases, "", "  ")
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want []mosaicCase
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(cases) {
		t.Fatalf("golden has %d cases, want %d (run with -update)", len(want), len(cases))
	}
	for i := range cases {
		if cases[i] != want[i] {
			t.Errorf("case %d: %+v, golden %+v", i, cases[i], want[i])
		}
	}
}

// A tile's pixels come from the lineage; the cell beyond the parts shows only the ink dot, so every part count differs.
func TestCover_Mosaic_PartCountsDiffer(t *testing.T) {
	t.Parallel()
	seen := map[string]int{}
	for _, n := range []int{0, 1, 2, 3, 4, 5, 9, 10, 12, 13} {
		d := pixelDigest(cover.RenderMosaic(rec("r", 1, n), 64))
		if prev, dup := seen[d]; dup && !(n == 13 && prev == 12) { // parts beyond the twelfth are ignored
			t.Errorf("%d and %d parts render the same", prev, n)
		}
		seen[d] = n
	}
}

func TestCover_RerollChangesLayoutNotPalette(t *testing.T) {
	t.Parallel()
	a, b := cover.RenderMosaic(rec("record-x", 1, 6), 96), cover.RenderMosaic(rec("record-x", 2, 6), 96)
	for i := range a.Palette {
		if a.Palette[i] != b.Palette[i] {
			t.Fatalf("palette entry %d moved with the seed", i)
		}
	}
	if pixelDigest(a) == pixelDigest(b) {
		t.Error("the seed does not change the layout")
	}
	// with no parts the seed drives the filler tiles
	if pixelDigest(cover.RenderMosaic(rec("record-x", 1, 0), 96)) == pixelDigest(cover.RenderMosaic(rec("record-x", 2, 0), 96)) {
		t.Error("the seed does not change filler tiles")
	}
}

func TestCover_PartsOrderMatters(t *testing.T) {
	t.Parallel()
	in := rec("record-x", 1, 6)
	rev := in
	rev.Parts = append([]cover.Part(nil), in.Parts...)
	rev.Parts[0], rev.Parts[1] = rev.Parts[1], rev.Parts[0]
	if pixelDigest(cover.RenderMosaic(in, 96)) == pixelDigest(cover.RenderMosaic(rev, 96)) {
		t.Error("tile order does not change the picture")
	}
	if cover.MosaicKey(in, 96) == cover.MosaicKey(rev, 96) {
		t.Error("tile order does not change the key")
	}
}

func TestCover_RecordsDiffer(t *testing.T) {
	t.Parallel()
	bgs := map[any]bool{}
	digests := map[string]bool{}
	for _, id := range []string{"r1", "r2", "r3", "r4", "r5", "r6"} {
		img := cover.RenderMosaic(rec(id, 1, 5), 64)
		bgs[img.Palette[0]] = true
		digests[pixelDigest(img)] = true
	}
	if len(bgs) < 5 || len(digests) != 6 {
		t.Errorf("%d distinct hues, %d distinct pictures across 6 records", len(bgs), len(digests))
	}
	album := cover.Input{Kind: cover.KindAlbum, ID: "r1", Parts: parts(5), Seed: 1}
	if pixelDigest(cover.RenderMosaic(album, 64)) == pixelDigest(cover.RenderMosaic(rec("r1", 1, 5), 64)) {
		t.Error("kind does not separate identities")
	}
}

func TestCover_MosaicSizeBudget(t *testing.T) {
	t.Parallel()
	for i := range 10 {
		in := rec("record-"+strconv.Itoa(i), int64(1000+37*i), []int{0, 1, 3, 5, 8, 9, 10, 12, 12, 4}[i])
		for _, tc := range []struct{ size, max int }{{512, 40 << 10}, {1024, 96 << 10}} {
			if tc.size == 1024 && i >= 2 { // slow under -race
				continue
			}
			var buf bytes.Buffer
			enc := png.Encoder{CompressionLevel: png.BestCompression}
			if err := enc.Encode(&buf, cover.RenderMosaic(in, tc.size)); err != nil {
				t.Fatal(err)
			}
			if buf.Len() > tc.max {
				t.Errorf("input %d at %d px: %d bytes > %d", i, tc.size, buf.Len(), tc.max)
			}
		}
	}
}

func TestCover_MosaicKeyTable(t *testing.T) {
	t.Parallel()
	base := rec("r", 7, 3)
	k := cover.MosaicKey(base, 512)
	if len(k) != 64 || cover.MosaicKey(rec("r", 7, 3), 512) != k {
		t.Fatalf("key %q is not a stable hex sha256", k)
	}
	if k == cover.Key("r", 7, 512) {
		t.Error("key spaces meet")
	}
	diff := map[string]cover.Input{
		"kind": {Kind: cover.KindAlbum, ID: "r", Seed: 7, Parts: parts(3)},
		"id":   rec("s", 7, 3),
		"seed": rec("r", 8, 3),
		"part": rec("r", 7, 2),
	}
	for name, in := range diff {
		if cover.MosaicKey(in, 512) == k {
			t.Errorf("key ignores the %s", name)
		}
	}
	if cover.MosaicKey(base, 1024) == k {
		t.Error("key ignores the size")
	}
	p := rec("r", 7, 3)
	p.Parts[1].Seed++
	if cover.MosaicKey(p, 512) == k {
		t.Error("key ignores a part seed")
	}
	// only the first twelve parts count
	if cover.MosaicKey(rec("r", 7, 12), 512) != cover.MosaicKey(rec("r", 7, 20), 512) {
		t.Error("parts beyond the twelfth change the key")
	}
	// the separators prevent id/seed ambiguity
	a := cover.Input{Kind: "record", ID: "a", Parts: []cover.Part{{RootID: "b", Seed: 1}}}
	b := cover.Input{Kind: "record", ID: "a", Parts: []cover.Part{{RootID: "b1", Seed: 0}}}
	if cover.MosaicKey(a, 64) == cover.MosaicKey(b, 64) {
		t.Error("part encoding is ambiguous")
	}
}

// TestCover_NoFloatInRenderer: the mosaic files name no floating-point type and no math package (C123).
func TestCover_NoFloatInRenderer(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("mosaic*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no mosaic files: %v", err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			if p := strings.Trim(imp.Path.Value, `"`); p == "math" || strings.HasPrefix(p, "math/") {
				t.Errorf("%s imports %s", f, p)
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				if strings.HasPrefix(x.Name, "float") || x.Name == "unit" || x.Name == "complex64" || x.Name == "complex128" {
					t.Errorf("%s: identifier %s", fset.Position(x.Pos()), x.Name)
				}
			case *ast.BasicLit:
				if x.Kind == token.FLOAT || x.Kind == token.IMAG {
					t.Errorf("%s: literal %s", fset.Position(x.Pos()), x.Value)
				}
			}
			return true
		})
	}
}

// TestCover_M4aGoldenUnchanged: the M4a golden file is byte-identical to the one the extension started from and its
// renderer still reproduces it.
func TestCover_M4aGoldenUnchanged(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/cover.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var want []goldenCase
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(goldenInputs) {
		t.Fatalf("M4a golden has %d cases", len(want))
	}
	for i, c := range goldenInputs {
		c.Digest = pixelDigest(cover.Render(c.Root, c.Seed, c.Size))
		if c != want[i] {
			t.Errorf("M4a case %d moved: %+v, golden %+v", i, c, want[i])
		}
	}
	if cover.Version != "mm-cover/1" || cover.MosaicVersion != "mm-cover/record-1" {
		t.Error("a version string changed")
	}
}

// TestPerf_CoverRender: a 512 px, 12-part mosaic renders well inside a second.
func TestPerf_CoverRender(t *testing.T) {
	budget := perftest.Budget(t, 400*time.Millisecond)
	in := rec("record-perf", 9, 12)
	sw := perftest.Start()
	cover.RenderMosaic(in, 512)
	if el := sw.Elapsed(); el > budget {
		t.Errorf("512 px mosaic took %v (budget %v)", el, budget)
	}
}
