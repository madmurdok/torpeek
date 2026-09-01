package frames

import (
	"testing"
	"time"
)

// TestDefaultPlanOnAFeature is the acceptance criterion: a 100-minute file
// yields 20 points between minute 5 and minute 95, evenly spaced.
func TestDefaultPlanOnAFeature(t *testing.T) {
	const duration = 100 * time.Minute

	points, err := DefaultPlan().Points(duration)
	if err != nil {
		t.Fatalf("Points: %v", err)
	}

	if len(points) != 20 {
		t.Fatalf("got %d points, want 20", len(points))
	}
	if points[0] != 5*time.Minute {
		t.Errorf("first point at %s, want 5m", points[0])
	}
	if points[19] != 95*time.Minute {
		t.Errorf("last point at %s, want 95m", points[19])
	}

	// Even spacing: every gap the same, within rounding.
	want := points[1] - points[0]
	for i := 2; i < len(points); i++ {
		gap := points[i] - points[i-1]
		if diff := gap - want; diff > time.Second || diff < -time.Second {
			t.Errorf("gap %d is %s, want about %s", i, gap, want)
		}
	}
	t.Logf("20 points from %s to %s, every %s", points[0], points[19], want)
}

func TestPointsAreOrderedAndInsideTheWindow(t *testing.T) {
	const duration = 2 * time.Hour

	for _, count := range []int{1, 2, 3, 20, 100} {
		plan := DefaultPlan()
		plan.Count = count

		points, err := plan.Points(duration)
		if err != nil {
			t.Fatalf("Points(count=%d): %v", count, err)
		}
		if len(points) != count {
			t.Fatalf("count=%d produced %d points", count, len(points))
		}

		lower := time.Duration(float64(duration) * DefaultStart)
		upper := time.Duration(float64(duration) * DefaultEnd)

		for i, p := range points {
			if p < lower || p > upper {
				t.Errorf("count=%d point %d at %s falls outside %s-%s", count, i, p, lower, upper)
			}
			if i > 0 && p <= points[i-1] {
				t.Errorf("count=%d point %d at %s does not advance past %s", count, i, p, points[i-1])
			}
		}
	}
}

// TestSinglePointLandsMidWindow: one frame is meant to represent the film, and
// its opening seconds do not.
func TestSinglePointLandsMidWindow(t *testing.T) {
	plan := DefaultPlan()
	plan.Count = 1

	points, err := plan.Points(100 * time.Minute)
	if err != nil {
		t.Fatalf("Points: %v", err)
	}
	if got, want := points[0], 50*time.Minute; got != want {
		t.Errorf("single point at %s, want %s", got, want)
	}
}

func TestPlanRejectsNonsense(t *testing.T) {
	cases := []struct {
		name     string
		plan     Plan
		duration time.Duration
	}{
		{name: "no points", plan: Plan{Count: 0, Start: 0.05, End: 0.95}, duration: time.Hour},
		{name: "negative count", plan: Plan{Count: -3, Start: 0.05, End: 0.95}, duration: time.Hour},
		{name: "inverted window", plan: Plan{Count: 5, Start: 0.9, End: 0.1}, duration: time.Hour},
		{name: "empty window", plan: Plan{Count: 5, Start: 0.5, End: 0.5}, duration: time.Hour},
		{name: "window past the end", plan: Plan{Count: 5, Start: 0.05, End: 1.5}, duration: time.Hour},
		{name: "zero duration", plan: DefaultPlan(), duration: 0},
		{name: "negative duration", plan: DefaultPlan(), duration: -time.Hour},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.plan.Points(tc.duration); err == nil {
				t.Error("Points accepted a plan that cannot produce frames")
			}
		})
	}
}

// TestCustomWindow covers the flag path: someone who wants the credits can ask
// for the whole file.
func TestCustomWindow(t *testing.T) {
	plan := Plan{Count: 3, Start: 0, End: 1}

	points, err := plan.Points(60 * time.Second)
	if err != nil {
		t.Fatalf("Points: %v", err)
	}

	want := []time.Duration{0, 30 * time.Second, 60 * time.Second}
	for i := range want {
		if points[i] != want[i] {
			t.Errorf("point %d at %s, want %s", i, points[i], want[i])
		}
	}
}

// TestShortFileStillPlans: a two-minute clip must not collapse into twenty
// identical timestamps.
func TestShortFileStillPlans(t *testing.T) {
	points, err := DefaultPlan().Points(2 * time.Minute)
	if err != nil {
		t.Fatalf("Points: %v", err)
	}

	seen := make(map[time.Duration]bool, len(points))
	for _, p := range points {
		if seen[p] {
			t.Fatalf("duplicate timestamp %s in a short file's plan", p)
		}
		seen[p] = true
	}
}
