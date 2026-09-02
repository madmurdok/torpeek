// Package frames plans capture points and turns them into images: even
// spacing inside a configurable window of the duration, blank frame rejection,
// shifting away from pieces the swarm cannot serve, and the min-time /
// min-traffic profiles. Decoding happens by invoking the bundled ffmpeg
// against a bridge URL, never by linking a decoder.
//
// Requirements: sections 2.3, 2.5 and 4.
package frames

import (
	"fmt"
	"time"
)

// Plan says how many frames to take and over which part of the running time.
type Plan struct {
	// Count is how many capture points to produce.
	Count int
	// Start and End bound the window as fractions of the duration. The
	// defaults trim the edges because the first and last percent of a film
	// are usually a black frame, a distributor logo or closing credits -
	// technically valid frames that tell a viewer nothing.
	Start, End float64
}

// Defaults from REQUIREMENTS.md section 7.
const (
	DefaultCount = 20
	DefaultStart = 0.05
	DefaultEnd   = 0.95
)

// DefaultPlan is 20 points spread across the middle 90% of a file.
func DefaultPlan() Plan {
	return Plan{Count: DefaultCount, Start: DefaultStart, End: DefaultEnd}
}

// Validate reports whether the plan can produce points at all.
func (p Plan) Validate() error {
	if p.Count < 1 {
		return fmt.Errorf("count must be at least 1, got %d", p.Count)
	}
	if p.Start < 0 || p.End > 1 {
		return fmt.Errorf("window %.2f-%.2f falls outside the file", p.Start, p.End)
	}
	if p.Start >= p.End {
		return fmt.Errorf("window start %.2f is not before end %.2f", p.Start, p.End)
	}
	return nil
}

// Points spreads Count timestamps evenly across the window, inclusive of both
// ends, so a contact sheet opens near the beginning and closes near the end.
//
// A single point lands in the middle of the window rather than at its start:
// one frame is meant to represent the film, and its opening seconds do not.
func (p Plan) Points(duration time.Duration) ([]time.Duration, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if duration <= 0 {
		return nil, fmt.Errorf("duration must be positive, got %s", duration)
	}

	first := time.Duration(float64(duration) * p.Start)
	last := time.Duration(float64(duration) * p.End)

	if p.Count == 1 {
		return []time.Duration{first + (last-first)/2}, nil
	}

	points := make([]time.Duration, p.Count)
	step := float64(last-first) / float64(p.Count-1)
	for i := range points {
		points[i] = first + time.Duration(float64(i)*step)
	}
	// Guard against float drift pushing the final point past the window.
	points[p.Count-1] = last

	return points, nil
}
