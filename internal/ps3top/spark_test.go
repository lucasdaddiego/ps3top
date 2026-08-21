package ps3top

import (
	"strings"
	"testing"
)

func fill(v ...int) *series {
	s := &series{}
	for _, x := range v {
		s.push(x)
	}
	return s
}

func TestSeriesRingKeepsNewest(t *testing.T) {
	s := &series{}
	for i := 0; i < histLen+50; i++ {
		s.push(i)
	}
	got := s.samples()
	if len(got) != histLen {
		t.Fatalf("len = %d, want %d", len(got), histLen)
	}
	// oldest-first, and the 50 pushed past capacity dropped off the front
	if got[0] != 50 || got[len(got)-1] != histLen+49 {
		t.Errorf("window = [%d..%d], want [50..%d]", got[0], got[len(got)-1], histLen+49)
	}
}

func TestSeriesShortWindow(t *testing.T) {
	if got := fill(1, 2, 3).samples(); len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("samples = %v, want [1 2 3]", got)
	}
	if got := (&series{}).samples(); len(got) != 0 {
		t.Errorf("empty series = %v, want []", got)
	}
}

// A steady reading must look steady, and it must sit mid-height rather than
// pinned to the floor — otherwise "idle" and "as cold as it's ever been" draw
// the same picture.
func TestSparkFlatIsFlatAndCentered(t *testing.T) {
	out := []rune(spark([]int{60, 60, 60, 60, 60, 60, 60, 60}, sparkW, tempSpan))
	if len(out) != sparkW {
		t.Fatalf("width = %d, want %d", len(out), sparkW)
	}
	for _, r := range out[1:] {
		if r != out[0] {
			t.Fatalf("flat input drew a slope: %q", string(out))
		}
	}
	mid := blocks[len(blocks)/2]
	if out[0] < blocks[2] || out[0] > mid+1 {
		t.Errorf("flat line at %q, want mid-height near %q", string(out[0]), string(mid))
	}
}

// The span floor is the whole reason sensor jitter doesn't read as a crisis.
func TestSparkNoiseStaysShallowButRealClimbSweeps(t *testing.T) {
	noise := spark([]int{59, 60, 59, 60, 61, 60, 59, 60}, sparkW, tempSpan)
	climb := spark([]int{58, 60, 63, 66, 69, 72, 75, 78}, sparkW, tempSpan)

	if lo, hi := runeRange(noise); hi-lo > 2 {
		t.Errorf("2° of jitter spans %d levels (%q) — span floor not applied", hi-lo, noise)
	}
	if lo, hi := runeRange(climb); hi-lo < 5 {
		t.Errorf("20° climb only spans %d levels (%q)", hi-lo, climb)
	}
	// and a climb has to actually go up
	c := []rune(climb)
	if c[0] >= c[len(c)-1] {
		t.Errorf("climb doesn't rise: %q", climb)
	}
}

func TestSparkBucketsWholeWindow(t *testing.T) {
	// 80 samples into 8 columns: the first column must reflect the oldest
	// tenth, not the newest — the point is an hour of context, not 2 minutes
	var vals []int
	for i := 0; i < 80; i++ {
		vals = append(vals, 50+i/8) // 50,50…51,51…57
	}
	out := []rune(spark(vals, sparkW, tempSpan))
	if len(out) != sparkW {
		t.Fatalf("width = %d, want %d", len(out), sparkW)
	}
	for i := 1; i < len(out); i++ {
		if out[i] < out[i-1] {
			t.Errorf("monotonic input drew a dip at %d: %q", i, string(out))
		}
	}
}

func TestSparkGapsAndEmpty(t *testing.T) {
	if got := spark(nil, sparkW, tempSpan); got != "" {
		t.Errorf("nil = %q, want empty", got)
	}
	if got := spark([]int{unknown, unknown}, sparkW, tempSpan); got != "" {
		t.Errorf("all-unknown = %q, want empty", got)
	}
	// an offline stretch reads as a hole, not as silently compressed history
	got := spark([]int{60, 60, unknown, unknown, 60, 60, 60, 60}, sparkW, tempSpan)
	if strings.Count(got, " ") != 2 {
		t.Errorf("gaps = %q, want 2 blanks", got)
	}
	if len([]rune(got)) != sparkW {
		t.Errorf("width = %d, want %d", len([]rune(got)), sparkW)
	}
}

func TestSparkNarrowerThanWindow(t *testing.T) {
	if got := spark([]int{60, 62, 64}, sparkW, tempSpan); len([]rune(got)) != 3 {
		t.Errorf("3 samples drew %d columns (%q), want 3", len([]rune(got)), got)
	}
	if got := spark([]int{60}, 0, tempSpan); got != "" {
		t.Errorf("zero width = %q, want empty", got)
	}
}

func TestTrend(t *testing.T) {
	rising := make([]int, 60)
	for i := range rising {
		rising[i] = 50 + i/4 // +15 across the window
	}
	if d := trend(rising); d < trendMin {
		t.Errorf("rising trend = %d, want >= %d", d, trendMin)
	}

	falling := make([]int, 60)
	for i := range falling {
		falling[i] = 75 - i/4
	}
	if d := trend(falling); d > -trendMin {
		t.Errorf("falling trend = %d, want <= -%d", d, trendMin)
	}

	// 2° of drift is sensor noise and must not draw an arrow
	noisy := []int{60, 61, 60, 62, 61, 60, 61, 62, 61, 60, 61, 62}
	if d := trend(noisy); d != 0 {
		t.Errorf("noise trend = %d, want 0", d)
	}

	// the first couple of polls always look dramatic — suppress until warm
	if d := trend([]int{50, 80}); d != 0 {
		t.Errorf("cold-start trend = %d, want 0", d)
	}
	if d := trend(nil); d != 0 {
		t.Errorf("empty trend = %d, want 0", d)
	}

	// an entirely blank window has nothing to compare
	gaps := make([]int, 60)
	for i := range gaps {
		gaps[i] = unknown
	}
	if d := trend(gaps); d != 0 {
		t.Errorf("all-unknown trend = %d, want 0", d)
	}
}

func TestTrendMark(t *testing.T) {
	if trendMark(0) != "" {
		t.Error("flat trend drew a mark")
	}
	if !strings.Contains(trendMark(6), "↗") {
		t.Error("rising trend missing ↗")
	}
	if !strings.Contains(trendMark(-6), "↘") {
		t.Error("falling trend missing ↘")
	}
}

// runeRange returns the lowest and highest block levels present.
func runeRange(s string) (int, int) {
	lo, hi := len(blocks), -1
	for _, r := range s {
		for i, b := range blocks {
			if r == b {
				if i < lo {
					lo = i
				}
				if i > hi {
					hi = i
				}
			}
		}
	}
	return lo, hi
}
