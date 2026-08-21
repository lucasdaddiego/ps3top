// ps3top is htop for a HEN'd PS3: a bubbletea TUI over webMAN MOD's HTTP
// interface. Status, temps, the running game, and mount/launch/eject of the
// game library, polled gently enough to sit open all day.
//
// The program is one flat package on purpose — the tests live in-package and
// exercise internals directly; main.go at the module root only carries the
// build stamp into Run. The map:
//
//	run.go      flags, discovery hand-off, and the program start
//	model.go    the bubbletea model, its messages, and list mechanics
//	update.go   Init/Update/handleKey and the network commands + action gate
//	view.go     every frame of the main screen, styles, shared formatters
//	session.go  what each poll feeds: metric samples and play sessions
//	fan.go      the serialized fan-command queue and its delta reports
//	thermal.go  the full-body thermal screen (t) that fan.go's queue drives
//	webman.go   HTTP client + parsers for webMAN's pages
//	discover.go LAN sweep that finds the console when --host is absent
//	history.go  the NDJSON play log behind the play totals
//	spark.go    fixed-size metric rings and the sparkline renderer
//	art.go      kitty-graphics cover plumbing (transmit/placeholder/cleanup)
//	cover.go    cover fetch + on-disk PNG cache
package ps3top
