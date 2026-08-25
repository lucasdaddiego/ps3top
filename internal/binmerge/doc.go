// Package binmerge merges a cue sheet's bin files into a single bin, writing
// a corrected cue; the operation is reversible with Split. It's the prep step
// for ps3top's PSX tab — the PS3's PS1 emulator mounts one bin per disc, while
// Redump dumps arrive as one bin per track — but it knows nothing about the
// console beyond the opt-in PSX checks in run.go, and works on any cue.
//
// One flat package, like internal/ps3top: the tests live in-package and drive
// internals directly. The map:
//
//	cue.go       the parsed sheet: decode, parse, timestamps, cue generation
//	validate.go  sector size, bin sizes, index bounds, and the output guards
//	merge.go     the copying, and the cleanup that follows a failed one
//	run.go       Options and Run — everything cmd/binmerge doesn't do itself
//
// The parser carries every line it doesn't understand (FLAGS, PREGAP, TITLE,
// PERFORMER, ISRC, REM, …) through to the output at the position it was found,
// which is the main thing this does that the original binmerge doesn't.
package binmerge

// Version is binmerge's own, tracked separately from ps3top's build stamp:
// this descends from a Python tool that was already at 2.x when it moved in
// here, and its CLI contract is what that number describes.
const Version = "2.1.0"
