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

const version = "1.0.0"

func main() {
	host := flag.String("host", os.Getenv("PS3TOP_HOST"), "PS3 address (default: auto-discover; env PS3TOP_HOST)")
	interval := flag.Duration("interval", 15*time.Second, "status poll interval (min 5s)")
	alarm := flag.Int("alarm", 80, "temp alarm threshold, °C (PS3 overheats/shuts down ~85)")
	noArt := flag.Bool("no-art", false, "disable cover art")
	once := flag.Bool("once", false, "print status once and exit (no TUI)")
	ver := flag.Bool("version", false, "print version")
	flag.Parse()

	if *ver {
		fmt.Println("ps3top", version)
		return
	}
	if *interval < 5*time.Second {
		*interval = 5 * time.Second
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

	cli := NewClient(*host)

	if *once {
		runOnce(cli, *host)
		return
	}

	// 30fps caps the renderer's repaint ticker — half the default wakeups, and
	// with a 15s poll cadence even that is mostly idle no-ops.
	m := newModel(cli, *host, *interval, *alarm, !*noArt, cacheDir)
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
	fmt.Printf("CPU %d°C  RSX %d°C  FAN %d%% (%s)  MEM %.1fM  HDD %.1fG\n",
		st.CPUTemp, st.RSXTemp, st.FanPct, st.FanMode, float64(st.MemFreeKB)/1024, st.HDDFreeGB)
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
