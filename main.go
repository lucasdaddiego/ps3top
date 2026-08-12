package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Set via -ldflags at build time (see the Makefile). The defaults are what a
// plain `go build` or `go run .` produces, and they say so rather than naming a
// release the source may have moved well past — a bug report quoting a version
// that doesn't match the code costs more than it saves.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func versionString() string {
	if version == "dev" {
		return fmt.Sprintf("ps3top %s (%s, built %s)", version, commit, date)
	}
	return fmt.Sprintf("ps3top %s (%s)", version, commit)
}

func main() {
	host := flag.String("host", os.Getenv("PS3TOP_HOST"), "PS3 address (default: auto-discover; env PS3TOP_HOST)")
	interval := flag.Duration("interval", 15*time.Second, "status poll interval (min 5s)")
	alarm := flag.Int("alarm", 80, "temp alarm threshold, °C (PS3 overheats/shuts down ~85)")
	noArt := flag.Bool("no-art", false, "disable cover art")
	once := flag.Bool("once", false, "print status once and exit (no TUI)")
	stats := flag.Bool("stats", false, "print play history and exit (no console needed)")
	ver := flag.Bool("version", false, "print version")
	flag.Parse()

	if *ver {
		fmt.Println(versionString())
		return
	}
	if *interval < 5*time.Second {
		*interval = 5 * time.Second
	}

	dataRoot, err := dataDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ps3top:", err)
		os.Exit(1)
	}

	// --stats reads the local log only, so it works with the console off
	if *stats {
		runStats(dataRoot)
		return
	}

	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		cacheRoot = os.TempDir()
	}
	cacheRoot = filepath.Join(cacheRoot, "ps3top")
	cacheDir := filepath.Join(cacheRoot, "covers")
	os.MkdirAll(cacheDir, 0o755)

	if *host == "" {
		h, err := discoverHost(filepath.Join(cacheRoot, "host"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "ps3top:", err)
			os.Exit(1)
		}
		*host = h
	}
	// validate before anything is sent: a host carrying a path or scheme would
	// otherwise prefix every endpoint and fail as if the console were at fault
	normHost, err := normalizeHost(*host)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ps3top: --host:", err)
		os.Exit(1)
	}
	*host = normHost

	cli := NewClient(*host)

	if *once {
		runOnce(cli, *host)
		return
	}

	hist, err := loadHistory(dataRoot)
	if err != nil {
		// not fatal: the TUI works fine without totals, and refusing to start
		// over a history problem is worse than starting without one
		fmt.Fprintln(os.Stderr, "ps3top: history:", err)
	}

	// 30fps caps the renderer's repaint ticker — half the default wakeups, and
	// with a 15s poll cadence even that is mostly idle no-ops.
	m := newModel(cli, *host, *interval, *alarm, !*noArt, cacheDir, hist)
	if _, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithFPS(30)).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "ps3top:", err)
		os.Exit(1)
	}
}

func runOnce(cli *Client, host string) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	st, err := cli.Status(ctx)
	if err != nil {
		fmt.Printf("ps3top — %s OFFLINE (%v)\n", host, err)
		os.Exit(1)
	}
	fmt.Printf("ps3top — %s ONLINE · FW %s · webMAN %s\n", host, st.Firmware, st.WMVersion)
	switch {
	case st.InGame:
		fmt.Printf("In game: %s [%s]\n", st.GameTitle, st.GameID)
	case st.MountedISO != "":
		fmt.Printf("XMB — mounted: %s\n", path.Base(st.MountedISO))
	default:
		fmt.Println("XMB — no disc mounted")
	}
	fmt.Printf("CPU %s  RSX %s  FAN %s (%s)  MEM %s  HDD %s\n",
		plainInt(st.CPUTemp, "°C"), plainInt(st.RSXTemp, "°C"), plainInt(st.FanPct, "%"),
		st.FanMode, plainMem(st.MemFreeKB), plainHDD(st.HDDFreeGB))
	if n := st.missing(); n > 0 {
		fmt.Printf("warning: %d of %d status fields not found — webMAN markup may have changed\n", n, statusFields)
	}
	if st.Uptime != "" {
		fmt.Printf("up %s", st.Uptime)
		if st.PlayTime != "" {
			fmt.Printf("  play %s", st.PlayTime)
		}
		fmt.Println()
	}
	if st.Lifetime != "" {
		fmt.Println("lifetime:", st.Lifetime)
	}

	games, err := cli.Games(ctx)
	if err != nil {
		fmt.Println("games: error:", err)
		return
	}
	psx := 0
	for _, g := range games {
		if g.IsPSX() {
			psx++
		}
	}
	fmt.Printf("games: %d (%d PS3, %d PSX)\n", len(games), len(games)-psx, psx)
}

// --once is scripting-facing, so an absent field prints ASCII "n/a" rather
// than the TUI's em dash.
func plainInt(v int, unit string) string {
	if v == unknown {
		return "n/a"
	}
	return fmt.Sprintf("%d%s", v, unit)
}

func plainMem(kb int) string {
	if kb == unknown {
		return "n/a"
	}
	return fmt.Sprintf("%.1fM", float64(kb)/1024)
}

func plainHDD(gb float64) string {
	if gb == unknown {
		return "n/a"
	}
	return fmt.Sprintf("%.1fG", gb)
}
