package ps3top

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Build is the version stamp main hands in, set there via -ldflags (see the
// Makefile). The defaults are what a plain `go build` or `go run .` produces,
// and they say so rather than naming a release the source may have moved
// well past — a bug report quoting a version that doesn't match the code
// costs more than it saves.
type Build struct {
	Version, Commit, Date string
}

func (b Build) String() string {
	if b.Version == "dev" {
		return fmt.Sprintf("ps3top %s (%s, built %s)", b.Version, b.Commit, b.Date)
	}
	return fmt.Sprintf("ps3top %s (%s)", b.Version, b.Commit)
}

// keysHelp rides along on --help: the keybar documents the keys too, but only
// from inside the TUI, and the narrow-terminal keybar can't fit them all.
// This is the one place every key is listed.
const keysHelp = `
Keys (TUI):
  ↑↓/jk move · ⇥/←→ console tab · ⏎ mount (mounted → launch) · p play
  u eject · / fuzzy filter · s sort alpha↔recent · m popup message on the TV
  r refresh status+game list · g rescan library (webMAN re-scans the ISO dirs)
  t thermal screen (there: ↑↓/+/− fan step · f fan mode · h long-term history · r refresh · esc back)
  x quit the running game to the XMB · X restart it · S shutdown · R restart · q quit
`

// usage is flag.CommandLine's Usage — split out so the suite can render it
// without driving main through a --help parse.
func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, "Usage: ps3top [flags]\n\nFlags:\n")
	flag.PrintDefaults()
	fmt.Fprint(out, keysHelp)
}

// Run is the program: flags, discovery, and the TUI until it quits. It parses
// flag.CommandLine, so main owns os.Args and nothing else.
func Run(build Build) error {
	host := flag.String("host", os.Getenv("PS3TOP_HOST"), "PS3 address (default: auto-discover; env PS3TOP_HOST)")
	interval := flag.Duration("interval", 15*time.Second, "status poll interval (min 5s)")
	alarm := flag.Int("alarm", 80, "temp alarm threshold, °C (PS3 overheats/shuts down ~85)")
	noArt := flag.Bool("no-art", false, "disable cover art")
	ver := flag.Bool("version", false, "print version")
	flag.CommandLine.Usage = usage
	flag.Parse()

	if *ver {
		fmt.Println(build)
		return nil
	}
	if *interval < 5*time.Second {
		*interval = 5 * time.Second
	}

	dataRoot, err := dataDir()
	if err != nil {
		return err
	}

	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		cacheRoot = os.TempDir()
	}
	cacheRoot = filepath.Join(cacheRoot, "ps3top")
	cacheDir := filepath.Join(cacheRoot, "covers")
	hostCache := filepath.Join(cacheRoot, "host")
	os.MkdirAll(cacheDir, 0o755)

	// With no --host, a cached address is taken on trust: the first poll is
	// the verification, and a stale cache is re-swept in the background while
	// the dashboard already shows. That saves the verify-then-poll double
	// request every launch used to make, and it's what lets ps3top start
	// before the console is on.
	auto := *host == ""
	if auto {
		h, err := discoverHost(hostCache)
		if err != nil {
			return err
		}
		*host = h
	}
	// validate before anything is sent: a host carrying a path or scheme would
	// otherwise prefix every endpoint and fail as if the console were at fault
	normHost, err := normalizeHost(*host)
	if err != nil {
		return fmt.Errorf("--host: %w", err)
	}
	*host = normHost

	cli := NewClient(*host)

	hist, err := loadHistory(dataRoot)
	if err != nil {
		// not fatal: the TUI works fine without totals, and refusing to start
		// over a history problem is worse than starting without one
		fmt.Fprintln(os.Stderr, "ps3top: history:", err)
	}

	// 30fps caps the renderer's flush ticker — half the default wakeups, and
	// with a 15s poll cadence even that is mostly idle no-ops.
	m := newModel(cli, *host, *interval, *alarm, !*noArt, cacheDir, hist)
	m.tlog = newThermalLog(dataRoot)
	if auto {
		m.hostCache = hostCache
	}
	_, err = tea.NewProgram(m, tea.WithFPS(30)).Run()
	return err
}
