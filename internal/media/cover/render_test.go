package cover_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/media/cover"
)

// pixelDigest is the sha256 of the palette (RGBA bytes) followed by the raw pixel indices.
func pixelDigest(img *image.Paletted) string {
	h := sha256.New()
	for _, c := range img.Palette {
		r, g, b, a := c.RGBA()
		h.Write([]byte{byte(r >> 8), byte(g >> 8), byte(b >> 8), byte(a >> 8)})
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		h.Write(img.Pix[(y-b.Min.Y)*img.Stride : (y-b.Min.Y)*img.Stride+b.Dx()])
	}
	return hex.EncodeToString(h.Sum(nil))
}

type goldenCase struct {
	Root   string `json:"root"`
	Seed   int64  `json:"seed"`
	Size   int    `json:"size"`
	Digest string `json:"sha256"`
}

var goldenInputs = []goldenCase{
	{Root: "root-synthetic-a", Seed: 1101, Size: 128},
	{Root: "root-synthetic-b", Seed: 7, Size: 96},
	{Root: "01900000-0000-7000-8000-000000000101", Seed: 0, Size: 64},
}

func TestCover_DeterministicPixels(t *testing.T) {
	t.Parallel()
	got := make([]goldenCase, len(goldenInputs))
	for i, c := range goldenInputs {
		a, b := cover.Render(c.Root, c.Seed, c.Size), cover.Render(c.Root, c.Seed, c.Size)
		if pixelDigest(a) != pixelDigest(b) {
			t.Fatalf("case %d: two renders differ", i)
		}
		if a.Bounds() != image.Rect(0, 0, c.Size, c.Size) || len(a.Palette) != 16 {
			t.Fatalf("case %d: bounds %v palette %d", i, a.Bounds(), len(a.Palette))
		}
		c.Digest = pixelDigest(a)
		got[i] = c
	}
	const path = "testdata/cover.golden.json"
	if *update {
		raw, _ := json.MarshalIndent(got, "", "  ")
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var want []goldenCase
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatalf("golden has %d cases, want %d (run with -update)", len(want), len(got))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("case %d: %+v, golden %+v", i, got[i], want[i])
		}
	}
}

func TestCover_SiblingsSharePalette(t *testing.T) {
	t.Parallel()
	a, b := cover.Render("root-synthetic-a", 1101, 128), cover.Render("root-synthetic-a", 1102, 128)
	if len(a.Palette) != len(b.Palette) {
		t.Fatal("palette sizes differ")
	}
	for i := range a.Palette {
		if a.Palette[i] != b.Palette[i] {
			t.Fatalf("palette entry %d differs between siblings", i)
		}
	}
	diff := 0
	for y := range 128 {
		for x := range 128 {
			if a.ColorIndexAt(x, y) != b.ColorIndexAt(x, y) {
				diff++
			}
		}
	}
	if diff*10 < 128*128 {
		t.Errorf("only %d of %d pixels differ, want at least 10%%", diff, 128*128)
	}
}

func TestCover_RootsDiffer(t *testing.T) {
	t.Parallel()
	seen := map[color.Color]string{}
	for _, root := range []string{"root-a", "root-b", "root-c", "root-d", "root-e", "root-f"} {
		bg := cover.Render(root, 1, 32).Palette[0]
		seen[bg] = root
	}
	if len(seen) < 5 {
		t.Errorf("only %d distinct base hues across 6 roots", len(seen))
	}
}

func TestCover_SizeBudget(t *testing.T) {
	t.Parallel()
	for i := range 10 {
		root := string(rune('a'+i)) + "-synthetic-root"
		seed := int64(1000 + 37*i)
		for _, tc := range []struct{ size, max int }{{512, 40 << 10}, {1024, 96 << 10}} {
			if tc.size == 1024 && i >= 2 { // 4x supersampled 1024 px renders are slow under -race
				continue
			}
			var buf bytes.Buffer
			enc := png.Encoder{CompressionLevel: png.BestCompression}
			if err := enc.Encode(&buf, cover.Render(root, seed, tc.size)); err != nil {
				t.Fatal(err)
			}
			if buf.Len() > tc.max {
				t.Errorf("%s/%d at %d px: %d bytes > %d", root, seed, tc.size, buf.Len(), tc.max)
			}
		}
	}
}
