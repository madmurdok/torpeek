package frames

import (
	"strconv"
	"strings"
)

// This file decides what to do about a stream's colour, and nothing else. It
// exists because an HDR source read as if it were SDR does not merely look
// different - it looks broken, and it looks broken in a way that points at
// torpeek rather than at the file. Measured on a real HDR10 clip through this
// package's own extractor, one frame at a time:
//
//	                       mean luma   luma spread   colourfulness
//	SDR reference             127.0        65.4          247.7
//	the same picture graded
//	  to HDR10, read as SDR    94.5        24.1           56.2   <- the defect
//	  graded to HLG            190.6       50.9          110.1   <- the defect
//	tone mapped by this file   111.9/131.2 57.4/66.8     191.0/225.5
//
// where colourfulness is the mean of max(r,g,b)-min(r,g,b) over the frame -
// zero for a grey frame, 255 for a fully saturated one. A fifth of the colour
// is exactly the "washed-out grey" a person reports as our bug (TOR-108).
//
// The same measurement on an exact PQ luminance ramp (20 bands of known
// cd/m^2, ST 2084 encoded, read back through the mjpeg encoder) says why the
// untreated frame looks flat rather than merely wrong: 1 cd/m^2 comes out at
// level 37 instead of near black and 10000 cd/m^2 at 253, so the whole range
// is squeezed into the middle - shadows lifted, highlights crushed.

// Transfer characteristics as ffprobe spells them in color_transfer. These two
// are the ones that mean high dynamic range; every other value either is SDR
// already or says nothing to act on.
const (
	transferPQ  = "smpte2084"    // SMPTE ST 2084, i.e. HDR10 and HDR10+
	transferHLG = "arib-std-b67" // ARIB STD-B67, i.e. Hybrid Log-Gamma
)

// referenceWhiteNits is the luminance one unit of linear light stands for
// throughout the chain: 100 cd/m^2, which is SDR reference white. Anchoring
// there is what keeps mid-tones - faces, walls, skin - at the level they would
// have in an SDR grade instead of being scaled by the source's peak.
const referenceWhiteNits = 100

// tonemapPeak is where the roll-off ends, in units of referenceWhiteNits, so
// 10 means 1000 cd/m^2 - the HDR10 baseline mastering peak and HLG's nominal
// reference display peak, which makes one constant serve both branches.
//
// It is passed explicitly rather than left to the filter's own detection. Not
// for a different result today: zscale drops a frame's mastering-display and
// content-light-level side data when it converts to linear light, and once the
// frame is tagged linear rather than ST 2084 the filter's fallback is this
// same 10 - measured, by giving one clip MaxCLL 1000 and another MaxCLL 4000
// and getting byte-identical output from both, on auto and on peak=10 alike.
// The reason to pin it is that this is an accident of one filter dropping side
// data another filter would have read. A future ffmpeg that carried the
// metadata through would silently start rendering every frame of an
// inflated-MaxCLL file darker, and nothing here would say why.
const tonemapPeak = 10

// dolbyVisionICtCpProfiles are the Dolby Vision profiles whose base layer is
// not BT.2020 YUV at all but IPT-PQ-c2, an entirely different colour space
// that only means anything once the stream's RPU has been applied.
//
// No chain torpeek can use on every platform can apply an RPU: that needs
// libplacebo with Dolby Vision support, and libplacebo is configured into the
// LGPL Linux and Windows builds but not into the GPL macOS one, so reaching
// for it would make the picture differ by platform - or fail outright on one
// (the class of problem TOR-93 exists to avoid). What this list is for is the
// other half: refusing to run the HDR10 chain over IPT data. A
// profile 5 base layer put through a BT.2020 PQ tone map does not come out
// closer to right, it comes out confidently wrong in a new direction, and a
// preview that is wrong in a way nobody recognises is worse than one that is
// wrong in a way somebody has already complained about.
var dolbyVisionICtCpProfiles = map[int]bool{4: true, 5: true}

// ColorTags is a stream's colour metadata as ffprobe reports it, verbatim.
// Empty strings mean the field was absent, which is not the same as a value:
// an absent transfer is the main reason a file is left alone.
type ColorTags struct {
	// Transfer, Primaries and Matrix are ffprobe's color_transfer,
	// color_primaries and color_space.
	Transfer, Primaries, Matrix string
	// Range is ffprobe's color_range: "tv" (limited) or "pc" (full).
	Range string
	// DolbyVisionProfile is dv_profile from the stream's Dolby Vision
	// configuration record, or 0 when it carries none.
	DolbyVisionProfile int
}

// ToneMap is a decided colour conversion for one stream. The zero value
// converts nothing, which is what every SDR file gets and what the extractor
// did for every file before this existed.
type ToneMap struct {
	// Source names the stream's dynamic range for whoever reads the manifest:
	// "hdr10", "hlg", "dolby-vision-p5", or empty for ordinary SDR. It is set
	// even when no conversion is possible, because "this is Dolby Vision
	// profile 5" is the answer a person staring at a wrong-looking frame
	// actually needs.
	Source string
	// filter is the -vf argument, empty when nothing is to be done.
	filter string
}

// Applies reports whether frames go through a filter chain.
func (t ToneMap) Applies() bool { return t.filter != "" }

// Filter is the -vf argument for this conversion, given the image format the
// frame will be encoded in. Empty when there is nothing to do.
//
// The output pixel format is the caller's because it is the one step of the
// chain that is about the file we write rather than the file we read: JPEG
// keeps the 4:2:0 subsampling it has always been written with, and PNG stays
// at the 16 bits per channel a 10-bit source already gave it, so turning
// tone mapping on does not quietly change either output's precision.
func (t ToneMap) Filter(format Format) string {
	if t.filter == "" {
		return ""
	}
	return t.filter + "," + outputFormatStep(format)
}

func outputFormatStep(format Format) string {
	if format == PNG {
		return "format=gbrp16le"
	}
	return "format=yuv420p"
}

// ToneMapFor decides what to do about a stream with these colour tags.
//
// The gate is deliberately narrow in one direction and forgiving in the other.
// Narrow: only an explicitly PQ or HLG transfer gets converted, because those
// are the two curves that make a picture look wrong when read as SDR, and a
// file that says nothing about its transfer is left exactly as it was rather
// than guessed at. Forgiving: primaries and matrix are taken from the stream
// when it states them and assumed BT.2020 when it does not, since that is the
// only reading a PQ or HLG stream admits, and plenty of real remuxes tag the
// transfer and leave the other two unspecified. Refusing those would cost the
// fix on the very files it is for.
func ToneMapFor(c ColorTags) ToneMap {
	transfer := strings.ToLower(strings.TrimSpace(c.Transfer))

	var source string
	switch transfer {
	case transferPQ:
		source = "hdr10"
	case transferHLG:
		source = "hlg"
	default:
		// Not high dynamic range as far as the container will say. A Dolby
		// Vision stream that states no transfer at all lands here too, which
		// is the conformant shape of a profile 5 file and the reason it is
		// safe by default.
		if p := c.DolbyVisionProfile; dolbyVisionICtCpProfiles[p] {
			return ToneMap{Source: dolbyVisionSource(p)}
		}
		return ToneMap{}
	}

	if p := c.DolbyVisionProfile; dolbyVisionICtCpProfiles[p] {
		// The stream claims a standard HDR transfer and also carries a Dolby
		// Vision record saying its base layer is IPT-PQ-c2. The record is the
		// more specific statement, so it wins, and no chain runs.
		return ToneMap{Source: dolbyVisionSource(p)}
	}

	return ToneMap{Source: source, filter: chain(transfer, primariesOr(c.Primaries), matrixOr(c.Matrix), rangeOr(c.Range))}
}

func dolbyVisionSource(profile int) string {
	switch profile {
	case 4:
		return "dolby-vision-p4"
	case 5:
		return "dolby-vision-p5"
	}
	return "dolby-vision"
}

// chain builds the filter graph. Every step is here for a reason that can be
// stated, and stating them is the point: this is the part of the fix that is
// easiest to copy from a forum post and hardest to defend afterwards.
//
//	zscale=tin=..:min=..:pin=..:rin=..:t=linear:npl=100
//	    Decode the transfer curve into linear light. The four `in` parameters
//	    are given rather than left to the frame's own tags because that is the
//	    difference between converting what the container says the file is and
//	    converting whatever zscale would have guessed had a tag been missing.
//	    npl fixes the scale: one unit of linear light is 100 cd/m^2, SDR
//	    reference white. HLG needs it as much as PQ - measured on an HLG clip,
//	    npl=100 lands mean luma within 3% of the SDR grade it was made from
//	    while npl=1000 lands 12% dark.
//	format=gbrpf32le
//	    The tone map operates on linear RGB in floating point. Without this it
//	    would run on whatever integer format arrived and clip the values above
//	    reference white that it exists to compress.
//	zscale=p=bt709
//	    Convert BT.2020 primaries to BT.709 in linear light, before the tone
//	    map rather than after, so the roll-off judges each pixel's brightest
//	    component in the gamut the frame is actually going to be written in.
//	    Colours outside BT.709 go negative here and are clipped by the last
//	    step; that hard gamut clip is the only option a build without
//	    libplacebo has, and it is a real limitation - a deeply saturated
//	    BT.2020 red comes out as the nearest BT.709 red rather than being
//	    mapped into gamut.
//	tonemap=tonemap=mobius:desat=0:peak=10
//	    Compress what is left above reference white. mobius is the identity
//	    below its knee and rolls the rest off so that `peak` lands on 1.0,
//	    which is the shape wanted here: mid-tones untouched, highlights kept
//	    apart. Measured on the PQ ramp, output level per input cd/m^2:
//
//	        cd/m^2      1    5   20   50  100  200  400  800 1000 4000
//	        no filter  37   61   89  111  127  146  165  183  190  229
//	        mobius     37   73  130  183  215  235  246  253  255  255
//	        clip       37   73  130  190  255  255  255  255  255  255
//	        hable      25   50   87  123  157  192  223  249  255  255
//
//	    clip keeps mid-tones but flattens everything above reference white
//	    into one value - thirteen of the twenty bands come out identical, so
//	    every highlight in the picture becomes the same white and there is
//	    nothing left to judge. hable darkens the whole picture instead: 100
//	    cd/m^2 renders at 157 where it should be near white. reinhard sits
//	    between the two and separates fewer highlight steps than mobius.
//	    mobius is the only one of the four that leaves the bottom half of the
//	    ramp where clip leaves it and still keeps seven distinguishable steps
//	    between 100 and 1000 cd/m^2.
//	    desat=0 because the filter's own desaturation pulls bright pixels
//	    towards grey, which on this ticket would be fixing a washed-out
//	    picture by washing it out. It costs 5-6% of the colour and buys
//	    nothing here: the roll-off already scales r, g and b together, so hue
//	    survives without it.
//	zscale=t=bt709:m=bt709:r=tv
//	    Back out of linear light into an ordinary SDR frame, and clip whatever
//	    the gamut conversion pushed out of range.
func chain(transfer, primaries, matrix, colorRange string) string {
	return strings.Join([]string{
		"zscale=tin=" + transfer + ":min=" + matrix + ":pin=" + primaries + ":rin=" + colorRange +
			":t=linear:npl=" + strconv.Itoa(referenceWhiteNits),
		"format=gbrpf32le",
		"zscale=p=bt709",
		"tonemap=tonemap=mobius:desat=0:peak=" + strconv.Itoa(tonemapPeak),
		"zscale=t=bt709:m=bt709:r=tv",
	}, ",")
}

// primariesOr, matrixOr and rangeOr fill in what the stream left out. A PQ or
// HLG stream is BT.2020 - there is no other combination in use - and a video
// stream with no stated range is limited.
func primariesOr(s string) string {
	if s = strings.ToLower(strings.TrimSpace(s)); s != "" && s != "unknown" && s != "unspecified" {
		return s
	}
	return "bt2020"
}

func matrixOr(s string) string {
	if s = strings.ToLower(strings.TrimSpace(s)); s != "" && s != "unknown" && s != "unspecified" {
		return s
	}
	return "bt2020nc"
}

func rangeOr(s string) string {
	if s = strings.ToLower(strings.TrimSpace(s)); s == "pc" || s == "full" {
		return "full"
	}
	return "limited"
}
