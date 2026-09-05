package frames

import (
	"bytes"
	"context"
	"image"
	"strings"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/ffmpeg"
	"github.com/madmurdok/torpeek/internal/hdrtest"
)

func TestToneMapForDecidesFromTheColourTags(t *testing.T) {
	cases := []struct {
		name   string
		tags   ColorTags
		source string
		// applies says whether a filter chain is expected at all; readAs is
		// the transfer the chain must claim to be reading, which is the one
		// argument it is worst to get wrong.
		applies bool
		readAs  string
	}{{
		name: "an untagged file is left exactly as it was",
		tags: ColorTags{},
	}, {
		name: "so is an ordinary bt709 file",
		tags: ColorTags{Transfer: "bt709", Primaries: "bt709", Matrix: "bt709", Range: "tv"},
	}, {
		// bt2020-10 is the BT.709 curve with a wide gamut, not HDR. Whatever
		// is wrong with such a file, it is not the flat grey this fix is for,
		// and tone mapping it would darken a picture that needs no darkening.
		name: "and so is a wide-gamut SDR file",
		tags: ColorTags{Transfer: "bt2020-10", Primaries: "bt2020", Matrix: "bt2020nc", Range: "tv"},
	}, {
		name:    "HDR10 is read as PQ",
		tags:    ColorTags{Transfer: "smpte2084", Primaries: "bt2020", Matrix: "bt2020nc", Range: "tv"},
		source:  "hdr10",
		applies: true,
		readAs:  "smpte2084",
	}, {
		name:    "HLG is read as arib-std-b67",
		tags:    ColorTags{Transfer: "arib-std-b67", Primaries: "bt2020", Matrix: "bt2020nc", Range: "tv"},
		source:  "hlg",
		applies: true,
		readAs:  "arib-std-b67",
	}, {
		// Plenty of real remuxes tag the transfer and leave the rest
		// unspecified. Refusing those would cost the fix on the files it is
		// for, and there is no other reading of a PQ stream than BT.2020.
		name:    "a PQ stream that states nothing else is still handled",
		tags:    ColorTags{Transfer: "smpte2084"},
		source:  "hdr10",
		applies: true,
		readAs:  "smpte2084",
	}, {
		name:    "an upper-case transfer is the same transfer",
		tags:    ColorTags{Transfer: "SMPTE2084"},
		source:  "hdr10",
		applies: true,
		readAs:  "smpte2084",
	}, {
		// Profile 8 keeps a BT.2020 PQ base layer, so it is HDR10 as far as
		// anything here is concerned and gets the ordinary treatment.
		name:    "Dolby Vision profile 8 is HDR10 underneath",
		tags:    ColorTags{Transfer: "smpte2084", Primaries: "bt2020", Matrix: "bt2020nc", Range: "tv", DolbyVisionProfile: 8},
		source:  "hdr10",
		applies: true,
		readAs:  "smpte2084",
	}, {
		// The one the naive chain gets wrong. A profile 5 base layer is
		// IPT-PQ-c2, so running the HDR10 chain over it produces a
		// confidently wrong picture rather than a closer one.
		name:   "Dolby Vision profile 5 is named and not converted",
		tags:   ColorTags{Transfer: "smpte2084", Primaries: "bt2020", Matrix: "bt2020nc", Range: "tv", DolbyVisionProfile: 5},
		source: "dolby-vision-p5",
	}, {
		name:   "a conformant profile 5 stream states no transfer and is still named",
		tags:   ColorTags{DolbyVisionProfile: 5},
		source: "dolby-vision-p5",
	}, {
		name:   "profile 4 has the same base layer and the same answer",
		tags:   ColorTags{Transfer: "smpte2084", DolbyVisionProfile: 4},
		source: "dolby-vision-p4",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ToneMapFor(tc.tags)
			if got.Source != tc.source {
				t.Errorf("Source = %q, want %q", got.Source, tc.source)
			}
			if got.Applies() != tc.applies {
				t.Fatalf("Applies() = %v, want %v (filter %q)", got.Applies(), tc.applies, got.Filter(JPEG))
			}
			if !tc.applies {
				if f := got.Filter(JPEG); f != "" {
					t.Errorf("no conversion expected, got filter %q", f)
				}
				return
			}
			filter := got.Filter(JPEG)
			if !strings.Contains(filter, "tin="+tc.readAs+":") {
				t.Errorf("filter does not read the stream as %s: %q", tc.readAs, filter)
			}
			// The stated input parameters are the whole reason this is driven
			// by the probe rather than left to the filter's own guess.
			for _, want := range []string{"min=", "pin=", "rin=", "npl=100", "tonemap=mobius", "peak=10", "desat=0"} {
				if !strings.Contains(filter, want) {
					t.Errorf("filter is missing %q: %q", want, filter)
				}
			}
		})
	}
}

// TestToneMapFilterEndsInTheOutputsOwnFormat pins the one step of the chain
// that is about the file being written rather than the file being read. JPEG
// has always been written 4:2:0 and PNG has always kept 16 bits per channel
// from a 10-bit source; turning tone mapping on must not quietly change
// either.
func TestToneMapFilterEndsInTheOutputsOwnFormat(t *testing.T) {
	tm := ToneMapFor(ColorTags{Transfer: "smpte2084"})

	if got := tm.Filter(JPEG); !strings.HasSuffix(got, ",format=yuv420p") {
		t.Errorf("jpeg chain does not end in yuv420p: %q", got)
	}
	if got := tm.Filter(PNG); !strings.HasSuffix(got, ",format=gbrp16le") {
		t.Errorf("png chain does not end in gbrp16le: %q", got)
	}
	// The zero value is what every SDR file gets, and it must produce no
	// argument at all rather than a no-op filter.
	var none ToneMap
	if got := none.Filter(PNG); got != "" {
		t.Errorf("the zero ToneMap produced a filter: %q", got)
	}
}

// TestToneMapRecoversAnHDRFrame is the ticket. An HDR source read as if it
// were SDR comes out flat and grey; the numbers below are what "grey" means,
// and they have to move.
func TestToneMapRecoversAnHDRFrame(t *testing.T) {
	for _, transfer := range []hdrtest.Transfer{hdrtest.PQ, hdrtest.HLG} {
		t.Run(string(transfer), func(t *testing.T) {
			tools := hdrtest.Tools(t, "../..")
			path := hdrtest.Clip(t, tools, transfer)

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			plain := NewExtractor(tools)
			tm := ToneMapFor(ColorTags{
				Transfer: string(transfer), Primaries: "bt2020", Matrix: "bt2020nc", Range: "tv",
			})
			if !tm.Applies() {
				t.Fatalf("%s was not recognised as high dynamic range", transfer)
			}

			at := 2 * time.Second
			before, err := plain.Frame(ctx, path, at)
			if err != nil {
				t.Fatalf("extract without tone mapping: %v", err)
			}
			after, err := plain.WithToneMap(tm).Frame(ctx, path, at)
			if err != nil {
				t.Fatalf("extract with tone mapping: %v", err)
			}

			// Frames stay at source resolution either way (REQUIREMENTS.md
			// 2.4); a colour fix that silently rescaled would be a different
			// bug.
			if before.Width != after.Width || before.Height != after.Height {
				t.Errorf("resolution changed: %dx%d -> %dx%d",
					before.Width, before.Height, after.Width, after.Height)
			}

			raw, mapped := hdrtest.Measure(t, before.Data), hdrtest.Measure(t, after.Data)
			t.Logf("untreated: luma_mean=%.1f luma_spread=%.1f colorfulness=%.1f",
				raw.LumaMean, raw.LumaSpread, raw.Colorfulness)
			t.Logf("tone mapped: luma_mean=%.1f luma_spread=%.1f colorfulness=%.1f",
				mapped.LumaMean, mapped.LumaSpread, mapped.Colorfulness)

			// Measured on the HDR10 and HLG clips this was developed against,
			// colourfulness went 56.2 -> 191.0 and 110.1 -> 225.5 against an
			// SDR reference of 247.7. The bar is set at 1.5x rather than at
			// those figures because the exact numbers belong to one zimg and
			// one tonemap version; the collapse of colour to a fraction of
			// the reference is the defect, and it has to be gone.
			if mapped.Colorfulness < 1.5*raw.Colorfulness {
				t.Errorf("colourfulness only went %.1f -> %.1f; the frame is still washed out",
					raw.Colorfulness, mapped.Colorfulness)
			}

			// The same picture graded for SDR in the first place, which is
			// what a handled frame should approach. Not asserted tightly -
			// tone mapping is not an inverse, and the roll-off gives up some
			// saturation on purpose - but a frame that came back to within a
			// third of the reference is a frame nobody would call broken.
			reference := hdrtest.Measure(t, sdrReferenceFrame(t, tools))
			t.Logf("SDR reference: luma_mean=%.1f luma_spread=%.1f colorfulness=%.1f",
				reference.LumaMean, reference.LumaSpread, reference.Colorfulness)
			if mapped.Colorfulness < 0.66*reference.Colorfulness {
				t.Errorf("colourfulness reached %.1f against a reference of %.1f",
					mapped.Colorfulness, reference.Colorfulness)
			}

			// Contrast across the range an SDR viewer can see. The untreated
			// frame squeezes 1 to 100 cd/m^2 into a fraction of the levels it
			// should use, which is why it reads as flat rather than merely
			// as wrong.
			rawLevels, mappedLevels := hdrtest.RampLevels(t, before.Data), hdrtest.RampLevels(t, after.Data)
			lo, hi := hdrtest.RampIndexOf(t, 1), hdrtest.RampIndexOf(t, 100)
			rawContrast := rawLevels[hi] - rawLevels[lo]
			mappedContrast := mappedLevels[hi] - mappedLevels[lo]
			t.Logf("1 to 100 cd/m^2 spans %.0f levels untreated, %.0f tone mapped",
				rawContrast, mappedContrast)
			if mappedContrast < 1.4*rawContrast {
				t.Errorf("contrast from 1 to 100 cd/m^2 only went %.0f -> %.0f levels",
					rawContrast, mappedContrast)
			}
		})
	}
}

// sdrReferenceFrame is the fixture's own SDR grade, extracted the same way,
// so "not washed out any more" has something to be measured against rather
// than only a ratio to the defect.
func sdrReferenceFrame(t *testing.T, tools ffmpeg.Tools) []byte {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	frame, err := NewExtractor(tools).Frame(ctx, hdrtest.Clip(t, tools, hdrtest.SDR), 2*time.Second)
	if err != nil {
		t.Fatalf("extract the SDR reference: %v", err)
	}
	return frame.Data
}

// TestToneMapKeepsHighlightsApart is why the operator is mobius and not clip.
//
// Clipping at reference white satisfies every other measurement here - it
// leaves mid-tones exactly where an SDR grade would put them - and then
// renders every highlight in the picture as the same white. On this ramp it
// flattens thirteen of the twenty bands into one value, so a blown sky and a
// lamp become indistinguishable, and judging a release is partly judging its
// highlights. mobius rolls off instead, which costs a little of reference
// white and keeps the bands apart.
func TestToneMapKeepsHighlightsApart(t *testing.T) {
	tools := hdrtest.Tools(t, "../..")
	path := hdrtest.Clip(t, tools, hdrtest.PQ)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	tm := ToneMapFor(ColorTags{Transfer: transferPQ, Primaries: "bt2020", Matrix: "bt2020nc", Range: "tv"})
	frame, err := NewExtractor(tools).WithToneMap(tm).Frame(ctx, path, 2*time.Second)
	if err != nil {
		t.Fatalf("extract with tone mapping: %v", err)
	}

	levels := hdrtest.RampLevels(t, frame.Data)
	from, to := hdrtest.RampIndexOf(t, 100), hdrtest.RampIndexOf(t, 1000)

	distinct := map[int]bool{}
	for i := from; i <= to; i++ {
		// Rounded to whole levels, and to a tolerance of two, so that jpeg
		// ringing on a band edge cannot be counted as a step.
		distinct[int(levels[i])/2] = true
	}
	t.Logf("levels from 100 to 1000 cd/m^2: %v", levels[from:to+1])

	// Five bands sit between 100 and 1000 cd/m^2 inclusive. Four of them
	// staying apart is the difference between a roll-off and a clip.
	if len(distinct) < 4 {
		t.Errorf("only %d distinct levels between 100 and 1000 cd/m^2: %v; highlights are being flattened",
			len(distinct), levels[from:to+1])
	}

	// Monotonic, because a tone curve that folded back on itself would render
	// a brighter part of the picture darker.
	for i := from; i < to; i++ {
		if levels[i+1] < levels[i]-1 {
			t.Errorf("level fell from %.0f to %.0f between %g and %g cd/m^2",
				levels[i], levels[i+1], hdrtest.Ramp[i], hdrtest.Ramp[i+1])
		}
	}
}

// TestAnSDRFileIsUntouched is the other half of the fix: nothing changes for
// the files that were already right, byte for byte and argument for argument.
func TestAnSDRFileIsUntouched(t *testing.T) {
	tools := locateTools(t)
	path := sampleVideo(t, tools)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	e := NewExtractor(tools)
	before, err := e.Frame(ctx, path, 30*time.Second)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	// What an SDR stream's tags decide, put through the same path a real run
	// takes rather than asserted about the zero value.
	tm := ToneMapFor(ColorTags{Transfer: "bt709", Primaries: "bt709", Matrix: "bt709", Range: "tv"})
	after, err := e.WithToneMap(tm).Frame(ctx, path, 30*time.Second)
	if err != nil {
		t.Fatalf("extract with an SDR decision: %v", err)
	}

	if !bytes.Equal(before.Data, after.Data) {
		t.Errorf("an SDR frame came out differently: %d bytes then %d bytes",
			len(before.Data), len(after.Data))
	}
}

// TestToneMapKeepsThePNGsPrecision is the claim in ToneMap.Filter's comment,
// checked rather than asserted. PNG is the format for when a frame is evidence
// about encoding quality, and a 10-bit source has always given it 16 bits per
// channel; a colour fix that quietly dropped it to 8 would be taking away the
// reason to ask for PNG at all.
func TestToneMapKeepsThePNGsPrecision(t *testing.T) {
	tools := hdrtest.Tools(t, "../..")
	path := hdrtest.Clip(t, tools, hdrtest.PQ)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	e := NewExtractor(tools)
	e.Format = PNG
	tm := ToneMapFor(ColorTags{Transfer: transferPQ, Primaries: "bt2020", Matrix: "bt2020nc", Range: "tv"})

	before, err := e.Frame(ctx, path, 2*time.Second)
	if err != nil {
		t.Fatalf("extract a png without tone mapping: %v", err)
	}
	after, err := e.WithToneMap(tm).Frame(ctx, path, 2*time.Second)
	if err != nil {
		t.Fatalf("extract a png with tone mapping: %v", err)
	}

	for _, arm := range []struct {
		name string
		data []byte
	}{{"untreated", before.Data}, {"tone mapped", after.Data}} {
		img, format, err := image.Decode(bytes.NewReader(arm.data))
		if err != nil {
			t.Fatalf("%s png does not decode: %v", arm.name, err)
		}
		if format != "png" {
			t.Fatalf("%s frame is a %s, not a png", arm.name, format)
		}
		// Go's png decoder returns a 64-bit image exactly when the file
		// carries 16 bits per channel, so the concrete type is the bit depth.
		switch img.(type) {
		case *image.RGBA64, *image.NRGBA64, *image.Gray16:
		default:
			t.Errorf("%s png decoded as %T, want 16 bits per channel from a 10-bit source",
				arm.name, img)
		}
	}
}
