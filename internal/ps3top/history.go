package ps3top

// Play history — the record the PS3 itself never keeps.
//
// Session length comes from webMAN's own PlayTime field, not from a clock we
// start ourselves: it is how long the current game has been running, so a
// session is right even when ps3top launches halfway through one or is
// restarted mid-game. That is what removes the need for an open-session
// sidecar file to survive a crash.
//
// The catch that costs, and what fixes it: PlayTime is CUMULATIVE for the whole
// game process, so quitting ps3top mid-game writes the total so far, and the
// next run writes the total again when the game finally ends. Both records
// describe one session. They're recognised as one by their derived START time
// (End − Secs), which is stable across restarts, and merged on load — keeping
// the longest reading and counting it once. Derived, not stored, so it also
// repairs records written before the field existed.
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
	"time"

	"github.com/lucasdaddiego/ps3top/webman"
)

// sessionRec is one line of history.ndjson. Append-only, one JSON object per
// line, so a forward-incompatible future field can't corrupt older records and
// an unparseable line is skipped rather than fatal.
type sessionRec struct {
	ID      string    `json:"id,omitempty"`
	Title   string    `json:"title"`
	Started time.Time `json:"started,omitempty"` // session identity — see recStart
	End     time.Time `json:"end"`
	Secs    int       `json:"secs"`
	PeakCPU int       `json:"peak_cpu,omitempty"`
	PeakRSX int       `json:"peak_rsx,omitempty"`
}

// gameStat is one game's totals as the list and art panel show them.
type gameStat struct {
	Title    string
	ID       string
	Secs     int
	Sessions int
	Last     time.Time

	// the most recent record folded for this game, so the next one can be
	// recognised as another reading of the same session rather than a new one
	lastStart time.Time
	lastSecs  int
}

type history struct {
	path  string
	stats map[string]gameStat // keyed by statKey
}

// statKey identifies a game across sessions. Title IDs are the stable handle,
// but PSX entries in mygames.xml carry none, so those fall back to the title.
func statKey(id, title string) string {
	if id != "" {
		return id
	}
	return "title:" + webman.SortKey(title)
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
// is the normal first-run case, not an error — but a file that exists and
// can't be read is, and it used to be indistinguishable: a permissions problem
// or a truncated line read as "no history yet", which is the one answer that
// looks the same as the data being gone. Always returns a usable history, so
// callers can warn and carry on.
func loadHistory(dir string) (*history, error) {
	h := &history{
		path:  filepath.Join(dir, "history.ndjson"),
		stats: map[string]gameStat{},
	}
	f, err := os.Open(h.path)
	if err != nil {
		if os.IsNotExist(err) {
			return h, nil
		}
		return h, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	lines := 0
	for sc.Scan() {
		lines++
		var r sessionRec
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue // skip junk rather than lose the whole file — deliberate
		}
		h.fold(r)
	}
	// a line past the 1 MiB cap, or a read error, stops the scan early and
	// silently truncates the history unless it's reported
	if err := sc.Err(); err != nil {
		return h, fmt.Errorf("%s: %w (stopped after %d lines)", h.path, err, lines)
	}
	return h, nil
}

// timeNow is a seam for tests. Session identity depends on the wall clock and
// webMAN's play counter advancing together, which a test finishing in
// microseconds cannot otherwise reproduce.
var timeNow = time.Now

// mergeWindow is how close two records' start times must be to be readings of
// one session. Wide enough to absorb poll lag at both ends (the flush happens
// up to an interval after the last sample), narrow enough that quitting a game
// and starting it again still reads as two sessions — that only collides for a
// session shorter than the window, where the miscount is a rounding error.
const mergeWindow = 5 * time.Minute

// recStart is when the session began. Records written before the field existed
// don't carry it, but it's derivable from what they do carry — which is what
// makes the double-count repair retroactive over an existing log.
func recStart(r sessionRec) time.Time {
	if !r.Started.IsZero() {
		return r.Started
	}
	if r.End.IsZero() {
		return time.Time{}
	}
	return r.End.Add(-time.Duration(r.Secs) * time.Second)
}

func (h *history) fold(r sessionRec) {
	k := statKey(r.ID, r.Title)
	s := h.stats[k]
	s.Title, s.ID = r.Title, r.ID // newest record wins the display name

	// Secs is webMAN's cumulative PlayTime, so two records of one session each
	// carry the whole total rather than a slice of it — summing them counts the
	// session twice (and once more per restart). Same start ⇒ same session:
	// take the longest reading, count it once.
	start := recStart(r)
	if !start.IsZero() && !s.lastStart.IsZero() && absDur(start.Sub(s.lastStart)) <= mergeWindow {
		if r.Secs > s.lastSecs {
			s.Secs += r.Secs - s.lastSecs
			s.lastSecs = r.Secs
		}
	} else {
		s.Secs += r.Secs
		s.Sessions++
		s.lastStart, s.lastSecs = start, r.Secs
	}
	if r.End.After(s.Last) {
		s.Last = r.End
	}
	h.stats[k] = s
}

func (h *history) stat(g Game) (gameStat, bool) {
	s, ok := h.stats[statKey(g.ID, g.Title)]
	return s, ok && s.Sessions > 0
}

// add appends a record and folds it in, so the running TUI reflects the
// session that just ended without re-reading the file.
//
// Order matters: the fold happens only after the append has landed. Folding
// first meant a failed write (full disk, unwritable data dir) left the totals
// on screen counting a session that isn't on disk — and since the caller
// closed the open session either way, it was gone for good and the numbers
// silently corrected themselves on the next restart.
func (h *history) add(r sessionRec) error {
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	h.fold(r)
	return nil
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
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

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
