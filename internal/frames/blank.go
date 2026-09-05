package frames

import (
	"bytes"
	"fmt"
	"image"
	"math"
)

// blankSpreadThreshold is the minimum standard deviation, in ordinary 0-255
// luminance units, a frame's sampled pixels must have to count as a real
// picture rather than a black or near-monotone one.
//
// Measured on fixtures built the same way the extractor produces frames -
// ffmpeg mjpeg at quality 2, decoded back with this package's own sampler
// (see blank_test.go):
//
//	a pure black frame (lavfi color=black, and a libx264-encoded clip's
//	  black lead-in seeked and decoded through the real pipeline)  stddev = 0.00
//	a flat grey card (lavfi color=gray), even re-encoded at a much
//	  worse jpeg quality (q:v 20 instead of 2)                     stddev = 0.00
//	a genuinely dark but real scene (testsrc with its brightness
//	  driven down until mean luminance bottoms out around 35-37,
//	  which still carries the source's gradients and color bars)   stddev = 19.8-28.8
//	an ordinary testsrc frame, raw or after a full encode/seek/
//	  decode round trip through a real container                   stddev = 81.4-81.6
//
// A flat frame lands exactly on 0: with nothing to encode, mjpeg's DCT
// leaves no ringing to sample. The dark-but-real floor sits at ~20, so 5.0
// keeps a wide margin on both sides - four times the flat ceiling, and well
// under a third of the dark-scene floor - without brushing up against
// either. This is the discriminator REQUIREMENTS.md 2.3 asks for: mean
// brightness alone would flag the dark scene along with the black one (see
// docs/results/0.1.0-acceptance.md), spread tells them apart.
//
// High dynamic range was measured against this line while TOR-108 was being
// fixed, because a badly handled HDR frame is flatter than a handled one and
// the worry was that it could be rejected here for being "blank" when the
// real cause was colour. It does not happen, on any source that could be
// built - but the margin is worth writing down, because it is the one place
// where the two tickets touch:
//
//	                                            untreated   tone mapped
//	an ordinary HDR10 frame                        23.2         54.4
//	an ordinary HLG frame                          49.6         63.0
//	a dark HDR10 scene graded to ~5 cd/m^2         22.5         21.9
//	the same scene graded to ~1 cd/m^2              8.6          6.2
//	(the dark-but-real SDR control, for scale)     21.5           -
//
// Two things follow. Tone mapping moves an ordinary HDR frame strongly away
// from the threshold, which is the expected direction. It moves a genuinely
// dark HDR scene slightly *towards* it - correctly, since a scene mastered
// under a couple of cd/m^2 really is nearly black - and that last row sits at
// 1.2x the threshold where the SDR control sits at 4.3x. Nothing crossed, so
// no capture point was stepped for the wrong reason either before or after;
// a scene darker still would be judged blank, and would deserve to be.
const blankSpreadThreshold = 5.0

// blankGrid is how many samples are taken per axis when judging a frame.
// 16x16 = 256 samples is enough to catch real picture structure without
// walking every pixel of a full-resolution frame.
const blankGrid = 16

// IsBlank reports whether a decoded frame is black or near-monotone: the
// spread of sampled luminance across the frame is too small for there to be
// any real picture in it. See blankSpreadThreshold for how that line was
// chosen and measured.
func IsBlank(data []byte) (bool, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return false, fmt.Errorf("decode frame: %w", err)
	}

	bounds := img.Bounds()
	if bounds.Dx() == 0 || bounds.Dy() == 0 {
		return false, fmt.Errorf("decoded frame has no pixels")
	}

	var sum, sumSq float64
	var n int
	for gy := 0; gy < blankGrid; gy++ {
		y := bounds.Min.Y + bounds.Dy()*gy/blankGrid
		for gx := 0; gx < blankGrid; gx++ {
			x := bounds.Min.X + bounds.Dx()*gx/blankGrid
			r, g, b, _ := img.At(x, y).RGBA()
			// img.At returns 16-bit-scaled components; ITU-R BT.601 weights
			// give luminance on the same scale.
			lum := 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
			sum += lum
			sumSq += lum * lum
			n++
		}
	}

	mean := sum / float64(n)
	variance := sumSq/float64(n) - mean*mean
	if variance < 0 {
		// Only possible from float rounding when the frame is perfectly flat.
		variance = 0
	}
	// Scale the 16-bit sample range back down to 8-bit terms, so the
	// threshold reads as an ordinary 0-255 luminance spread.
	stddev := math.Sqrt(variance) / 257.0

	return stddev < blankSpreadThreshold, nil
}
