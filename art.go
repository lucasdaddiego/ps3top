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
// cover-pack JPEGs — so the box is tall enough for a portrait and wide enough
// for a landscape, and each image is placed at its own aspect inside it
// (fitCover) rather than stretched to the box.
const (
	artCols     = 24
	artRows     = 10
	placeholder = '\U0010EEEE'
)

// cellAspect is a terminal cell's height in units of its width. Most
// monospace faces sit near 1:2; close enough that a cover shaped by it reads
// as the cover, not a squashed one.
const cellAspect = 2

// fitCover sizes an image's placement to fit inside the art box at its own
// aspect ratio. Kitty scales the image to exactly the cells it's given, so
// the only way to keep a portrait cover portrait is to hand it a portrait
// rectangle.
func fitCover(w, h int) (cols, rows int) {
	if w <= 0 || h <= 0 {
		return artCols, artRows
	}
	// as wide as the box allows at full height, then shrink to the box width
	cols = min(artCols, max(1, (artRows*cellAspect*w+h/2)/h))
	rows = min(artRows, max(1, (cols*h+w*cellAspect/2)/(w*cellAspect)))
	return cols, rows
}

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
	fmt.Fprintf(&sb, "\x1b_Ga=p,i=%d,U=1,p=1,c=%d,r=%d,q=2\x1b\\", id, cols, rows)
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
