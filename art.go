package main

// Cover art via the kitty graphics protocol in Unicode-placeholder mode
// (supported by kitty and Ghostty). Images are transmitted once (id-tagged,
// q=2 so the terminal stays quiet), bound to a virtual placement, and then
// referenced from the View as placeholder cells — which survive bubbletea's
// cell-based redraws, unlike direct placements.
//
// The escape sequences themselves go out through tea.Raw, which bubbletea
// flushes from the same ticker as the frame, ahead of it — so a transmission
// can't interleave with a repaint, and a frame that references an image is
// never written before the image is.

import (
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"strings"
)

// The art box. Covers come in two shapes — 320×176 ICON0s and 260×300
// multiMAN JPEGs, per the covers source on /setup.ps3 — and each is placed at
// its own aspect inside the box (fitCover) rather than stretched to it. The
// box itself is sized from the terminal: a wide window earns a wider cover,
// and the box is as tall as the panel's text leaves room for, capped where a
// portrait cover at that width would already be filling it.
const (
	artMinCols  = 24
	artMaxCols  = 64 // the diacritics table is the ceiling
	artMinRows  = 7
	artTextRows = 3 // under the box: title, meta, play total + last played
	placeholder = '\U0010EEEE'
)

// Portrait cover proportions, for sizing the box: multiMAN's are 260×300.
const portraitW, portraitH = 260, 300

// artBox is the cover box for a terminal of this width and list height, at a
// given cell aspect, beside a list that needs listW columns to show its rows
// whole: every column the list doesn't need goes to the cover.
func artBox(width, listH int, aspect float64, listW int) (cols, rows int) {
	cols = clamp(width-listW-4, artMinCols, artMaxCols) // 4: box border + gap
	// taller than a portrait cover at this width and no cover could use the
	// rows; and a short window gives the width back, since a box wider than
	// its rows can fill only costs the list columns
	rows = clamp(listH-2-artTextRows, artMinRows, rowsFor(cols, portraitW, portraitH, aspect))
	cols = clamp(colsFor(rows, portraitW, portraitH, aspect), artMinCols, cols)
	return cols, rows
}

// defaultCellAspect is a terminal cell's height in units of its width when
// the terminal won't say: most monospace faces sit near 1:2.
const defaultCellAspect = 2.0

// colsFor is how many columns an image of w×h pixels spans at rows tall;
// rowsFor the converse. Both round, both floor at one cell.
func colsFor(rows, w, h int, aspect float64) int {
	return max(1, int(math.Round(float64(rows)*aspect*float64(w)/float64(h))))
}

func rowsFor(cols, w, h int, aspect float64) int {
	return max(1, int(math.Round(float64(cols)*float64(h)/(float64(w)*aspect))))
}

// fitCover sizes an image's placement to fit inside a cols×rows box at its
// own aspect ratio. Kitty scales the image to fit the cells it's given —
// letterboxing inside them if the rectangle is the wrong shape — so the
// placement has to be the image's shape at the terminal's real cell aspect,
// or the blank strip ends up inside the placement where nothing can center
// it.
func fitCover(w, h, cols, rows int, aspect float64) (int, int) {
	if w <= 0 || h <= 0 {
		return cols, rows
	}
	// as wide as the box allows at full height, then shrink to the box width
	c := min(cols, colsFor(rows, w, h, aspect))
	r := min(rows, rowsFor(c, w, h, aspect))
	return c, r
}

// First rows/cols of kitty's rowcolumn-diacritics table (gen/rowcolumn-
// diacritics.txt in the kitty source, same order) — enough for an
// artMaxCols-wide placement.
var diacritics = []rune{
	0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F,
	0x0346, 0x034A, 0x034B, 0x034C, 0x0350, 0x0351, 0x0352, 0x0357,
	0x035B, 0x0363, 0x0364, 0x0365, 0x0366, 0x0367, 0x0368, 0x0369,
	0x036A, 0x036B, 0x036C, 0x036D, 0x036E, 0x036F, 0x0483, 0x0484,
	0x0485, 0x0486, 0x0487, 0x0592, 0x0593, 0x0594, 0x0595, 0x0597,
	0x0598, 0x0599, 0x059C, 0x059D, 0x059E, 0x059F, 0x05A0, 0x05A1,
	0x05A8, 0x05A9, 0x05AB, 0x05AC, 0x05AF, 0x05C4, 0x0610, 0x0611,
	0x0612, 0x0613, 0x0614, 0x0615, 0x0616, 0x0617, 0x0657, 0x0658,
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

// placementEscape binds image id to a virtual placement of cols×rows cells.
// Sent once with the image, and again on its own whenever the box changes
// size: the terminal keeps the pixels, only the placement is replaced.
func placementEscape(id, cols, rows int) string {
	return fmt.Sprintf("\x1b_Ga=p,i=%d,U=1,p=1,c=%d,r=%d,q=2\x1b\\", id, cols, rows)
}

// transmitEscapes encodes a PNG as an id-tagged kitty image plus a virtual
// placement of cols×rows cells.
func transmitEscapes(id int, png []byte, cols, rows int) string {
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
	sb.WriteString(placementEscape(id, cols, rows))
	return sb.String()
}

// placementRow renders one row of cols placeholder cells for image id (id
// must be ≤255: it is carried in the 256-color foreground).
func placementRow(id, row, cols int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "\x1b[38;5;%dm", id)
	for c := 0; c < cols; c++ {
		sb.WriteRune(placeholder)
		sb.WriteRune(diacritics[row])
		sb.WriteRune(diacritics[c])
	}
	sb.WriteString("\x1b[39m")
	return sb.String()
}

// deleteAllImages frees every transmitted image on exit, so the terminal's
// store doesn't keep ~225KB per browsed cover until the window closes.
const deleteAllImages = "\x1b_Ga=d,d=A,q=2\x1b\\"
