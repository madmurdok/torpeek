package bridge

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// errUnsatisfiable means the range cannot be served for a resource of this
// size, which is a 416 rather than a 400.
var errUnsatisfiable = errors.New("range not satisfiable")

// byteRange is a resolved, half-open range: [Start, Start+Length).
type byteRange struct {
	Start  int64
	Length int64
}

// End is the last byte index in the range, as HTTP counts it (inclusive).
func (r byteRange) End() int64 { return r.Start + r.Length - 1 }

// contentRange renders the Content-Range header value for a resource of size.
func (r byteRange) contentRange(size int64) string {
	return fmt.Sprintf("bytes %d-%d/%d", r.Start, r.End(), size)
}

// parseRange resolves a single HTTP Range header against a known resource size.
//
// Only one range is accepted. ffmpeg asks for one at a time, and answering a
// multipart range would mean buffering pieces we have no reason to fetch.
func parseRange(header string, size int64) (byteRange, error) {
	spec, ok := strings.CutPrefix(strings.TrimSpace(header), "bytes=")
	if !ok {
		return byteRange{}, fmt.Errorf("unsupported range unit in %q", header)
	}
	if strings.Contains(spec, ",") {
		return byteRange{}, fmt.Errorf("multiple ranges are not supported")
	}

	first, last, ok := strings.Cut(strings.TrimSpace(spec), "-")
	if !ok {
		return byteRange{}, fmt.Errorf("malformed range %q", header)
	}
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)

	// "bytes=-N": the final N bytes. This is how ffmpeg reaches an index that
	// sits at the end of the file, so it has to work.
	if first == "" {
		if last == "" {
			return byteRange{}, fmt.Errorf("malformed range %q", header)
		}
		n, err := strconv.ParseInt(last, 10, 64)
		if err != nil || n < 0 {
			return byteRange{}, fmt.Errorf("malformed suffix range %q", header)
		}
		if n == 0 {
			return byteRange{}, errUnsatisfiable
		}
		if n > size {
			n = size
		}
		return byteRange{Start: size - n, Length: n}, nil
	}

	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 {
		return byteRange{}, fmt.Errorf("malformed range start in %q", header)
	}
	if start >= size {
		return byteRange{}, errUnsatisfiable
	}

	// "bytes=N-": from N to the end.
	if last == "" {
		return byteRange{Start: start, Length: size - start}, nil
	}

	end, err := strconv.ParseInt(last, 10, 64)
	if err != nil || end < start {
		return byteRange{}, fmt.Errorf("malformed range end in %q", header)
	}
	if end >= size {
		end = size - 1
	}
	return byteRange{Start: start, Length: end - start + 1}, nil
}
