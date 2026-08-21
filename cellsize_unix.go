//go:build unix

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// termCellAspect asks the terminal for its cell shape: TIOCGWINSZ carries the
// window's pixel size next to its cell size on terminals that fill it in
// (kitty and Ghostty do), and height-over-width of one cell is what shapes a
// cover's placement. 0 when the terminal doesn't say — not a tty, or a
// terminal that leaves the pixel fields zero.
func termCellAspect() float64 {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 || ws.Xpixel == 0 || ws.Ypixel == 0 {
		return 0
	}
	cellW := float64(ws.Xpixel) / float64(ws.Col)
	cellH := float64(ws.Ypixel) / float64(ws.Row)
	return cellH / cellW
}
