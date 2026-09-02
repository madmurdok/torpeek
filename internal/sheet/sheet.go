// Package sheet composes one video file's produced frames into a single
// contact-sheet image with timecode labels.
//
// It is built at the end of a file's processing, from whatever frames are
// ready on disk (REQUIREMENTS.md section 2.11) - never accumulated in memory
// as frames arrive, so a run that stops partway still gets a sheet from what
// it actually has. A capture point that never produced a frame - a rejected
// seek, an unavailable point, or the tail of a plan a budget stop cut short -
// still gets a tile: a dark placeholder carrying the requested timecode and
// why it is empty, so the grid stays complete and the gap is visible rather
// than silently absent (section 2.3: "the grid tolerates irregularity").
//
// Rendering is pure Go rather than going through ffmpeg: the ffmpeg build
// this project links against has no drawtext filter, so labelling frames has
// nothing to draw text with on that path. golang.org/x/image supplies
// resizing (draw.ApproxBiLinear) and a bitmap font
// (font/basicfont.Face7x13) that needs no font file and no cgo, which keeps
// this project's CGO_ENABLED=0 cross-compilation intact.
package sheet

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png" // registers the PNG decoder for frames.PNG output
	"math"
	"os"
	"time"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/madmurdok/torpeek/internal/manifest"
)

// TileWidth is the width, in pixels, a frame is scaled to on the sheet. Small
// enough that a full plan's worth of tiles (20 by default) stays a
// reasonable file; wide enough that the label text scaled up to it is still
// legible.
const TileWidth = 240

// JPEGQuality is the encoding quality for the assembled sheet.
const JPEGQuality = 85

const (
	padding     = 6
	labelLines  = 2
	labelScale  = 2
	labelMargin = 2
)

var (
	background = color.RGBA{24, 24, 24, 255}
	missingBG  = color.RGBA{48, 24, 24, 255}
	labelBG    = color.RGBA{0, 0, 0, 255}
	labelFG    = color.RGBA{230, 230, 230, 255}
	missingFG  = color.RGBA{240, 160, 160, 255}
)

// glyphHeight is basicfont.Face7x13's own line height (its Metrics().Height,
// which for this face comes out to 13px).
var glyphHeight = basicfont.Face7x13.Metrics().Height.Ceil()

// labelHeight is the height of the two-line label band under a tile.
var labelHeight = labelLines*glyphHeight*labelScale + (labelLines+1)*labelMargin

// Build reads each frame's tile from disk by manifest.Frame.Path and lays
// them into one grid image labelled with timecodes, encoded as JPEG.
//
// points is the full capture plan; records is what processing actually
// produced for it, in the same order. records may be shorter than points -
// a run can stop between capture points on a budget or cancellation - and
// any record can carry no Path - a shifted-to-unavailable point, or a
// rejected seek. Both cases render as a placeholder tile rather than
// failing the sheet.
//
// videoWidth and videoHeight size the tiles (every tile, real or
// placeholder, keeps the same aspect so the grid stays even); when neither
// is known a 16:9 fallback is used.
func Build(points []time.Duration, records []manifest.Frame, videoWidth, videoHeight int) ([]byte, error) {
	if len(points) == 0 {
		return nil, fmt.Errorf("sheet: no capture points to compose")
	}

	tileW := TileWidth
	tileH := tileHeight(videoWidth, videoHeight)

	cols, rows := gridSize(len(points))
	cellW := tileW + padding
	cellH := tileH + labelHeight + padding
	canvas := image.NewRGBA(image.Rect(0, 0, cols*cellW+padding, rows*cellH+padding))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{background}, image.Point{}, draw.Src)

	byIndex := make(map[int]manifest.Frame, len(records))
	for _, r := range records {
		byIndex[r.Index] = r
	}

	for i, requested := range points {
		record, ok := byIndex[i]
		if !ok {
			record = manifest.Frame{
				Index:       i,
				RequestedMS: requested.Milliseconds(),
				Shift:       manifest.ShiftFailed,
				Error:       "not attempted",
			}
		}

		col := i % cols
		row := i / cols
		origin := image.Pt(padding+col*cellW, padding+row*cellH)
		drawTile(canvas, origin, tileW, tileH, record)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: JPEGQuality}); err != nil {
		return nil, fmt.Errorf("encode sheet: %w", err)
	}
	return buf.Bytes(), nil
}

// gridSize picks a roughly-square layout for n tiles: enough columns that
// rows*cols >= n, without leaving more than one row half-empty.
func gridSize(n int) (cols, rows int) {
	cols = int(math.Ceil(math.Sqrt(float64(n))))
	if cols < 1 {
		cols = 1
	}
	rows = int(math.Ceil(float64(n) / float64(cols)))
	return cols, rows
}

// tileHeight derives a tile's height from the video's own aspect ratio, so
// every tile - real frame or placeholder - is the same shape. 16:9 is the
// fallback for the rare case neither dimension probed.
func tileHeight(width, height int) int {
	if width <= 0 || height <= 0 {
		width, height = 16, 9
	}
	h := int(math.Round(float64(TileWidth) * float64(height) / float64(width)))
	if h < 1 {
		h = 1
	}
	return h
}

// drawTile paints one tile - the picture (or a placeholder) plus its label
// band - at origin.
func drawTile(canvas *image.RGBA, origin image.Point, tileW, tileH int, record manifest.Frame) {
	picRect := image.Rectangle{Min: origin, Max: origin.Add(image.Pt(tileW, tileH))}

	if record.Path != "" {
		if tile, err := loadTile(record.Path, tileW, tileH); err == nil {
			draw.Draw(canvas, picRect, tile, image.Point{}, draw.Src)
		} else {
			fillMissing(canvas, picRect)
			record.Error = "unreadable frame: " + err.Error()
		}
	} else {
		fillMissing(canvas, picRect)
	}

	labelRect := image.Rectangle{
		Min: image.Pt(origin.X, picRect.Max.Y),
		Max: image.Pt(origin.X+tileW, picRect.Max.Y+labelHeight),
	}
	drawLabel(canvas, labelRect, tileW, record)
}

// fillMissing paints the placeholder tile for a point with no frame.
func fillMissing(canvas *image.RGBA, rect image.Rectangle) {
	draw.Draw(canvas, rect, image.NewUniform(missingBG), image.Point{}, draw.Src)
}

// loadTile reads and decodes a frame from disk and scales it to (w, h).
func loadTile(path string, w, h int) (image.Image, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("invalid tile size %dx%d", w, h)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	return dst, nil
}

// drawLabel renders the two-line label band: the timecode requested and
// actual (when they differ) on the first line, the shift/error status on
// the second. Text is drawn at the bitmap font's native size and then
// scaled up, since basicfont has no larger size of its own.
func drawLabel(canvas *image.RGBA, rect image.Rectangle, tileW int, record manifest.Frame) {
	draw.Draw(canvas, rect, image.NewUniform(labelBG), image.Point{}, draw.Src)

	fg, status := labelFG, string(record.Shift)
	if record.Path == "" {
		fg = missingFG
		status = record.Error
		if status == "" {
			status = string(record.Shift)
		}
	}
	if status == "" {
		status = "ok"
	}

	line1 := formatTimecode(record.RequestedMS)
	if record.ActualMS != nil && *record.ActualMS != record.RequestedMS {
		line1 = fmt.Sprintf("%s (%s)", line1, formatTimecode(*record.ActualMS))
	}

	y := rect.Min.Y + labelMargin
	drawText(canvas, rect.Min.X+labelMargin, y, tileW-2*labelMargin, line1, labelFG)
	y += glyphHeight*labelScale + labelMargin
	drawText(canvas, rect.Min.X+labelMargin, y, tileW-2*labelMargin, status, fg)
}

// drawText renders s at native bitmap size, then scales it up by
// labelScale and blits it at (x, y), clipped to maxWidth.
func drawText(canvas *image.RGBA, x, y, maxWidth int, s string, fg color.RGBA) {
	if s == "" {
		return
	}

	extent := font.MeasureString(basicfont.Face7x13, s).Ceil()
	small := image.NewRGBA(image.Rect(0, 0, extent, glyphHeight))
	d := &font.Drawer{
		Dst:  small,
		Src:  image.NewUniform(fg),
		Face: basicfont.Face7x13,
		Dot:  fixed.P(0, basicfont.Face7x13.Metrics().Ascent.Ceil()),
	}
	d.DrawString(s)

	scaled := image.NewRGBA(image.Rect(0, 0, extent*labelScale, glyphHeight*labelScale))
	xdraw.NearestNeighbor.Scale(scaled, scaled.Bounds(), small, small.Bounds(), draw.Over, nil)

	w := scaled.Bounds().Dx()
	if w > maxWidth {
		w = maxWidth
	}
	dst := image.Rect(x, y, x+w, y+scaled.Bounds().Dy())
	draw.Draw(canvas, dst, scaled, image.Point{}, draw.Over)
}

// formatTimecode renders milliseconds as mm:ss, or h:mm:ss once an hour is
// reached.
func formatTimecode(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	total := int(d.Round(time.Second).Seconds())
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}
