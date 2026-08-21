//go:build !unix

package ps3top

// termCellAspect has no TIOCGWINSZ to ask here; the default aspect applies.
func termCellAspect() float64 { return 0 }
