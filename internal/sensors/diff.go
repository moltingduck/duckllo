package sensors

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	_ "image/jpeg" // decoded baselines may be JPEG
)

// PixelDiff compares two PNG-encoded images and returns:
//   - diffPNG: a PNG where pixels that differ above the per-channel
//     tolerance are tinted red, and matching pixels are dimmed grey.
//   - diffPixels: the count of pixels that exceeded the tolerance.
//   - totalPixels: width * height of the larger image (or the
//     intersection if dimensions disagree).
//
// Tolerance is per-channel 0..255; 16 is a sensible default that ignores
// JPEG-style compression noise. Threshold of 1 is the sum of channel
// deltas above which we mark the pixel as "different".
func PixelDiff(baseline, current []byte, tolerance int) (diffPNG []byte, diffPixels, totalPixels int, err error) {
	a, _, err := image.Decode(bytes.NewReader(baseline))
	if err != nil {
		return nil, 0, 0, err
	}
	b, _, err := image.Decode(bytes.NewReader(current))
	if err != nil {
		return nil, 0, 0, err
	}

	bA := a.Bounds()
	bB := b.Bounds()
	w := min(bA.Dx(), bB.Dx())
	h := min(bA.Dy(), bB.Dy())
	totalPixels = w * h

	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			ar, ag, ab, _ := a.At(bA.Min.X+x, bA.Min.Y+y).RGBA()
			br, bg, bb, _ := b.At(bB.Min.X+x, bB.Min.Y+y).RGBA()
			ar8, ag8, ab8 := uint8(ar>>8), uint8(ag>>8), uint8(ab>>8)
			br8, bg8, bb8 := uint8(br>>8), uint8(bg>>8), uint8(bb>>8)
			diff := absDelta(ar8, br8) + absDelta(ag8, bg8) + absDelta(ab8, bb8)
			if diff > tolerance {
				diffPixels++
				out.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 230})
			} else {
				// Dim the matching pixel so the differing ones stand out.
				gray := uint8((uint16(ar8) + uint16(ag8) + uint16(ab8)) / 3)
				gray = gray/2 + 64 // never fully black
				out.Set(x, y, color.RGBA{R: gray, G: gray, B: gray, A: 255})
			}
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), diffPixels, totalPixels, nil
}

func absDelta(a, b uint8) int {
	if a >= b {
		return int(a - b)
	}
	return int(b - a)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
