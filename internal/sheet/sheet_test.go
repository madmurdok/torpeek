package sheet

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/madmurdok/torpeek/internal/manifest"
)

// writeTestFrame writes a small solid-colour JPEG to dir and returns its
// path, standing in for a real decoded video frame.
func writeTestFrame(t *testing.T, dir string, index int, c color.Color) string {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			img.Set(x, y, c)
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode test frame: %v", err)
	}

	path := filepath.Join(dir, fmt.Sprintf("frame-%d.jpg", index))
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write test frame: %v", err)
	}
	return path
}

func decodeSheet(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode sheet: %v", err)
	}
	return img
}

// TestBuildFullSet is the ordinary case: every point produced a frame.
func TestBuildFullSet(t *testing.T) {
	dir := t.TempDir()
	n := 6
	points := make([]time.Duration, n)
	records := make([]manifest.Frame, n)
	for i := 0; i < n; i++ {
		at := time.Duration(i) * 10 * time.Second
		points[i] = at
		actual := at.Milliseconds()
		records[i] = manifest.Frame{
			Index:       i,
			RequestedMS: at.Milliseconds(),
			ActualMS:    &actual,
			Path:        writeTestFrame(t, dir, i, color.RGBA{uint8(i * 30), 100, 150, 255}),
			Width:       320,
			Height:      180,
		}
	}

	data, err := Build(points, records, 1920, 1080)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	img := decodeSheet(t, data)
	cols, rows := gridSize(n)
	wantW := cols*(TileWidth+padding) + padding
	wantH := rows*(tileHeight(1920, 1080)+labelHeight+padding) + padding
	if got := img.Bounds().Dx(); got != wantW {
		t.Errorf("sheet width = %d, want %d", got, wantW)
	}
	if got := img.Bounds().Dy(); got != wantH {
		t.Errorf("sheet height = %d, want %d", got, wantH)
	}
}

// TestBuildPartialSet is the acceptance criterion for TOR-16: a sheet must
// be produced from a partial frame set without failing on gaps. It mixes
// every kind of gap the plan can leave behind:
//   - a point explicitly marked unavailable (ShiftFailed, no path),
//   - a point that shifted or stepped but still has a real frame,
//   - the tail of the plan simply missing from records, as a run cut short
//     by a budget stop or cancellation leaves it.
func TestBuildPartialSet(t *testing.T) {
	dir := t.TempDir()
	points := []time.Duration{
		1 * time.Second, 2 * time.Second, 3 * time.Second,
		4 * time.Second, 5 * time.Second, 6 * time.Second, 7 * time.Second,
	}

	actual2 := points[2].Milliseconds()
	actual4 := points[4].Milliseconds() + 500

	records := []manifest.Frame{
		{Index: 0, RequestedMS: points[0].Milliseconds(), Shift: manifest.ShiftFailed, Error: "seek_failed"},
		{Index: 1, RequestedMS: points[1].Milliseconds(), Shift: manifest.ShiftFailed, Error: "pieces_unavailable"},
		{
			Index: 2, RequestedMS: points[2].Milliseconds(), ActualMS: &actual2,
			Path: writeTestFrame(t, dir, 2, color.RGBA{10, 200, 10, 255}), Width: 320, Height: 180,
		},
		{
			Index: 3, RequestedMS: points[3].Milliseconds(), Shift: manifest.ShiftFailed, Error: "blank_after_retries",
		},
		{
			Index: 4, RequestedMS: points[4].Milliseconds(), ActualMS: &actual4, Shift: manifest.ShiftBlank,
			Path: writeTestFrame(t, dir, 4, color.RGBA{200, 10, 200, 255}), Width: 320, Height: 180,
		},
		// Indices 5 and 6 are absent entirely - the run stopped before
		// reaching them.
	}

	data, err := Build(points, records, 1920, 1080)
	if err != nil {
		t.Fatalf("Build did not tolerate a partial frame set: %v", err)
	}

	img := decodeSheet(t, data)
	cols, rows := gridSize(len(points))
	wantW := cols*(TileWidth+padding) + padding
	wantH := rows*(tileHeight(1920, 1080)+labelHeight+padding) + padding
	if got := img.Bounds().Dx(); got != wantW {
		t.Errorf("sheet width = %d, want %d", got, wantW)
	}
	if got := img.Bounds().Dy(); got != wantH {
		t.Errorf("sheet height = %d, want %d", got, wantH)
	}
}

// TestBuildEmptyRecords covers a file whose every point failed: still a
// grid-shaped sheet, not an error.
func TestBuildEmptyRecords(t *testing.T) {
	points := []time.Duration{1 * time.Second, 2 * time.Second, 3 * time.Second}

	data, err := Build(points, nil, 0, 0)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	decodeSheet(t, data)
}

// TestBuildRejectsEmptyPlan: nothing to compose is a real error, not a
// silently empty image.
func TestBuildRejectsEmptyPlan(t *testing.T) {
	if _, err := Build(nil, nil, 0, 0); err == nil {
		t.Error("Build with no points: want an error, got nil")
	}
}

// TestBuildToleratesUnreadableFrame: a path that is set but cannot be
// decoded (corrupt, deleted after the manifest recorded it) must still
// produce a sheet rather than fail the whole file.
func TestBuildToleratesUnreadableFrame(t *testing.T) {
	points := []time.Duration{1 * time.Second, 2 * time.Second}
	records := []manifest.Frame{
		{Index: 0, RequestedMS: points[0].Milliseconds(), Path: filepath.Join(t.TempDir(), "gone.jpg")},
		{Index: 1, RequestedMS: points[1].Milliseconds(), Path: filepath.Join(t.TempDir(), "not-an-image.jpg")},
	}
	if err := os.WriteFile(records[1].Path, []byte("not a jpeg"), 0o644); err != nil {
		t.Fatalf("write bogus frame: %v", err)
	}

	data, err := Build(points, records, 1920, 1080)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	decodeSheet(t, data)
}

func TestGridSizeIsRoughlySquareAndCoversEveryPoint(t *testing.T) {
	for n := 1; n <= 30; n++ {
		cols, rows := gridSize(n)
		if cols*rows < n {
			t.Errorf("gridSize(%d) = %dx%d, covers only %d cells", n, cols, rows, cols*rows)
		}
		if cols < 1 || rows < 1 {
			t.Errorf("gridSize(%d) = %dx%d, want positive dimensions", n, cols, rows)
		}
	}
}

func TestFormatTimecode(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "00:00"},
		{61_000, "01:01"},
		{3_600_000, "1:00:00"},
		{3_661_000, "1:01:01"},
	}
	for _, c := range cases {
		if got := formatTimecode(c.ms); got != c.want {
			t.Errorf("formatTimecode(%d) = %q, want %q", c.ms, got, c.want)
		}
	}
}
