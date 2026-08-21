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
	artMaxCols  = 40
	artMinRows  = 7
	artTextRows = 6 // under the box: title (two lines), meta, play total, last played, mounted
	placeholder = '\U0010EEEE'
)

// artBox is the cover box for a terminal of this width and list height.
func artBox(width, listH int) (cols, rows int) {
	cols = clamp(width/6, artMinCols, artMaxCols)
	// 3/5 is a 260×300 portrait's height in rows per column of width, at the
	// cell aspect below — taller than that and no cover could use the rows
	// and a short window gives the width back, since a box wider than its
	// rows can fill only costs the list columns
	rows = clamp(listH-2-artTextRows, artMinRows, (cols*3+4)/5)
	cols = clamp(rows*5/3, artMinCols, cols)
	return cols, rows
}

// cellAspect is a terminal cell's height in units of its width. Most
// monospace faces sit near 1:2; close enough that a cover shaped by it reads
// as the cover, not a squashed one.
const cellAspect = 2

// fitCover sizes an image's placement to fit inside a cols×rows box at its
// own aspect ratio. Kitty scales the image to exactly the cells it's given,
// so the only way to keep a portrait cover portrait is to hand it a portrait
// rectangle.
func fitCover(w, h, cols, rows int) (int, int) {
	if w <= 0 || h <= 0 {
		return cols, rows
	}
	// as wide as the box allows at full height, then shrink to the box width
	c := min(cols, max(1, (rows*cellAspect*w+h/2)/h))
	r := min(rows, max(1, (c*h+w*cellAspect/2)/(w*cellAspect)))
	return c, r
}

// First rows/cols of kitty's rowcolumn-diacritics table — enough for an
// artMaxCols-wide placement.
var diacritics = []rune{
	0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F,
	0x0346, 0x034A, 0x034B, 0x034C, 0x0350, 0x0351, 0x0352, 0x0357,
	0x035B, 0x0363, 0x0364, 0x0365, 0x0366, 0x0367, 0x0368, 0x0369,
	0x036A, 0x036B, 0x036C, 0x036D, 0x036E, 0x036F, 0x0483, 0x0484,
	0x0485, 0x0486, 0x0487, 0x0592, 0x0593, 0x0594, 0x0595, 0x0597,
	0x0598, 0x0599, 0x059C, 0x059D, 0x059E, 0x059F, 0x05A0, 0x05A1,
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
