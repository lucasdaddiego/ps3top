// ps3top is htop for a HEN'd PS3: a bubbletea TUI over webMAN MOD's HTTP
// interface. Status, temps, the running game, and mount/launch/eject of the
// game library, polled gently enough to sit open all day.
//
// The program is one flat package on purpose — the tests live in-package and
// exercise internals directly; main.go at the module root only carries the
// build stamp into Run. The one exception is the webMAN client: the webman
// package at the module root is public since 2026-10-09, so ps3sync and
// ps3run can import it at a pinned version, and webman_bridge.go keeps its
// names here. The map:
//
//	run.go      flags, discovery hand-off, and the program start
//	model.go    the bubbletea model, its messages, and list mechanics
//	update.go   Init/Update/handleKey and the network commands + action gate
//	view.go     every frame of the main screen, styles, shared formatters
//	session.go  what each poll feeds: metric samples and play sessions
//	fan.go      the serialized fan-command queue and its delta reports
//	thermal.go  the full-body thermal screen (t) that fan.go's queue drives
//	webman_bridge.go  the webman types, constants and entry points under the TUI's names
//	help.go     the key table behind the ? screen and --help
//	notify.go   desktop notifications for the alarm and a mid-game outage
//	history.go  the NDJSON play log behind the play totals
//	spark.go    fixed-size metric rings and the sparkline renderer
//	thermallog.go  the per-minute thermal log behind the thermal screen's history page
//	art.go      kitty-graphics cover plumbing (transmit/placeholder/cleanup)
//	cover.go    cover fetch + on-disk PNG cache
//
// The client itself — webman.go (HTTP client + parsers), discover.go (the LAN
// sweep) and patch.go (Sony's title-update index) — is in ../../webman.
package ps3top
