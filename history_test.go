package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustLoad(t *testing.T, dir string) *history {
	t.Helper()
	h, err := loadHistory(dir)
	if err != nil {
		t.Fatalf("loadHistory(%s): %v", dir, err)
	}
	return h
}

func testModel(t *testing.T, dir string, h *history) *model {
	t.Helper()
	if h == nil {
		h = mustLoad(t, dir)
	}
	return newModel(nil, "test", time.Second, 80, false, dir, h)
}

// lastRecord reads the newest line of the log — the peaks a session closed
// with are written there and shown once, in the exit flash, so the record is
// the only place to check them.
func lastRecord(t *testing.T, dir string) sessionRec {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "history.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var r sessionRec
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &r); err != nil {
		t.Fatal(err)
	}
	return r
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

	h := mustLoad(t, dir)
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
	if r := lastRecord(t, dir); r.PeakCPU != 75 || r.PeakRSX != 72 {
		t.Errorf("peaks = %d°/%d°, want 75°/72°", r.PeakCPU, r.PeakRSX)
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

	h := mustLoad(t, dir)
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

	if got := lastRecord(t, dir).PeakCPU; got != 68 {
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
	h := mustLoad(t, dir)
	now := time.Now()

	for i := 0; i < 3; i++ {
		err := h.add(sessionRec{ID: "MOCK1", Title: "Mock", End: now.Add(time.Duration(i) * time.Hour),
			Secs: 600, PeakCPU: 60 + i})
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

	got := mustLoad(t, dir)
	s, ok := got.stats["MOCK1"]
	if !ok {
		t.Fatal("records lost")
	}
	if s.Secs != 1800 || s.Sessions != 3 {
		t.Errorf("aggregate = %ds/%d sessions, want 1800/3", s.Secs, s.Sessions)
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
	h := mustLoad(t, t.TempDir())
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
	h := mustLoad(t, dir)
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
	h := mustLoad(t, dir)
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
	h := mustLoad(t, dir)
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

// --- session identity ---

// webMAN's PlayTime is CUMULATIVE for the whole game process, so a record
// written mid-game already contains the total so far. Quitting ps3top during a
// game wrote that total; restarting and playing on wrote the (larger) total
// again when the game finally ended. Both describe ONE session, and the log
// added them together: a 90-minute play reported as 150 minutes across two
// sessions, worse with every restart. README claimed this case was correct.
func TestRestartMidGameDoesNotDoubleCount(t *testing.T) {
	dir := t.TempDir()
	inGame := func(secs int) Status {
		return Status{InGame: true, GameID: "MOCK30982", GameTitle: "Sample Game™ 2", PlaySecs: secs}
	}
	// the clock has to move with the play counter — that's the whole basis on
	// which two records are recognised as one session
	base := time.Now()
	defer func(f func() time.Time) { timeNow = f }(timeNow)
	timeNow = func() time.Time { return base }

	// process A joins an hour in, then the user quits ps3top with the game running
	a := testModel(t, dir, nil)
	a.trackSession(inGame(3600))
	a.flushSession() // what q / ctrl-c does

	// thirty more minutes of play, under a new ps3top process
	timeNow = func() time.Time { return base.Add(30 * time.Minute) }
	b := testModel(t, dir, nil)
	b.trackSession(inGame(5400))
	b.trackSession(Status{InGame: false})

	h := mustLoad(t, dir)
	s := h.stats["MOCK30982"]
	if s.Secs != 5400 {
		t.Errorf("Secs = %d, want 5400 — one 90-minute session, not 3600+5400", s.Secs)
	}
	if s.Sessions != 1 {
		t.Errorf("Sessions = %d, want 1", s.Sessions)
	}
}

// Same repair, applied to a log written before the Started field existed: the
// start time is derivable from End − Secs, so history recorded by an older
// ps3top is corrected on load rather than staying wrong forever.
func TestLegacyRecordsAreDeduplicatedOnLoad(t *testing.T) {
	dir := t.TempDir()
	end := time.Now()
	// exactly the two lines the old code would have written, no Started field
	lines := `{"id":"MOCK1","title":"Game","end":"` + end.Add(-30*time.Minute).Format(time.RFC3339Nano) + `","secs":3600}
{"id":"MOCK1","title":"Game","end":"` + end.Format(time.RFC3339Nano) + `","secs":5400}
`
	if err := os.WriteFile(filepath.Join(dir, "history.ndjson"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	s := mustLoad(t, dir).stats["MOCK1"]
	if s.Secs != 5400 || s.Sessions != 1 {
		t.Errorf("legacy log folded to %ds over %d sessions, want 5400s over 1", s.Secs, s.Sessions)
	}
}

// Two genuinely separate plays of the same game must still count twice — the
// repair above must not collapse a whole evening into one record.
func TestSeparateSessionsStillCountSeparately(t *testing.T) {
	dir := t.TempDir()
	h := mustLoad(t, dir)
	now := time.Now()
	h.fold(sessionRec{ID: "MOCK1", Title: "Game", Started: now.Add(-5 * time.Hour), End: now.Add(-4 * time.Hour), Secs: 3600})
	h.fold(sessionRec{ID: "MOCK1", Title: "Game", Started: now.Add(-2 * time.Hour), End: now, Secs: 7200})

	s := h.stats["MOCK1"]
	if s.Secs != 10800 || s.Sessions != 2 {
		t.Errorf("two evenings folded to %ds over %d sessions, want 10800s over 2", s.Secs, s.Sessions)
	}
}

// If a game stops and restarts between two polls the title never changes, but
// webMAN's counter drops back. That drop was written straight over the longer
// reading, so the first session simply vanished.
func TestCounterResetStartsANewSession(t *testing.T) {
	dir := t.TempDir()
	m := testModel(t, dir, nil)
	inGame := func(secs int) Status {
		return Status{InGame: true, GameID: "MOCK1", GameTitle: "Game", PlaySecs: secs}
	}
	m.trackSession(inGame(3600)) // an hour in
	m.trackSession(inGame(120))  // quit and relaunched between polls
	m.trackSession(inGame(300))
	m.trackSession(Status{InGame: false})

	s := mustLoad(t, dir).stats["MOCK1"]
	if s.Sessions != 2 {
		t.Errorf("Sessions = %d, want 2 — the counter reset ended the first play", s.Sessions)
	}
	if s.Secs != 3900 {
		t.Errorf("Secs = %d, want 3900 (3600 + 300)", s.Secs)
	}
}

// An unparseable PlayTime reads as 0. That's missing information, not a session
// that restarted: it must not zero the counter (which would discard the session
// if no good poll followed) nor look like a reset.
func TestUnparseablePlayTimeIsIgnored(t *testing.T) {
	dir := t.TempDir()
	m := testModel(t, dir, nil)
	m.trackSession(Status{InGame: true, GameID: "MOCK1", GameTitle: "Game", PlaySecs: 3600})
	m.trackSession(Status{InGame: true, GameID: "MOCK1", GameTitle: "Game", PlaySecs: 0}) // PlayTime didn't match
	if m.sesSecs != 3600 {
		t.Errorf("sesSecs = %d, want the last good reading (3600)", m.sesSecs)
	}
	m.trackSession(Status{InGame: false})

	s := mustLoad(t, dir).stats["MOCK1"]
	if s.Secs != 3600 || s.Sessions != 1 {
		t.Errorf("got %ds over %d sessions, want 3600s over 1", s.Secs, s.Sessions)
	}
}

// The totals on screen used to include a record that never reached the disk:
// fold ran before the write, and the open session was closed either way, so a
// failed append lost the session AND left the UI showing it until restart.
func TestFailedAppendKeepsMemoryAndSessionIntact(t *testing.T) {
	dir := t.TempDir()
	// a plain file where the history DIRECTORY should be, so MkdirAll fails
	if err := os.WriteFile(filepath.Join(dir, "blocked"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h := &history{
		path:  filepath.Join(dir, "blocked", "sub", "history.ndjson"),
		stats: map[string]gameStat{},
	}
	m := testModel(t, dir, h)
	m.trackSession(Status{InGame: true, GameID: "MOCK1", GameTitle: "Game", PlaySecs: 3600})

	if flashed := m.flushSession(); !flashed {
		t.Error("write failure wasn't reported")
	}
	if _, ok := m.hist.stats["MOCK1"]; ok {
		t.Error("in-memory totals count a session that isn't on disk")
	}
	if !m.sesOn {
		t.Error("session was thrown away instead of held for a retry")
	}

	// and a later poll can still record it once the path works again
	m.hist.path = filepath.Join(dir, "history.ndjson")
	m.trackSession(Status{InGame: false})
	if s := mustLoad(t, dir).stats["MOCK1"]; s.Secs != 3600 {
		t.Errorf("retry recorded %ds, want 3600", s.Secs)
	}
}

// --- load errors ---

// A history file that exists but can't be read used to be indistinguishable
// from having no history at all — the one answer that looks exactly like the
// data being gone.
func TestLoadHistoryReportsRealFailures(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadHistory(dir); err != nil {
		t.Errorf("missing file should be the silent first-run case, got %v", err)
	}

	// a line past the scanner's 1 MiB cap stops the read partway through
	long := append(bytes.Repeat([]byte("x"), 2<<20), '\n')
	rec := []byte(`{"id":"MOCK1","title":"Game","secs":60,"end":"` + time.Now().Format(time.RFC3339Nano) + `"}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "history.ndjson"), append(rec, long...), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := loadHistory(dir)
	if err == nil {
		t.Error("truncated read reported as a complete history")
	}
	if h == nil || h.stats == nil {
		t.Fatal("loadHistory must always return a usable history")
	}
	// what did parse is still there — the caller warns and carries on
	if h.stats["MOCK1"].Secs != 60 {
		t.Error("records read before the failure were discarded")
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
