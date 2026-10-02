package ps3top

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Every action key, taken all the way to the wire. The guard tests stop at the
// prompt, which leaves the closure that actually calls the console unrun — the
// one place a wrong endpoint or a swapped argument would hide.
func TestActionKeysReachTheConsole(t *testing.T) {
	for _, c := range []struct {
		key     string
		confirm bool
		want    string
	}{
		{key: "u", want: "/mount_ps3/unmount"},
		{key: "p", want: "/play.ps3/dev_hdd0/PS3ISO/SampleGame2.iso"},
		{key: "enter", want: "/mount_ps3/dev_hdd0/PS3ISO/SampleGame2.iso"},
		{key: "S", confirm: true, want: "/shutdown.ps3"},
		{key: "R", confirm: true, want: "/restart.ps3"},
	} {
		t.Run(c.key, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/cpursx") {
					got = r.URL.RequestURI()
				}
				w.Write([]byte("webMAN CPU: 58°C"))
			}))
			defer srv.Close()

			m := liveModel(t, 120)
			m.cli = NewClient(strings.TrimPrefix(srv.URL, "http://"))
			m.st.InGame = false // known idle, so no prompt for the media keys
			m.games[0] = Game{Title: "Sample Game™ 2", ID: "MOCK30982", Category: "hdd0/PS3ISO",
				Path: "/dev_hdd0/PS3ISO/SampleGame2.iso", MountURL: "/mount_ps3/dev_hdd0/PS3ISO/SampleGame2.iso"}
			m.applyFilter("")

			_, cmd := m.handleKey(key(c.key))
			if c.confirm {
				if m.confirm == nil {
					t.Fatalf("%q raised no prompt", c.key)
				}
				_, cmd = m.handleKey(key("y"))
			}
			m = exec(t, m, cmd)
			if got != c.want {
				t.Errorf("%q hit %q, want %q", c.key, got, c.want)
			}
			if !strings.Contains(m.flash, "✓") {
				t.Errorf("%q didn't report success: %q", c.key, m.flash)
			}
		})
	}
}

// Launching an already-mounted game is its own endpoint, reached only when the
// selected row is the mounted disc.
func TestLaunchReachesTheConsole(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/cpursx") {
			got = r.URL.RequestURI()
		}
		w.Write([]byte("webMAN CPU: 58°C"))
	}))
	defer srv.Close()

	m := liveModel(t, 120)
	m.cli = NewClient(strings.TrimPrefix(srv.URL, "http://"))
	m.st.InGame = false
	m.st.MountedISO = m.games[0].Path
	if !m.mounted(m.games[0]) {
		t.Fatal("fixture isn't mounted")
	}
	_, cmd := m.handleKey(key("enter"))
	exec(t, m, cmd)
	if got != "/play.ps3" {
		t.Errorf("launch hit %q, want /play.ps3", got)
	}
}

func TestPopupReachesTheConsole(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/cpursx") {
			got = r.URL.RequestURI()
		}
		w.Write([]byte("webMAN CPU: 58°C"))
	}))
	defer srv.Close()

	m := liveModel(t, 120)
	m.cli = NewClient(strings.TrimPrefix(srv.URL, "http://"))
	m.handleKey(key("m"))
	for _, r := range "hi" {
		m.handleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	_, cmd := m.handleKey(key("enter"))
	exec(t, m, cmd)
	if got != "/popup.ps3/hi" {
		t.Errorf("popup hit %q", got)
	}
}

// The fan command's closure is where the previous state is captured for the
// delta report, so it has to run for real.
func TestFanCommandRoundTrip(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RequestURI()
		w.Write([]byte(`webMAN CPU: 58°C<br>RSX: 61°C<a href="x">FAN SPEED:  31% (0x4f)</a>`))
	}))
	defer srv.Close()

	m := liveModel(t, 120)
	m.cli = NewClient(strings.TrimPrefix(srv.URL, "http://"))
	m.handleKey(key("t"))
	m.st.FanMode, m.st.FanPct = "manual", 26

	_, cmd := m.handleKey(key("up"))
	m = exec(t, m, cmd)
	if got != "/cpursx.ps3?up" {
		t.Errorf("fan hit %q", got)
	}
	if !strings.Contains(m.flash, "26%") || !strings.Contains(m.flash, "31%") {
		t.Errorf("fan report didn't name the move: %q", m.flash)
	}
	if m.st.FanPct != 31 {
		t.Errorf("reply not adopted as the new state (fan %d%%)", m.st.FanPct)
	}
}

// r refreshes everything on screen — the status poll AND the games list. It
// used to re-poll status only, leaving the list to a separate key nobody
// remembered; one refresh key that skips half the screen isn't a refresh key.
func TestRefreshKeyFetchesStatusAndGames(t *testing.T) {
	m := liveModel(t, 120)
	m.cli = liveServer(t)

	_, cmd := m.handleKey(key("r"))
	if cmd == nil {
		t.Fatal("r did nothing")
	}
	m = exec(t, m, cmd)
	if len(m.games) != 22 {
		t.Errorf("r reloaded %d games, want the library", len(m.games))
	}

	// keys arrive through Update in the real runtime, not handleKey directly
	mm, _ := m.Update(key("t"))
	if !mm.(*model).thermalOn {
		t.Error("a key routed through Update didn't reach handleKey")
	}
}

// With a status request already in flight, r must not stack a second poll on
// top of it — but the games re-fetch is an idempotent read outside the gate,
// so THAT still goes out, and the poll is owed rather than dropped.
func TestRefreshKeyDefersTheStatusPollWhileBusy(t *testing.T) {
	m := liveModel(t, 120)
	m.cli = liveServer(t)
	m.inFlight = true

	_, cmd := m.handleKey(key("r"))
	if cmd == nil {
		t.Fatal("r dropped the games re-fetch along with the deferred poll")
	}
	if !m.needStatus {
		t.Error("the deferred status poll wasn't remembered")
	}
	m = exec(t, m, cmd)
	if len(m.games) != 22 {
		t.Errorf("r while busy reloaded %d games, want the library", len(m.games))
	}
}

// g's rescan taken to the wire: webMAN must be asked to rebuild its game
// database (?xmb — the variant that regenerates the XML we read), because
// re-fetching mygames.xml alone can never surface an ISO FTP'd over after
// boot. The reload that follows arrives as its own scheduled message.
func TestRescanReachesTheConsoleAndReloads(t *testing.T) {
	games, err := os.ReadFile("testdata/mygames.xml")
	if err != nil {
		t.Fatal(err)
	}
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/refresh.ps3") {
			got = r.URL.RequestURI()
		}
		w.Write(games)
	}))
	defer srv.Close()

	m := liveModel(t, 120)
	m.cli = NewClient(strings.TrimPrefix(srv.URL, "http://"))

	_, cmd := m.handleKey(key("g"))
	if cmd == nil {
		t.Fatal("g did nothing")
	}
	if !m.actBusy {
		t.Error("rescan didn't claim the action gate — a mount could race the scan")
	}
	if !strings.Contains(m.flash, "rescan") {
		t.Errorf("no immediate feedback for a seconds-long round trip: %q", m.flash)
	}
	m = exec(t, m, cmd)
	if got != "/refresh.ps3?xmb" {
		t.Errorf("rescan hit %q, want /refresh.ps3?xmb", got)
	}
	if m.actBusy {
		t.Error("gate still held after the rescan replied")
	}
	if !strings.Contains(m.flash, "✓") {
		t.Errorf("rescan didn't report success: %q", m.flash)
	}

	// the settle tick delivers reloadMsg; from there the list must re-read
	mm, reload := m.Update(reloadMsg{})
	m = exec(t, mm.(*model), reload)
	if len(m.games) != 22 {
		t.Errorf("reload after rescan loaded %d games, want the library", len(m.games))
	}
}

// A failed rescan must free the gate (or every action key is dead until
// restart) and say what happened.
func TestRescanFailureFreesTheGate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	m := liveModel(t, 120)
	m.cli = NewClient(strings.TrimPrefix(srv.URL, "http://"))

	_, cmd := m.handleKey(key("g"))
	m = exec(t, m, cmd)
	if m.actBusy {
		t.Error("failed rescan left the action gate held")
	}
	if !strings.Contains(m.flash, "rescan") {
		t.Errorf("failure not surfaced: %q", m.flash)
	}
}

// Like every other state-changing command, a rescan queues behind nothing —
// it refuses while one is out, with an explanation rather than silence.
func TestRescanRespectsTheActionGate(t *testing.T) {
	m := liveModel(t, 120)
	m.cli = liveServer(t)
	m.actBusy = true

	before := m.flash
	m.handleKey(key("g"))
	if m.flash == before || m.flash == "" {
		t.Error("g while busy was silently swallowed")
	}
	if !m.actBusy {
		t.Error("busy refusal somehow released the gate")
	}
}

// --- model edge states ---

func TestNewModelWithoutHistory(t *testing.T) {
	// every read path dereferences hist; an empty log has to be the no-op
	m := newModel(nil, "h", time.Second, 80, false, "", nil)
	if m.hist == nil || m.hist.stats == nil {
		t.Fatal("nil history wasn't replaced with an empty one")
	}
	if _, ok := m.hist.stat(Game{ID: "MOCK1"}); ok {
		t.Error("empty history reported a stat")
	}
	m.recalcPlayCol()
}

func TestViewBeforeTheFirstResize(t *testing.T) {
	m := liveModel(t, 120)
	m.width = 0
	if got := m.frame(); got != "starting…" {
		t.Errorf("View before a size = %q", got)
	}
	// the art panel widens the frame and narrows the list
	m.width = 120
	m.artOn = true
	wide := m.listWidth()
	m.artOn = false
	if bare := m.listWidth(); bare <= wide {
		t.Error("the art panel didn't reserve any width")
	}
	if !strings.Contains(m.frame(), "Sample Game") {
		t.Error("View dropped the library")
	}
	m.artOn = true
	m.frame() // with the panel joined on
}

func TestAlarmRingsOnceOnCrossing(t *testing.T) {
	m := liveModel(t, 120)
	m.alarm, m.alarming = 70, false

	hot := m.st
	hot.CPUTemp = 82
	_, cmd := m.Update(statusMsg{st: hot})
	if !strings.Contains(raw(cmd), "\a") {
		t.Error("crossing the alarm threshold didn't ring the bell")
	}
	if !m.alarming {
		t.Error("alarm state not latched")
	}
	// still hot on the next poll: no second bell
	_, cmd = m.Update(statusMsg{st: hot})
	if strings.Contains(raw(cmd), "\a") {
		t.Error("the bell rang again while already alarming")
	}
}

func TestScrollMarginFollowsTheCursorDown(t *testing.T) {
	m := liveModel(t, 120)
	m.listH = 4
	m.order = make([]int, 0, 40)
	m.games = make([]Game, 40)
	for i := range m.games {
		m.games[i] = Game{Title: "G", Category: "hdd0/PS3ISO"}
		m.order = append(m.order, i)
	}
	m.cursor, m.offset = 0, 0
	for i := 0; i < 30; i++ {
		m.moveCursor(1)
	}
	if m.offset == 0 {
		t.Error("the viewport never scrolled to follow the cursor")
	}
	if m.cursor < m.offset || m.cursor >= m.offset+m.listH {
		t.Errorf("cursor %d outside the visible window [%d,%d)", m.cursor, m.offset, m.offset+m.listH)
	}
}

// A history write that fails while the game is CHANGING must not fold the new
// game's counter into the session it couldn't close.
func TestSessionSwitchWithABrokenLog(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blocked"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h := &history{
		path:  filepath.Join(dir, "blocked", "sub", "history.ndjson"),
		stats: map[string]gameStat{},
	}
	m := testModel(t, dir, h)
	m.trackSession(Status{InGame: true, GameID: "MOCK1", GameTitle: "One", PlaySecs: 3600})
	// straight into a different game while the log is unwritable
	m.trackSession(Status{InGame: true, GameID: "MOCK2", GameTitle: "Two", PlaySecs: 60})
	if m.sesID != "MOCK1" || m.sesSecs != 3600 {
		t.Errorf("open session became %s/%ds — the failed flush let the new game in", m.sesID, m.sesSecs)
	}

	// same for a counter reset that can't be recorded
	m.trackSession(Status{InGame: true, GameID: "MOCK1", GameTitle: "One", PlaySecs: 10})
	if m.sesSecs != 3600 {
		t.Errorf("sesSecs = %d, want the unflushed 3600", m.sesSecs)
	}
}

func TestFlushSessionIgnoresEmptySessions(t *testing.T) {
	dir := t.TempDir()
	m := testModel(t, dir, nil)
	m.sesOn, m.sesTitle, m.sesSecs = true, "", 600 // no title
	if m.flushSession() {
		t.Error("a titleless session was recorded")
	}
	if m.sesOn {
		t.Error("session left open")
	}
	m.sesOn, m.sesTitle, m.sesSecs = true, "Game", 0 // no length
	if m.flushSession() {
		t.Error("a zero-length session was recorded")
	}
	if len(mustLoad(t, dir).stats) != 0 {
		t.Error("something reached the log")
	}
}

func TestRefreshNowDefersWhileBusy(t *testing.T) {
	m := liveModel(t, 120)
	m.fanBusy = true
	if cmd := m.refreshNow(); cmd != nil {
		t.Error("refreshNow polled on top of a fan command")
	}
	if !m.needStatus {
		t.Error("the deferred poll wasn't recorded")
	}
}

func TestClearFlashLaterFires(t *testing.T) {
	m := liveModel(t, 120)
	m.flash = "something"
	cmd := m.clearFlashLater()
	if msg := cmd(); msg == nil {
		t.Fatal("the flash timer produced no message")
	} else if _, ok := msg.(clearFlashMsg); !ok {
		t.Errorf("flash timer produced %T", msg)
	}
}

// --- rendering edges ---

func TestRenderingInATinyTerminal(t *testing.T) {
	// every bookended line clamps its fill rather than repeating a negative
	// count, which panics
	for _, w := range []int{1, 2, 5, 10, 20} {
		m := liveModel(t, w)
		m.listH = 3
		m.bookend("a fairly long piece of content that cannot possibly fit")
		m.tabLine()
		m.metricsRow(lipgloss.NewStyle())
		m.footer()
		m.frame()
		m.thermalOn = true
		m.frame()
	}
}

func TestTabLineStates(t *testing.T) {
	m := liveModel(t, 120)
	m.consoles, m.counts = []string{"PSX", "PS2", "PS3"}, []int{1, 2, 21}
	m.activeTab = 2
	line := m.tabLine()
	for _, want := range []string{"PSX", "PS2", "PS3", "│"} {
		if !strings.Contains(line, want) {
			t.Errorf("tab strip missing %q:\n%s", want, line)
		}
	}
	// a filter shows its query and the position within the matches
	m.filterQ = "storm"
	if got := m.tabLine(); !strings.Contains(got, "storm") {
		t.Errorf("filtered tab line doesn't show the query:\n%s", got)
	}
	m.filterQ = ""
	m.sortMode = sortRecent
	if got := m.tabLine(); !strings.Contains(got, "recent") {
		t.Errorf("recent sort not flagged:\n%s", got)
	}
}

func TestMetricLineShowsFanMode(t *testing.T) {
	m := liveModel(t, 120)
	m.st.FanMode = "manual"
	if got := m.metricsRow(lipgloss.NewStyle()); !strings.Contains(got, "manual") {
		t.Errorf("non-SYSCON fan mode not shown:\n%s", got)
	}
	m.st.FanMode = "SYSCON" // the console's own control needs no annotation
	m.metricsRow(lipgloss.NewStyle())
	m.st.FanMode = ""
	m.metricsRow(lipgloss.NewStyle())
	// the bare variant, used when the frame is too narrow for a box
	m.width = 40
	m.metricsRow(lipgloss.NewStyle())
}

func TestValueColourBands(t *testing.T) {
	m := liveModel(t, 120)
	// each band has to be distinguishable, or the colour carries no information
	seen := map[string]bool{}
	for _, pct := range []int{10, 55, 80} {
		seen[fanVal(pct)] = true
	}
	if len(seen) != 3 {
		t.Error("fan percentage bands aren't distinct")
	}
	if hddVal(30.0) == hddVal(300.0) {
		t.Error("a nearly-full disk renders like an empty one")
	}
	if memVal(300) == memVal(9000) {
		t.Error("memory pressure isn't distinguished")
	}
	if tempVal(72, m.alarm) == tempVal(50, m.alarm) {
		t.Error("a warm temperature renders like a cool one")
	}
}

func TestRowShowsMountedMark(t *testing.T) {
	m := liveModel(t, 120)
	m.st.MountedISO = m.games[0].Path
	if !strings.Contains(m.renderRow(0, false), "mounted") {
		t.Error("the mounted disc isn't marked in the list")
	}
}

func TestArtPanelVariants(t *testing.T) {
	dir := t.TempDir()
	m := testModel(t, dir, nil)
	m.width, m.height, m.listH, m.artOn = 120, 30, 20, true
	m.games = []Game{{Title: "Fixture Storm", Category: "hdd0/PSXISO"}} // PSX: no title ID
	m.secNums, m.consoles, m.counts, m.numW = []int{1}, []string{"PSX"}, []int{1}, 1
	m.applyFilter("")

	// with no ID the panel falls back to naming the console
	if got := m.artPanel(); !strings.Contains(got, "PSX") {
		t.Errorf("panel didn't fall back to the console:\n%s", got)
	}
	// play history and the mounted mark are appended when they apply
	m.hist.fold(sessionRec{Title: "Fixture Storm", End: time.Now().Add(-2 * time.Hour), Secs: 3600})
	m.st.MountedISO = m.games[0].Path
	m.online = true
	got := m.artPanel()
	for _, want := range []string{"1h00m", "session", "last "} {
		if !strings.Contains(got, want) {
			t.Errorf("panel missing %q:\n%s", want, got)
		}
	}
}

func TestFooterStates(t *testing.T) {
	m := liveModel(t, 120)

	// a dangerous prompt with no title still names the risk
	m.confirm = &confirmAction{label: "eject", danger: true}
	m.st.GameTitle = ""
	if got := m.footer(); !strings.Contains(got, "a game is running") {
		t.Errorf("titleless danger prompt = %q", got)
	}
	// an ordinary prompt is not styled as a danger
	m.confirm = &confirmAction{label: "leave SYSCON?"}
	if got := m.footer(); !strings.Contains(got, "SYSCON") || strings.Contains(got, "⚠") {
		t.Errorf("plain prompt = %q", got)
	}
	m.confirm = nil

	m.popupOn = true
	if got := m.footer(); !strings.Contains(got, "popup:") {
		t.Errorf("popup footer = %q", got)
	}
	m.popupOn = false

	m.flash = "something happened"
	if got := m.footer(); !strings.Contains(got, "something happened") {
		t.Errorf("flash footer = %q", got)
	}
	m.flash = ""

	m.filterQ = "storm"
	if got := m.footer(); !strings.Contains(got, "esc clear") {
		t.Errorf("filtered footer doesn't offer to clear = %q", got)
	}
}

// A HEN build is flagged as such; anything else keeps its second field, which
// is where the region lives.
func TestShortFWVariants(t *testing.T) {
	if got := shortFW("4.93 CEX PS3HEN 3.5.0"); got != "4.93 HEN" {
		t.Errorf("shortFW(HEN) = %q", got)
	}
	if got := shortFW("4.90 CEX"); got != "4.90 CEX" {
		t.Errorf("shortFW(non-HEN) = %q, want the region kept", got)
	}
	if got := shortFW("4.90"); got != "4.90" {
		t.Errorf("shortFW(bare) = %q", got)
	}
}

// --- pure helpers ---

func TestSparkAndTrendEdges(t *testing.T) {
	// a descending series exercises the low-water branch
	desc := make([]int, 60)
	for i := range desc {
		desc[i] = 80 - i/2
	}
	if got := spark(desc, 8, 10); got == "" {
		t.Error("a descending series drew nothing")
	}
	if d := trend(desc); d >= 0 {
		t.Errorf("trend of a falling series = %d, want negative", d)
	}
	// nothing valid to compare against
	gaps := make([]int, 60)
	for i := range gaps {
		gaps[i] = unknown
	}
	if d := trend(gaps); d != 0 {
		t.Errorf("trend of gaps = %d, want 0", d)
	}
	// a series shorter than the warm-up shows no trend at all
	if d := trend([]int{80, 40}); d != 0 {
		t.Errorf("trend before warm-up = %d, want 0", d)
	}
	if _, ok := firstValidFrom([]int{unknown, unknown}, 0); ok {
		t.Error("firstValidFrom found a value among gaps")
	}
	if v, ok := firstValidFrom([]int{unknown, 42}, 0); !ok || v != 42 {
		t.Errorf("firstValidFrom = %d, %v", v, ok)
	}
}

func TestThermalGeometryEdges(t *testing.T) {
	m := liveModel(t, 120)

	// a rune with no glyph is skipped rather than drawn as tofu
	if got := bigText("7Z4", lipgloss.NewStyle()); len(got) != digitH {
		t.Errorf("bigText height = %d", len(got))
	}
	if bigTextWidth("") != 0 {
		t.Error("empty text claimed width")
	}
	// too narrow for the gutter
	if plotRows(8, 6, nil, plotLine{[]int{60, 61}, cpuSt}) != nil {
		t.Error("drew a plot narrower than its gutter")
	}
	// a gridline outside the data range is dropped, not clamped onto an edge
	plotRows(6, 60, []int{5, 500}, plotLine{[]int{60, 61, 62}, cpuSt})
	// a descending series, for the low-water branch
	plotRange(nil, plotLine{[]int{80, 70, 60}, cpuSt})

	// the dynamic-mode target joins the gridlines
	m.st.MaxTemp = 86
	found := false
	for _, mk := range m.plotMarks() {
		if mk == 86 {
			found = true
		}
	}
	if !found {
		t.Error("the fan target isn't drawn as a gridline")
	}

	// the readouts collapse their gaps as the frame tightens. Below the widths
	// thermalView actually calls them at they simply return their natural size
	// — the screen clips — so the contract is checked where it applies.
	for _, w := range []int{60, 80, 120, 200} {
		for _, line := range m.bigReadouts(w) {
			if lipgloss.Width(line) > w {
				t.Errorf("readout at width %d is %d cols", w, lipgloss.Width(line))
			}
		}
		if got := lipgloss.Width(m.plotCaption(w)); got > w+1 {
			t.Errorf("caption at width %d is %d cols", w, got)
		}
		for _, line := range m.fanRow(w) {
			if lipgloss.Width(line) > w+1 {
				t.Errorf("fan row at width %d is %d cols", w, lipgloss.Width(line))
			}
		}
	}
	// narrower than anything real: must not panic on a negative pad
	m.bigReadouts(20)
	m.plotCaption(10)
	m.fanRow(10)
}

// --- io edges ---

func TestHistoryAddFailurePaths(t *testing.T) {
	dir := t.TempDir()
	// the log path is a directory: MkdirAll succeeds, OpenFile can't
	asDir := filepath.Join(dir, "history.ndjson")
	if err := os.Mkdir(asDir, 0o755); err != nil {
		t.Fatal(err)
	}
	h := &history{path: asDir, stats: map[string]gameStat{}}
	if err := h.add(sessionRec{ID: "MOCK1", Title: "Game", Secs: 60, End: time.Now()}); err == nil {
		t.Error("appending to a directory reported success")
	}
	if _, ok := h.stats["MOCK1"]; ok {
		t.Error("a record that never landed was folded into the totals")
	}
}

func TestLoadHistoryOpenFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "history.ndjson"), 0o755); err != nil {
		t.Fatal(err)
	}
	h, err := loadHistory(dir)
	if err == nil {
		t.Error("an unreadable history reported success")
	}
	if h == nil || h.stats == nil {
		t.Error("loadHistory must always return a usable history")
	}
}

func TestWriteCacheRenameFailure(t *testing.T) {
	dir := t.TempDir()
	// the destination is a non-empty directory, so the rename can't succeed
	dest := filepath.Join(dir, "x.png")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "occupant"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	writeCache(dest, tinyPNG(t))

	// the temp file must not be left behind
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".cover-") {
			t.Errorf("failed write left %q behind", e.Name())
		}
	}
}

func TestDecodeCoverRejectsImplausibleDimensions(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, coverMaxPx+1, 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeCover(buf.Bytes()); err == nil {
		t.Errorf("a %dpx-wide cover was accepted", coverMaxPx+1)
	}
}

// --- client edges ---

func TestNormalizeHostRejectsAnEmptyHostname(t *testing.T) {
	if got, err := normalizeHost(":80"); err == nil {
		t.Errorf("a port with no host was accepted as %q", got)
	}
}

func TestRedirectChainIsBounded(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// same origin every time, so only the hop count can stop it
		http.Redirect(w, r, srv.URL+"/again", http.StatusFound)
	}))
	defer srv.Close()

	cli := NewClient(strings.TrimPrefix(srv.URL, "http://"))
	if _, err := cli.get(context.Background(), "/start"); err == nil {
		t.Error("an endless same-origin redirect loop was followed to completion")
	}
}

func TestSanitizeDropsBidiControls(t *testing.T) {
	for name, in := range map[string]string{
		"LRM":     "a\u200eb",
		"RLM":     "a\u200fb",
		"isolate": "a\u2066b\u2069",
	} {
		if got := sanitize(in); got != "ab" {
			t.Errorf("%s: sanitize(%q) = %q, want %q", name, in, got, "ab")
		}
	}
}

// A franchise where one entry has no title ID can't use the release-order hint:
// the entry without an ID would sort against everything by title while its
// siblings sorted by ID, which is how the cycle got in.
func TestFranchiseWithAnIDlessMemberFallsBackToTitles(t *testing.T) {
	games := []Game{
		{Title: "Saga: Zulu", ID: "MOCK00001", Category: "hdd0/PS3ISO"},
		{Title: "Saga: Alpha", ID: "MOCK00002", Category: "hdd0/PS3ISO"},
		{Title: "Saga: Mike", ID: "", Category: "hdd0/PSXISO"}, // PSX entries carry none
	}
	keys := gameKeys(games)
	if keys[0].id != "" || keys[1].id != "" {
		t.Error("the ID hint stayed on for a franchise with an ID-less member")
	}
	if !keys[1].less(keys[0]) {
		t.Error("fallback isn't title order")
	}
}

// --- last-mile branches ---

// The list keys have to survive an empty library: a filter that matches
// nothing, or a console tab with no games on it.
func TestActionKeysWithNothingSelected(t *testing.T) {
	for _, k := range []string{"p", "enter"} {
		m := liveModel(t, 120)
		m.order = nil
		if _, cmd := m.handleKey(key(k)); cmd != nil {
			t.Errorf("%q acted with no game selected", k)
		}
		if m.confirm != nil {
			t.Errorf("%q raised a prompt with no game selected", k)
		}
	}
}

// A history long enough to show a trend, but whose only reading is older than
// the comparison window, has nothing to compare against.
func TestTrendWithNothingInTheWindow(t *testing.T) {
	vals := make([]int, 60)
	for i := range vals {
		vals[i] = unknown
	}
	vals[0] = 70 // the only sample, far older than trendGap
	if d := trend(vals); d != 0 {
		t.Errorf("trend = %d, want 0 — nothing recent enough to compare", d)
	}
	// and a delta below the noise floor is not a trend either
	flat := make([]int, 60)
	for i := range flat {
		flat[i] = 60
	}
	flat[len(flat)-1] = 61
	if d := trend(flat); d != 0 {
		t.Errorf("trend = %d, want 0 — under the noise floor", d)
	}
}

// The axis gutter grows with the label width, so a plot can be wide enough to
// pass the first check and still have no room left to draw in.
func TestPlotTooNarrowOnceTheGutterIsTaken(t *testing.T) {
	vals := []int{100, 101, 102}
	if got := plotRows(6, 12, nil, plotLine{vals, cpuSt}); got != nil {
		t.Errorf("drew a %d-row plot with no room after the gutter", len(got))
	}
}

func TestArtPanelShowsMountedDisc(t *testing.T) {
	m := liveModel(t, 120)
	m.artOn = true
	m.games[0].Path = "/dev_hdd0/PS3ISO/SampleGame2.iso"
	m.st.MountedISO = m.games[0].Path
	m.online = true
	// the mark rides on the title line — a line of its own would be a row
	// taken from the cover
	if got := m.artPanel(); !strings.Contains(got, "● ") || strings.Contains(got, "● mounted") {
		t.Errorf("art panel doesn't mark the mounted disc on the title line:\n%s", got)
	}
}

// A history file that exists but can't be opened is not "no history yet".
func TestLoadHistoryPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permissions don't apply")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "history.ndjson")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) })

	h, err := loadHistory(dir)
	if err == nil {
		t.Error("an unreadable history read as an empty one")
	}
	if h == nil || h.stats == nil {
		t.Error("loadHistory must always return a usable history")
	}
}

// x and X act on the running game, so they only work when one is known to be
// running — and then always behind the red confirm, since the game goes
// with whatever it hadn't saved. On the XMB, or with the state unknown, they
// say so instead of firing at nothing.
func TestQuitAndRestartGameKeys(t *testing.T) {
	for _, c := range []struct{ key, want string }{
		{"x", "/xmb.ps3$exit"},
		{"X", "/xmb.ps3$reloadgame"},
	} {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/cpursx") {
				got = r.URL.RequestURI()
			}
			w.Write([]byte("webMAN CPU: 58°C"))
		}))
		m := liveModel(t, 120)
		m.cli = NewClient(strings.TrimPrefix(srv.URL, "http://"))

		// on the XMB: a flash, no prompt, no request
		m.st.InGame = false
		if _, cmd := m.handleKey(key(c.key)); m.confirm != nil || !strings.Contains(m.flash, "no game running") {
			t.Errorf("%q on the XMB: confirm=%v flash=%q", c.key, m.confirm != nil, m.flash)
		} else {
			_ = cmd
		}
		// state unknown: same — the last reading can't be trusted either way
		m.st.InGame, m.online = true, false
		if _, _ = m.handleKey(key(c.key)); m.confirm != nil {
			t.Errorf("%q with the console offline raised a prompt", c.key)
		}
		// in-game: red confirm, then the wire
		m.online, m.st.InGame = true, true
		m.flash = ""
		m.handleKey(key(c.key))
		if m.confirm == nil || !m.confirm.danger {
			t.Fatalf("%q in-game didn't raise the red confirm", c.key)
		}
		_, cmd := m.handleKey(key("y"))
		exec(t, m, cmd)
		if got != c.want {
			t.Errorf("%q hit %q, want %q", c.key, got, c.want)
		}
		srv.Close()
	}
}
