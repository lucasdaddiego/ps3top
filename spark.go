package main

// Metric history. ps3top already polls every interval all day, so keeping the
// last histLen samples costs nothing extra on the wire and turns three
// instantaneous numbers into a shape you can read: settled, climbing, or the
// fan finally ramping.

import "strings"

const (
	histLen = 240 // ~1h at the default 15s cadence
	sparkW  = 8   // columns; a full ring is then ~7min per column

	// trendGap is how far back the trend compares to (~10min at 15s), and
	// trendWarm is the minimum history before a trend is shown at all —
	// without it the first two polls always look like a dramatic move.
	trendGap  = 40
	trendWarm = 8
	trendMin  = 3 // °/points below this is sensor noise, not a trend
)

var blocks = []rune("▁▂▃▄▅▆▇█")

// series is a fixed ring of the last histLen samples. unknown entries are kept
// as gaps rather than dropped, so the x axis stays evenly spaced in time; every
// reader skips them.
type series struct {
	v [histLen]int
	n int // total pushes ever; the ring holds the last min(n, histLen)

	// win is the window materialized oldest-first, rebuilt lazily after a
	// push. A frame reads each ring several times (bare and sparked metric
	// lines, the trend, the thermal plot), and the ring only changes once a
	// poll — so the copy is made once per poll, not once per read.
	win   []int
	stale bool
}

func (s *series) push(v int) {
	s.v[s.n%histLen] = v
	s.n++
	s.stale = true
}

// samples returns the window oldest-first. Shared, not copied: readers only
// read it, and it's replaced (never edited) on the next push.
func (s *series) samples() []int {
	if !s.stale {
		return s.win
	}
	n := min(s.n, histLen)
	out := make([]int, 0, n)
	for i := s.n - n; i < s.n; i++ {
		out = append(out, s.v[i%histLen])
	}
	s.win, s.stale = out, false
	return out
}

// spark compresses the whole window into w columns, each the mean of its
// bucket — a full ring shows the last hour, not the last two minutes at
// pointless resolution.
//
// The scale is the window's own min/max widened to at least minSpan and
// centered: raw auto-scale turns 1° of sensor noise into a cliff, a fixed
// 0–100 scale flattens a real 8° climb into one block, and centering makes a
// steady reading sit mid-height instead of pinned to the floor. Buckets with
// no valid sample render blank.
func spark(vals []int, w, minSpan int) string {
	if w <= 0 || len(vals) == 0 {
		return ""
	}
	lo, hi, have := 0, 0, false
	for _, v := range vals {
		if v == unknown {
			continue
		}
		switch {
		case !have:
			lo, hi, have = v, v, true
		case v < lo:
			lo = v
		case v > hi:
			hi = v
		}
	}
	if !have {
		return ""
	}
	span := max(hi-lo, minSpan)
	base := lo - (span-(hi-lo))/2

	cols := min(w, len(vals))
	var sb strings.Builder
	for c := 0; c < cols; c++ {
		start, end := c*len(vals)/cols, (c+1)*len(vals)/cols
		sum, n := 0, 0
		for _, v := range vals[start:end] {
			if v != unknown {
				sum, n = sum+v, n+1
			}
		}
		if n == 0 {
			sb.WriteRune(' ')
			continue
		}
		lvl := (sum/n - base) * (len(blocks) - 1) / span
		sb.WriteRune(blocks[clamp(lvl, 0, len(blocks)-1)])
	}
	return sb.String()
}

// trend is the change between the newest sample and the one ~trendGap polls
// back, or 0 when the history is too short or the move is inside the noise
// band. A rising 74° matters more than a settled 76°, which is the whole point
// of keeping history.
func trend(vals []int) int {
	if len(vals) < trendWarm {
		return 0
	}
	last, ok := lastValid(vals)
	if !ok {
		return 0
	}
	ref, ok := firstValidFrom(vals, max(0, len(vals)-1-trendGap))
	if !ok {
		return 0
	}
	if d := last - ref; d <= -trendMin || d >= trendMin {
		return d
	}
	return 0
}

func lastValid(vals []int) (int, bool) {
	for i := len(vals) - 1; i >= 0; i-- {
		if vals[i] != unknown {
			return vals[i], true
		}
	}
	return 0, false
}

func firstValidFrom(vals []int, i int) (int, bool) {
	for ; i < len(vals); i++ {
		if vals[i] != unknown {
			return vals[i], true
		}
	}
	return 0, false
}
