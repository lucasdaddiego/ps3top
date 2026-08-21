//go:build !unix

package main

// termCellAspect has no TIOCGWINSZ to ask here; the default aspect applies.
func termCellAspect() float64 { return 0 }
