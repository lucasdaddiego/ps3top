package main

// Play history — the record the PS3 itself never keeps.
//
// Session length comes from webMAN's own PlayTime field, not from a clock we
// start ourselves: it is how long the current game has been running, so a
// session is right even when ps3top launches halfway through one or is
// restarted mid-game. That is what removes the need for an open-session
// sidecar file to survive a crash.
//
// A session that both starts and ends while ps3top isn't running is simply
// never seen. That's inherent to polling and not worth chasing.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// sessionRec is one line of history.ndjson. Append-only, one JSON object per
// line, so a forward-incompatible future field can't corrupt older records and
// an unparseable line is skipped rather than fatal.
type sessionRec struct {
	ID        string    `json:"id,omitempty"`
	Title     string    `json:"title"`
	End       time.Time `json:"end"`
	Secs      int       `json:"secs"`
	PeakCPU   int       `json:"peak_cpu,omitempty"`
	PeakRSX   int       `json:"peak_rsx,omitempty"`
	HDDFreeGB float64   `json:"hdd_free_gb,omitempty"`
}

// gameStat carries Title/ID so --stats can name a game from the log alone,
// with the console off and no mygames.xml in reach.
type gameStat struct {
	Title    string
	ID       string
	Secs     int
	Sessions int
	Last     time.Time
	PeakCPU  int
}

type history struct {
	path              string
	stats             map[string]gameStat // keyed by statKey
	first             time.Time           // oldest record seen
	firstHDD, lastHDD float64
}

// statKey identifies a game across sessions. Title IDs are the stable handle,
// but PSX entries in mygames.xml carry none, so those fall back to the title.
func statKey(id, title string) string {
	if id != "" {
		return id
	}
	return "title:" + sortKey(title)
}

// dataDir is deliberately NOT the cache dir: covers are disposable and cache
// cleaners eat ~/Library/Caches, but this file is the only copy of how long
// you've played anything.
func dataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "ps3top"), nil
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "ps3top"), nil
	}
	return filepath.Join(home, ".local", "share", "ps3top"), nil
}

// loadHistory reads the log and folds it into per-game totals. A missing file
// is the normal first-run case, not an error.
func loadHistory(dir string) *history {
	h := &history{
		path:     filepath.Join(dir, "history.ndjson"),
		stats:    map[string]gameStat{},
		firstHDD: unknown,
		lastHDD:  unknown,
	}
	f, err := os.Open(h.path)
	if err != nil {
		return h
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var r sessionRec
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue // skip junk rather than lose the whole file
		}
		h.fold(r)
	}
	return h
}

func (h *history) fold(r sessionRec) {
	k := statKey(r.ID, r.Title)
	s := h.stats[k]
	s.Title, s.ID = r.Title, r.ID // newest record wins the display name
	s.Secs += r.Secs
	s.Sessions++
	if r.End.After(s.Last) {
		s.Last = r.End
	}
	if r.PeakCPU > s.PeakCPU {
		s.PeakCPU = r.PeakCPU
	}
	h.stats[k] = s

	if !r.End.IsZero() && (h.first.IsZero() || r.End.Before(h.first)) {
		h.first = r.End
	}
	if r.HDDFreeGB > 0 {
		if h.firstHDD == unknown {
			h.firstHDD = r.HDDFreeGB
		}
		h.lastHDD = r.HDDFreeGB
	}
}

func (h *history) stat(g Game) (gameStat, bool) {
	s, ok := h.stats[statKey(g.ID, g.Title)]
	return s, ok && s.Sessions > 0
}

// add appends a record and folds it in, so the running TUI reflects the
// session that just ended without re-reading the file.
func (h *history) add(r sessionRec) error {
	h.fold(r)
	if err := os.MkdirAll(filepath.Dir(h.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

// --- formatting ---

// fmtDur renders a duration the way the header already renders clocks:
// "2h14m" / "14m" / "45s".
func fmtDur(secs int) string {
	if secs < 0 {
		secs = 0
	}
	h, mi := secs/3600, (secs%3600)/60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, mi)
	case mi > 0:
		return fmt.Sprintf("%dm", mi)
	}
	return fmt.Sprintf("%ds", secs%60)
}

// fmtAgo is a coarse relative time — "3d", "2h", "just now". Precision past
// the day is noise for a "when did I last play this" column.
func fmtAgo(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// --- ps3top --stats ---

func runStats(dir string) {
	h := loadHistory(dir)
	if len(h.stats) == 0 {
		fmt.Println("ps3top — no play history yet")
		fmt.Println("(recorded at", filepath.Join(dir, "history.ndjson")+")")
		return
	}

	rows := make([]gameStat, 0, len(h.stats))
	total, sessions := 0, 0
	for _, s := range h.stats {
		rows = append(rows, s)
		total += s.Secs
		sessions += s.Sessions
	}
	// most-played first, then by name so equal totals don't shuffle between runs
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Secs != rows[j].Secs {
			return rows[i].Secs > rows[j].Secs
		}
		return sortKey(rows[i].Title) < sortKey(rows[j].Title)
	})

	// display width, not byte or rune count — ™ and CJK titles would otherwise
	// pad short and shear the column
	titleW := 5
	for _, r := range rows {
		if n := lipgloss.Width(r.Title); n > titleW {
			titleW = n
		}
	}
	titleW = min(titleW, 40)

	fmt.Printf("ps3top — play history · %d games · %d sessions · %s total\n\n",
		len(rows), sessions, fmtDur(total))
	for _, r := range rows {
		line := fmt.Sprintf("  %s  %9s  %3d %-9s", truncPad(r.Title, titleW),
			fmtDur(r.Secs), r.Sessions, plural(r.Sessions, "session"))
		if r.PeakCPU > 0 {
			line += fmt.Sprintf("  peak %d°", r.PeakCPU)
		}
		if a := fmtAgo(r.Last); a != "" {
			line += "  " + a
		}
		fmt.Println(line)
	}
	if !h.first.IsZero() {
		fmt.Printf("\nsince %s\n", h.first.Local().Format("2006-01-02"))
	}
	if h.firstHDD != unknown && h.lastHDD != unknown {
		if d := h.lastHDD - h.firstHDD; d <= -0.1 || d >= 0.1 {
			fmt.Printf("HDD %.1fG free (%+.1fG since first record)\n", h.lastHDD, d)
		}
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
