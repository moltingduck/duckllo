package sensors

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// makePNG returns a PNG of `w x h` filled with a single colour.
func makePNG(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func TestPixelDiff_Identical(t *testing.T) {
	red := color.RGBA{200, 50, 50, 255}
	a := makePNG(t, 32, 32, red)
	b := makePNG(t, 32, 32, red)

	_, diff, total, err := PixelDiff(a, b, 16)
	if err != nil {
		t.Fatalf("PixelDiff: %v", err)
	}
	if total != 32*32 {
		t.Errorf("total: got %d want %d", total, 32*32)
	}
	if diff != 0 {
		t.Errorf("identical images should yield 0 diff pixels, got %d", diff)
	}
}

func TestPixelDiff_FullyDifferent(t *testing.T) {
	a := makePNG(t, 16, 16, color.RGBA{0, 0, 0, 255})
	b := makePNG(t, 16, 16, color.RGBA{255, 255, 255, 255})
	_, diff, total, err := PixelDiff(a, b, 16)
	if err != nil {
		t.Fatalf("PixelDiff: %v", err)
	}
	if diff != total {
		t.Errorf("fully-different images should diff every pixel, got %d/%d", diff, total)
	}
}

func TestPixelDiff_TolerancePasses(t *testing.T) {
	// Channel deltas of 5 each → sum = 15 which is below tolerance 16.
	a := makePNG(t, 8, 8, color.RGBA{100, 100, 100, 255})
	b := makePNG(t, 8, 8, color.RGBA{105, 105, 105, 255})
	_, diff, _, err := PixelDiff(a, b, 16)
	if err != nil {
		t.Fatalf("PixelDiff: %v", err)
	}
	if diff != 0 {
		t.Errorf("tolerance-bound delta should not count as a diff; got %d", diff)
	}

	// Same delta, lower tolerance → every pixel counts.
	_, diff2, _, err := PixelDiff(a, b, 5)
	if err != nil {
		t.Fatalf("PixelDiff: %v", err)
	}
	if diff2 == 0 {
		t.Error("tighter tolerance should flag the delta")
	}
}

func TestPixelDiff_DifferentSizesUseIntersection(t *testing.T) {
	a := makePNG(t, 10, 10, color.RGBA{0, 0, 0, 255})
	b := makePNG(t, 5, 5, color.RGBA{0, 0, 0, 255})
	diffPNG, diff, total, err := PixelDiff(a, b, 16)
	if err != nil {
		t.Fatalf("PixelDiff: %v", err)
	}
	if total != 25 {
		t.Errorf("intersection should be 5x5, total got %d", total)
	}
	if diff != 0 {
		t.Errorf("identical pixels in intersection should not diff, got %d", diff)
	}
	// Output PNG should decode and have the intersection size.
	out, err := png.Decode(bytes.NewReader(diffPNG))
	if err != nil {
		t.Fatalf("decode diff PNG: %v", err)
	}
	bnd := out.Bounds()
	if bnd.Dx() != 5 || bnd.Dy() != 5 {
		t.Errorf("diff PNG bounds: %v want 5x5", bnd)
	}
}

func TestPixelDiff_RejectsBadInput(t *testing.T) {
	_, _, _, err := PixelDiff([]byte("not a png"), []byte("also not"), 16)
	if err == nil {
		t.Error("expected an error decoding garbage")
	}
}
