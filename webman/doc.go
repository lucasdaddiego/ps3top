// Package webman is the HTTP client and the parsers for webMAN MOD on a
// HEN'd PS3 (the sMAN-skinned 1.47.48 build), the LAN discovery that finds
// the console, and the Sony title-update lookup behind the patch-available
// mark. ps3top's TUI drives it, and it is public so ps3sync and ps3run can
// import it at a pinned version.
//
// The rules it keeps, for every caller: one fresh connection per request
// (webMAN's web server has ~4 session slots, so no keep-alive), a 2 s dial, a
// redirect to another host refused, and every string that came off the wire
// sanitized before it can reach a terminal — all but the fields ParseSFO
// returns, which stay raw until InstalledGame keeps them. One rule the caller
// must keep itself, for the same slots: at most one status request in
// flight; the client enforces nothing there. The map:
//
//	webman.go   the client, the parsers for webMAN's pages, the library sort
//	discover.go the LAN sweep that finds the console, and the last-good cache
//	patch.go    Sony's title-update index, behind a pinned root
//
// SweepPort, LocalNets and PatchLookup are test hooks: a suite points the
// sweep at loopback and stubs the Sony lookup, and nothing else sets them.
package webman
