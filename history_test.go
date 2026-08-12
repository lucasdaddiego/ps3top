package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testModel(t *testing.T, dir string, h *history) *model {
	t.Helper()
	if h == nil {
		h = loadHistory(dir)
	}
	return newModel(nil, "test", time.Second, 80, false, dir, h)
}

// The session length is webMAN's PlayTime, not a clock ps3top starts. That is
// what makes a record right when ps3top joins a game already in progress —
// the case a naive timer gets wrong by however long the game had been running.
func TestSessionLengthComesFromPlayTime(t *testing.T) {
	dir := t.TempDir()
	m := testModel(t, dir, nil)

	inGame := func(secs, cpu, rsx int) Status {
		return Status{InGame: true, GameID: "MOCK30982", GameTitle: "Sample Game™ 2",
			PlaySecs: secs, CPUTemp: cpu, RSXTemp: rsx, HDDFreeGB: 123.4}
	}
	// first poll already reports an hour of play
	m.trackSession(inGame(3600, 70, 72))
	m.trackSession(inGame(3660, 75, 71))
	m.trackSession(Status{InGame: false, HDDFreeGB: 123.4})

	h := loadHistory(dir)
	s, ok := h.stats["MOCK30982"]
	if !ok {
		t.Fatal("no record written")
	}
	if s.Secs != 3660 {
		t.Errorf("Secs = %d, want 3660 (webMAN's count, not ps3top's uptime)", s.Secs)
	}
	if s.Sessions != 1 {
		t.Errorf("Sessions = %d, want 1", s.Sessions)
	}
	if s.PeakCPU != 75 {
		t.Errorf("PeakCPU = %d, want 75", s.PeakCPU)
	}
	if s.Title != "Sample Game™ 2" {
		t.Errorf("Title = %q", s.Title)
	}
}

// Going straight from one game into another has to close the first session,
// not merge the two.
func TestGameSwitchClosesPreviousSession(t *testing.T) {
	dir := t.TempDir()
	m := testModel(t, dir, nil)

	m.trackSession(Status{InGame: true, GameID: "MOCK00001", GameTitle: "First", PlaySecs: 600})
	m.trackSession(Status{InGame: true, GameID: "MOCK00002", GameTitle: "Second", PlaySecs: 120})
	m.trackSession(Status{InGame: false})

	h := loadHistory(dir)
	if len(h.stats) != 2 {
		t.Fatalf("games recorded = %d, want 2", len(h.stats))
	}
	if got := h.stats["MOCK00001"].Secs; got != 600 {
		t.Errorf("first session = %ds, want 600", got)
	}
	if got := h.stats["MOCK00002"].Secs; got != 120 {
		t.Errorf("second session = %ds, want 120", got)
	}
}

func TestNoRecordWithoutUsableLength(t *testing.T) {
	dir := t.TempDir()
	m := testModel(t, dir, nil)

	// PlayTime didn't parse (0) — a zero-length session is noise, not history
	m.trackSession(Status{InGame: true, GameID: "MOCK1", GameTitle: "Mock", PlaySecs: 0})
	m.trackSession(Status{InGame: false})

	if _, err := os.Stat(filepath.Join(dir, "history.ndjson")); !os.IsNotExist(err) {
		t.Error("wrote a record for a zero-length session")
	}
	// and quitting with no session open must not write either
	if m.flushSession() {
		t.Error("flushSession with nothing open reported a flash")
	}
}

func TestPeaksIgnoreUnknownTemps(t *testing.T) {
	dir := t.TempDir()
	m := testModel(t, dir, nil)

	m.trackSession(Status{InGame: true, GameID: "MOCK1", GameTitle: "Mock", PlaySecs: 60, CPUTemp: 68, RSXTemp: 70})
	m.trackSession(Status{InGame: true, GameID: "MOCK1", GameTitle: "Mock", PlaySecs: 120, CPUTemp: unknown, RSXTemp: unknown})
	m.trackSession(Status{InGame: false})

	if got := loadHistory(dir).stats["MOCK1"].PeakCPU; got != 68 {
		t.Errorf("PeakCPU = %d, want 68 — unknown must not overwrite a real peak", got)
	}
}

// PSX entries in mygames.xml carry no title ID, so they key off the title.
func TestStatKeyFallsBackToTitle(t *testing.T) {
	if statKey("MOCK1", "Whatever") != "MOCK1" {
		t.Error("title ID should win when present")
	}
	a := statKey("", "Fixture Storm™")
	b := statKey("", "FIXTURE STORM")
	if a != b {
		t.Errorf("trademark noise split the key: %q vs %q", a, b)
	}
	if a == "MOCK1" || a == "" {
		t.Errorf("unexpected fallback key %q", a)
	}
}

func TestHistoryRoundTripAndJunkTolerance(t *testing.T) {
	dir := t.TempDir()
	h := loadHistory(dir)
	now := time.Now()

	for i := 0; i < 3; i++ {
		err := h.add(sessionRec{ID: "MOCK1", Title: "Mock", End: now.Add(time.Duration(i) * time.Hour),
			Secs: 600, PeakCPU: 60 + i, HDDFreeGB: float64(200 - i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	// a half-written line (power loss mid-append) must not lose the file
	f, err := os.OpenFile(filepath.Join(dir, "history.ndjson"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("{\"id\":\"MOCK2\",\"tit\n")
	f.Close()

	got := loadHistory(dir)
	s, ok := got.stats["MOCK1"]
	if !ok {
		t.Fatal("records lost")
	}
	if s.Secs != 1800 || s.Sessions != 3 {
		t.Errorf("aggregate = %ds/%d sessions, want 1800/3", s.Secs, s.Sessions)
	}
	if s.PeakCPU != 62 {
		t.Errorf("PeakCPU = %d, want 62 (highest across sessions)", s.PeakCPU)
	}
	if got.firstHDD != 200 || got.lastHDD != 198 {
		t.Errorf("HDD drift = %v → %v, want 200 → 198", got.firstHDD, got.lastHDD)
	}
	if len(got.stats) != 1 {
		t.Errorf("junk line produced a phantom game: %v", got.stats)
	}

	// in-memory stats must already reflect the adds, without a reload
	if live, _ := h.stats["MOCK1"]; live.Sessions != 3 {
		t.Errorf("live sessions = %d, want 3", live.Sessions)
	}
}

func TestLoadHistoryMissingFile(t *testing.T) {
	h := loadHistory(t.TempDir())
	if len(h.stats) != 0 {
		t.Errorf("stats = %v, want empty", h.stats)
	}
	if _, ok := h.stat(Game{ID: "MOCK1"}); ok {
		t.Error("empty history reported a stat")
	}
}

// The whole point of the recent sort: the handful of games you actually play
// float above an alphabetical wall of everything else.
func TestRecencySort(t *testing.T) {
	dir := t.TempDir()
	h := loadHistory(dir)
	now := time.Now()
	h.fold(sessionRec{ID: "MOCK2", Title: "Beta", End: now.Add(-time.Hour), Secs: 100})
	h.fold(sessionRec{ID: "MOCK3", Title: "Gamma", End: now.Add(-24 * time.Hour), Secs: 100})

	m := testModel(t, dir, h)
	m.games = []Game{
		{Title: "Alpha", ID: "MOCK1", Category: "hdd0/PS3ISO"}, // never played
		{Title: "Beta", ID: "MOCK2", Category: "hdd0/PS3ISO"},  // 1h ago
		{Title: "Gamma", ID: "MOCK3", Category: "hdd0/PS3ISO"}, // 1d ago
	}
	m.consoles, m.counts, m.activeTab = []string{"PS3"}, []int{3}, 0

	m.applyFilter("")
	if want := []int{0, 1, 2}; !eqInts(m.order, want) {
		t.Errorf("alphabetical order = %v, want %v", m.order, want)
	}

	m.sortRecent = true
	m.applyFilter("")
	if want := []int{1, 2, 0}; !eqInts(m.order, want) {
		t.Errorf("recent order = %v, want %v (Beta, Gamma, then unplayed Alpha)", m.order, want)
	}

	// a filter must keep whichever sort is active
	m.applyFilter("a")
	for i := 1; i < len(m.order); i++ {
		si, oki := h.stat(m.games[m.order[i-1]])
		sj, okj := h.stat(m.games[m.order[i]])
		if !oki && okj {
			t.Errorf("filtered order dropped the recent sort: %v", m.order)
		}
		if oki && okj && sj.Last.After(si.Last) {
			t.Errorf("filtered order out of recency: %v", m.order)
		}
	}
}

// A sort toggle that dumps you back at row 1 is useless for "where did that
// one go".
func TestReorderKeepsCursorOnSameGame(t *testing.T) {
	dir := t.TempDir()
	h := loadHistory(dir)
	h.fold(sessionRec{ID: "MOCK3", Title: "Gamma", End: time.Now(), Secs: 100})

	m := testModel(t, dir, h)
	m.games = []Game{
		{Title: "Alpha", ID: "MOCK1", Category: "hdd0/PS3ISO"},
		{Title: "Beta", ID: "MOCK2", Category: "hdd0/PS3ISO"},
		{Title: "Gamma", ID: "MOCK3", Category: "hdd0/PS3ISO"},
	}
	m.consoles, m.counts, m.activeTab, m.listH = []string{"PS3"}, []int{3}, 0, 10

	m.applyFilter("")
	m.cursor = 2 // Gamma, last alphabetically
	m.sortRecent = true
	m.reorder()

	if g, ok := m.selectedGame(); !ok || g.ID != "MOCK3" {
		t.Errorf("cursor landed on %+v, want Gamma/MOCK3", g)
	}
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (Gamma is now the most recent)", m.cursor)
	}
}

func TestPlayColWidthIsLibraryWide(t *testing.T) {
	dir := t.TempDir()
	h := loadHistory(dir)
	h.fold(sessionRec{ID: "MOCK1", Title: "Short", End: time.Now(), Secs: 90})           // "1m"
	h.fold(sessionRec{ID: "MOCK2", Title: "Long", End: time.Now(), Secs: 3600*38 + 720}) // "38h12m"

	m := testModel(t, dir, h)
	m.games = []Game{
		{Title: "Short", ID: "MOCK1", Category: "hdd0/PS3ISO"},
		{Title: "Long", ID: "MOCK2", Category: "hdd0/PS3ISO"},
		{Title: "Unplayed", ID: "MOCK3", Category: "hdd0/PS3ISO"},
	}
	m.recalcPlayCol()
	if m.playColW != len("38h12m") {
		t.Errorf("playColW = %d, want %d — the column is sized once for the library, not per row",
			m.playColW, len("38h12m"))
	}

	// no history at all reserves nothing
	empty := testModel(t, t.TempDir(), nil)
	empty.games = m.games
	empty.recalcPlayCol()
	if empty.playColW != 0 {
		t.Errorf("playColW = %d with no history, want 0", empty.playColW)
	}
}

func TestFmtDur(t *testing.T) {
	cases := map[int]string{
		0: "0s", 45: "45s", 90: "1m", 3600: "1h00m", 5025: "1h23m", 137520: "38h12m",
		-5: "0s",
	}
	for in, want := range cases {
		if got := fmtDur(in); got != want {
			t.Errorf("fmtDur(%d) = %q, want %q", in, got, want)
		}
	}
	// the header's clock formatter now shares this, but must still pass
	// through anything that isn't a clock rather than claim "0s"
	if got := fmtClock("01:23:45"); got != "1h23m" {
		t.Errorf("fmtClock = %q, want 1h23m", got)
	}
	if got := fmtClock("garbage"); got != "garbage" {
		t.Errorf("fmtClock(garbage) = %q, want passthrough", got)
	}
}

func TestFmtAgo(t *testing.T) {
	now := time.Now()
	cases := []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, ""},
		{now, "just now"},
		{now.Add(-30 * time.Minute), "30m ago"},
		{now.Add(-5 * time.Hour), "5h ago"},
		{now.Add(-72 * time.Hour), "3d ago"},
	}
	for _, c := range cases {
		if got := fmtAgo(c.t); got != c.want {
			t.Errorf("fmtAgo(%v) = %q, want %q", c.t, got, c.want)
		}
	}
}

func eqInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
