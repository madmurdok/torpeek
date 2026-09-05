// Package hdrtest builds high dynamic range fixtures and measures what came
// back out of them, so more than one package can test colour handling against
// the same file and the same numbers.
//
// It is test scaffolding outside a _test.go file for the reason
// internal/torrenttest is: internal/frames needs it to check the filter chain
// and internal/core needs it to check that the chain reaches the right file of
// a run - and two definitions of "colourfulness" would be two tests whose
// results cannot be compared.
//
// The fixtures are built here rather than converted by a filter. The obvious
// way to make one is to render an SDR clip and push it through zscale, which is
// how the HDR10 and HLG clips this was developed against were made. The test
// suite cannot do that: zscale needs libzimg, which all four bundled builds
// have and a developer's own ffmpeg frequently does not - the Homebrew build
// this was written on has none, so every such test would have skipped on the
// machine that needed them most. Nor can it lean on libx265, the other way to
// get a 10-bit HDR-tagged file and a GPL-only external encoder.
//
// So the transfer curves are applied here, straight into a raw yuv420p10le
// frame, and the result is written with ffv1 - a native lossless encoder in
// every ffmpeg there is - into Matroska, which is the one combination measured
// to carry all four colour tags through (mp4 and nut both drop the transfer and
// the primaries for a non-HEVC codec). The picture really is high dynamic
// range: the curve is applied for real, so reading it as SDR really does
// produce the washed-out frame TOR-108 is about.
package hdrtest

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	_ "image/jpeg" // fixtures are measured as the extractor wrote them
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/ffmpeg"
)

// Transfer is which curve a fixture is built with, spelled the way ffprobe
// spells color_transfer so a caller can hand the same string to the code under
// test.
type Transfer string

const (
	// PQ is SMPTE ST 2084, i.e. HDR10.
	PQ Transfer = "smpte2084"
	// HLG is ARIB STD-B67.
	HLG Transfer = "arib-std-b67"
	// SDR is the BT.709 curve and BT.709 primaries: the same picture, already
	// right without any conversion. It is the reference arm - what the other
	// two are supposed to come back to.
	SDR Transfer = "bt709"
)

// Tools locates an ffmpeg that can run a tone map chain, preferring the
// bundled binaries over whatever is on PATH, and skips the calling test when
// no located build can.
//
// The preference is not a convenience. zscale needs libzimg, which all four
// bundled builds are configured with and a developer's own ffmpeg frequently is
// not, so PATH first would mean these tests silently skipping on most machines.
// It is also the better arm to measure: those two files are what a release
// hands to a user, and whether the chain works inside them is the question
// worth a test.
//
// root is the repository root relative to the calling package, e.g. "../..".
func Tools(t *testing.T, root string) ffmpeg.Tools {
	t.Helper()

	bundled := filepath.Join(root, "third_party", "ffmpeg", runtime.GOOS+"-"+runtime.GOARCH)
	tools, err := ffmpeg.LocateIn(bundled)
	if err != nil {
		t.Skipf("no ffmpeg available: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := tools.Run(ctx, "ffmpeg", "-hide_banner", "-filters")
	if err != nil {
		t.Fatalf("list filters: %v", err)
	}
	for _, name := range []string{"zscale", "tonemap", "setparams"} {
		if !bytes.Contains(out, []byte(" "+name+" ")) {
			t.Skipf("%s has no %q filter, so a tone map chain cannot run here; "+
				"run scripts/fetch-ffmpeg.sh to test against the bundled build",
				tools.FFmpeg, name)
		}
	}
	return tools
}

// Fixture geometry. Callers index bands by position, so the layout is
// constants rather than magic numbers inside the generator.
const (
	Width  = 640
	Height = 360
	// The top two thirds are eight saturated colour bands; the bottom third is
	// a neutral luminance ramp.
	rampTop = 240
)

// hues are the colour bands, given as BT.709 linear RGB directions scaled so
// the brightest component reaches the paired luminance in cd/m^2, and converted
// into BT.2020 for the two HDR transfers.
//
// Stated in BT.709 on purpose, and this is the part of the fixture that took
// two attempts. Bands placed at the BT.2020 gamut corners are not washed out
// when read as BT.709 - they are the opposite, oversaturated to the point of
// clipping - so a fixture built that way measures almost no colour loss and the
// test it should fail passes. Real high dynamic range content sits mostly
// inside the BT.709 gamut expressed in BT.2020 coordinates, which is precisely
// the arrangement that comes out grey when nobody converts the primaries.
// Measured: colourfulness went from 32.7 untreated to 116.3 handled once the
// bands were stated this way, against 81.1 and 117.0 when they sat at the
// BT.2020 corners.
var hues = []struct {
	r, g, b float64
	nits    float64
}{
	{1, 1, 1, 100},  // reference white
	{1, 0, 0, 200},  // red
	{0, 1, 0, 200},  // green
	{0, 0, 1, 200},  // blue
	{1, 1, 0, 400},  // yellow
	{0, 1, 1, 600},  // cyan
	{1, 0, 1, 800},  // magenta
	{1, 1, 1, 1000}, // a specular highlight
}

// Ramp are the neutral ramp's luminances in cd/m^2. It reaches from well below
// SDR black to well above SDR white, because the two things worth measuring sit
// at opposite ends: whether the shadows are lifted, and whether anything above
// reference white survives as more than one value.
var Ramp = []float64{
	0.5, 1, 2, 5, 10, 20, 35, 50, 75, 100,
	150, 200, 300, 400, 600, 800, 1000, 1500, 2000, 4000,
}

// RampIndexOf is where in Ramp a given luminance sits, for tests that name a
// level rather than an index.
func RampIndexOf(t *testing.T, nits float64) int {
	t.Helper()
	for i, n := range Ramp {
		if n == nits {
			return i
		}
	}
	t.Fatalf("no %g cd/m^2 band in the fixture ramp", nits)
	return -1
}

// bt709ToBT2020 is the linear RGB gamut conversion between the two, both D65.
func bt709ToBT2020(r, g, b float64) (float64, float64, float64) {
	return 0.6274039*r + 0.3292830*g + 0.0433131*b,
		0.0690973*r + 0.9195404*g + 0.0113623*b,
		0.0163914*r + 0.0880132*g + 0.8955953*b
}

// pqEncode is the SMPTE ST 2084 inverse EOTF: absolute luminance in cd/m^2 to a
// normalised code value. The constants are the standard's.
func pqEncode(nits float64) float64 {
	const (
		m1 = 2610.0 / 16384
		m2 = 2523.0 / 4096 * 128
		c1 = 3424.0 / 4096
		c2 = 2413.0 / 4096 * 32
		c3 = 2392.0 / 4096 * 32
	)
	l := math.Max(nits, 0) / 10000
	lm := math.Pow(l, m1)
	return math.Pow((c1+c2*lm)/(1+c3*lm), m2)
}

// hlgEncode is the ARIB STD-B67 OETF, with luminance taken relative to the 1000
// cd/m^2 nominal peak.
//
// The display-side OOTF is deliberately not applied. A scene-referred signal
// put through this curve is a valid HLG signal, which is what the file has to be
// for a test to mean anything, and inverting the OOTF as well would only change
// which absolute luminance each band claims to be - not whether the frame comes
// out washed out when the curve is ignored.
func hlgEncode(nits float64) float64 {
	const (
		a = 0.17883277
		b = 0.28466892
		c = 0.55991073
	)
	e := math.Min(math.Max(nits, 0)/1000, 1)
	if e <= 1.0/12 {
		return math.Sqrt(3 * e)
	}
	return a*math.Log(12*e-b) + c
}

// bt709Encode is the BT.709 OETF, with 100 cd/m^2 as reference white. Anything
// brighter clips, which is what an SDR grade of this picture would do.
func bt709Encode(nits float64) float64 {
	e := math.Min(math.Max(nits, 0)/100, 1)
	if e < 0.018 {
		return 4.5 * e
	}
	return 1.099*math.Pow(e, 0.45) - 0.099
}

// Frame builds one raw yuv420p10le, limited range frame for the given
// transfer: BT.2020 non-constant luminance for the two HDR curves, BT.709 for
// the reference arm.
func Frame(transfer Transfer) []byte {
	oetf := pqEncode
	kr, kg, kb := 0.2627, 0.6780, 0.0593
	toGamut := bt709ToBT2020
	switch transfer {
	case HLG:
		oetf = hlgEncode
	case SDR:
		oetf = bt709Encode
		kr, kg, kb = 0.2126, 0.7152, 0.0722
		toGamut = func(r, g, b float64) (float64, float64, float64) { return r, g, b }
	}

	// The luma matrix is applied to the encoded components, as both standards
	// specify for non-constant luminance - the same thing a real encoder does.
	encode := func(r, g, b float64) (y, cb, cr float64) {
		rp, gp, bp := oetf(r), oetf(g), oetf(b)
		y = kr*rp + kg*gp + kb*bp
		cb = (bp - y) / (2 * (1 - kb))
		cr = (rp - y) / (2 * (1 - kr))
		return y, cb, cr
	}

	// nitsAt returns the linear RGB, in cd/m^2, of one pixel. The neutral ramp
	// is the same in either gamut, so only the hues are converted.
	nitsAt := func(x, y int) (float64, float64, float64) {
		if y >= rampTop {
			n := Ramp[x*len(Ramp)/Width]
			return n, n, n
		}
		h := hues[x*len(hues)/Width]
		return toGamut(h.r*h.nits, h.g*h.nits, h.b*h.nits)
	}

	clamp10 := func(v float64) uint16 {
		switch {
		case v < 0:
			return 0
		case v > 1023:
			return 1023
		}
		return uint16(math.Round(v))
	}

	var scratch [2]byte
	put := func(dst []byte, v uint16) []byte {
		binary.LittleEndian.PutUint16(scratch[:], v)
		return append(dst, scratch[0], scratch[1])
	}

	luma := make([]byte, 0, Width*Height*2)
	for y := 0; y < Height; y++ {
		for x := 0; x < Width; x++ {
			yv, _, _ := encode(nitsAt(x, y))
			// Limited range 10-bit: 64 is black, 940 is white.
			luma = put(luma, clamp10(yv*876+64))
		}
	}
	// 4:2:0 chroma. Every band boundary here is an even number of pixels, so
	// one sample per 2x2 block is the block's own colour, not a blend.
	cbPlane := make([]byte, 0, Width*Height/2)
	crPlane := make([]byte, 0, Width*Height/2)
	for y := 0; y < Height; y += 2 {
		for x := 0; x < Width; x += 2 {
			_, cb, cr := encode(nitsAt(x, y))
			cbPlane = put(cbPlane, clamp10(cb*896+512))
			crPlane = put(crPlane, clamp10(cr*896+512))
		}
	}

	return append(append(luma, cbPlane...), crPlane...)
}

// WriteClip renders a short clip of the given transfer to path, tagged so
// ffprobe reports the colour metadata the code under test switches on.
//
// seconds decides how much there is to seek into; the picture never changes, so
// a capture point anywhere inside it decodes the same frame and a test does not
// have to care where the planner put it.
func WriteClip(t *testing.T, tools ffmpeg.Tools, transfer Transfer, path string, seconds int) {
	t.Helper()

	raw := filepath.Join(t.TempDir(), "frame.yuv")
	if err := os.WriteFile(raw, Frame(transfer), 0o600); err != nil {
		t.Fatalf("write raw frame: %v", err)
	}

	primaries := "bt2020"
	matrix := "bt2020nc"
	if transfer == SDR {
		primaries, matrix = "bt709", "bt709"
	}

	const fps = 10
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// setparams rather than the -color_* output options: measured, the
	// Matroska muxer writes the range and the matrix from those but leaves the
	// transfer and the primaries unspecified, and the transfer is the whole
	// point of the fixture. Tagging the frames themselves carries all four.
	if _, err := tools.Run(ctx, "ffmpeg",
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "rawvideo", "-pix_fmt", "yuv420p10le",
		"-s", itoa(Width)+"x"+itoa(Height), "-r", itoa(fps), "-i", raw,
		"-vf", "loop=loop="+itoa(seconds*fps-1)+":size=1:start=0,"+
			"setparams=color_primaries="+primaries+":color_trc="+string(transfer)+
			":colorspace="+matrix+":range=tv",
		"-frames:v", itoa(seconds*fps), "-c:v", "ffv1",
		path,
	); err != nil {
		t.Fatalf("render %s fixture: %v", transfer, err)
	}
}

// Clip is WriteClip into a temporary directory, returning the path.
func Clip(t *testing.T, tools ffmpeg.Tools, transfer Transfer) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clip.mkv")
	WriteClip(t, tools, transfer, path, 4)
	return path
}

// Stats are the numbers TOR-108 turns on, all read off the image the extractor
// actually produced rather than off a source frame.
type Stats struct {
	// LumaMean and LumaSpread are BT.601 luminance over the whole frame, on the
	// ordinary 0-255 scale. LumaSpread is the same quantity frames.IsBlank
	// judges a frame by.
	LumaMean, LumaSpread float64
	// Colorfulness is the mean of max(r,g,b)-min(r,g,b): zero for a grey frame,
	// 255 for a fully saturated one. This is the number that separates a
	// washed-out HDR frame from a handled one.
	Colorfulness float64
}

// Measure reads Stats off an encoded image.
func Measure(t *testing.T, data []byte) Stats {
	t.Helper()

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	b := img.Bounds()

	var sum, sumSq, sat float64
	var n int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r16, g16, b16, _ := img.At(x, y).RGBA()
			r, g, bb := float64(r16)/257, float64(g16)/257, float64(b16)/257
			lum := 0.299*r + 0.587*g + 0.114*bb
			sum += lum
			sumSq += lum * lum
			sat += math.Max(r, math.Max(g, bb)) - math.Min(r, math.Min(g, bb))
			n++
		}
	}

	mean := sum / float64(n)
	return Stats{
		LumaMean:     mean,
		LumaSpread:   math.Sqrt(math.Max(sumSq/float64(n)-mean*mean, 0)),
		Colorfulness: sat / float64(n),
	}
}

// RampLevels reads the luminance an image gives each band of the neutral ramp,
// sampling the middle of each band and of the ramp's own height so nothing
// lands on a band edge that chroma subsampling has softened.
func RampLevels(t *testing.T, data []byte) []float64 {
	t.Helper()

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	b := img.Bounds()

	bandWidth := b.Dx() / len(Ramp)
	y := b.Min.Y + (rampTop*b.Dy()/Height+b.Dy())/2

	levels := make([]float64, len(Ramp))
	for i := range levels {
		x := b.Min.X + i*bandWidth + bandWidth/2
		r16, g16, b16, _ := img.At(x, y).RGBA()
		levels[i] = (0.299*float64(r16) + 0.587*float64(g16) + 0.114*float64(b16)) / 257
	}
	return levels
}

// itoa keeps the geometry above readable as numbers rather than as strings that
// happen to look like them.
func itoa(n int) string { return strconv.Itoa(n) }
