package main

// Cover art via the kitty graphics protocol in Unicode-placeholder mode
// (supported by kitty and Ghostty). Images are transmitted once (id-tagged,
// q=2 so the terminal stays quiet), bound to a virtual placement, and then
// referenced from the View as placeholder cells — which survive bubbletea's
// cell-based redraws, unlike direct placements.

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	artCols     = 24
	artRows     = 7
	placeholder = '\U0010EEEE'
)

// First rows/cols of kitty's rowcolumn-diacritics table — enough for a
// artCols-wide placement.
var diacritics = []rune{
	0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F,
	0x0346, 0x034A, 0x034B, 0x034C, 0x0350, 0x0351, 0x0352, 0x0357,
	0x035B, 0x0363, 0x0364, 0x0365, 0x0366, 0x0367, 0x0368, 0x0369,
	0x036A, 0x036B, 0x036C, 0x036D, 0x036E, 0x036F, 0x0483, 0x0484,
}

// Only kitty and Ghostty implement Unicode-placeholder (U=1) placements —
// WezTerm speaks the kitty graphics protocol but not this mode, so it is
// deliberately NOT detected here (placeholder cells would render as garbage).
func artSupported() bool {
	term := os.Getenv("TERM")
	prog := os.Getenv("TERM_PROGRAM")
	return strings.Contains(term, "kitty") || strings.Contains(term, "ghostty") ||
		prog == "ghostty" || prog == "kitty"
}

// rawWrite serializes out-of-band escape writes (image transmissions, bell)
// against each other. A frame race with bubbletea's renderer is theoretically
// possible but transmissions are rare (once per cover) and self-heal on the
// next repaint.
var (
	outMu    sync.Mutex
	quitting atomic.Bool
)

func rawWrite(s string) {
	// a cover download finishing after quit must not re-add an image to the
	// terminal's store post-cleanup (it would leak there until window close)
	if quitting.Load() {
		return
	}
	outMu.Lock()
	defer outMu.Unlock()
	os.Stdout.WriteString(s)
}

// transmitEscapes encodes a PNG as an id-tagged kitty image plus a virtual
// placement scaled to the art panel box.
func transmitEscapes(id int, png []byte) string {
	b64 := base64.StdEncoding.EncodeToString(png)
	var sb strings.Builder
	first := true
	for len(b64) > 0 {
		n := min(4096, len(b64))
		chunk, rest := b64[:n], b64[n:]
		m := 1
		if len(rest) == 0 {
			m = 0
		}
		if first {
			fmt.Fprintf(&sb, "\x1b_Gf=100,t=d,i=%d,q=2,m=%d;%s\x1b\\", id, m, chunk)
			first = false
		} else {
			fmt.Fprintf(&sb, "\x1b_Gm=%d;%s\x1b\\", m, chunk)
		}
		b64 = rest
	}
	fmt.Fprintf(&sb, "\x1b_Ga=p,i=%d,U=1,p=1,c=%d,r=%d,q=2\x1b\\", id, artCols, artRows)
	return sb.String()
}

// placementRow renders one row of placeholder cells for image id (id must be
// ≤255: it is carried in the 256-color foreground).
func placementRow(id, row int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "\x1b[38;5;%dm", id)
	for c := 0; c < artCols; c++ {
		sb.WriteRune(placeholder)
		sb.WriteRune(diacritics[row])
		sb.WriteRune(diacritics[c])
	}
	sb.WriteString("\x1b[39m")
	return sb.String()
}

// clearImages politely frees all transmitted images on exit. It flips the
// quitting gate first, then waits on the mutex — so an in-flight transmission
// either finished before the delete-all (and is cleaned by it) or is dropped.
func clearImages() {
	quitting.Store(true)
	outMu.Lock()
	defer outMu.Unlock()
	os.Stdout.WriteString("\x1b_Ga=d,d=A,q=2\x1b\\")
}
