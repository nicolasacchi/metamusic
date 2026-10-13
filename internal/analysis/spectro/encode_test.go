package spectro

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand/v2"
	"testing"
)

func noiseImage() *image.RGBA {
	r := rand.New(rand.NewPCG(1, 2))
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for i := 0; i < len(img.Pix); i += 4 {
		v := uint8(r.UintN(256))
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = v, uint8(r.UintN(256)), uint8(r.UintN(256)), 255
	}
	return img
}

func TestEncodeBudget(t *testing.T) {
	t.Parallel()
	img := noiseImage()
	qs := []int{85, 75, 65, 55, 45}
	size := map[int]int{}
	for _, q := range qs {
		var b bytes.Buffer
		if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: q}); err != nil {
			t.Fatal(err)
		}
		size[q] = b.Len()
	}
	if !(size[65] > size[55] && size[55] > size[45]) {
		t.Fatalf("sizes not decreasing: %v", size)
	}
	jpg, q, err := EncodeBudget(img, size[55], qs)
	if err != nil || q != 55 || len(jpg) != size[55] {
		t.Fatalf("q = %d len = %d err = %v", q, len(jpg), err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(jpg)); err != nil {
		t.Fatal(err)
	}
	_, _, err = EncodeBudget(img, 100, qs)
	if !errors.Is(err, ErrTooLarge) || Code(err) != "SPECTROGRAM_TOO_LARGE" {
		t.Errorf("err = %v", err)
	}
	_ = color.RGBA{}
}
