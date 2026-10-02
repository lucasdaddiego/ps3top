package ps3top

import (
	"errors"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

var errTest = errors.New("dial tcp: connection refused")

func liveModel(t *testing.T, width int) *model {
	t.Helper()
	dir := t.TempDir()
	m := testModel(t, dir, nil)
	m.width, m.height = width, 30
	m.listH = m.height - headerH - footerH
	m.online, m.haveStatus = true, true
	m.st = Status{
		InGame: true, GameTitle: "Sample Game™ 2", GameID: "MOCK30982", GameVer: "01.15",
		CPUTemp: 58, RSXTemp: 61, FanMode: "SYSCON", FanPct: 26, MemFreeKB: 1152,
		MaxTemp:   unknown, // SYSCON: webMAN reports no ceiling
		HDDFreeGB: 123.4, PlayTime: "01:23:45", PlaySecs: 5025, Uptime: "01:24:02",
		Firmware: "4.93 CEX PS3HEN 3.5.0", WMVersion: "1.47.48q",
		LifeDays: 100, Boots: 1234, HardOffs: 34,
	}
	for i := 0; i < 60; i++ { // enough history for sparklines and a trend
		m.pushSamples(Status{CPUTemp: 55 + i/6, RSXTemp: 60, FanPct: 26}, 1)
	}
	m.games = []Game{
		{Title: "Sample Game™ 2", ID: "MOCK30982", Category: "hdd0/PS3ISO", Path: "/dev_hdd0/PS3ISO/SampleGame2.iso"},
		{Title: "Alpha", ID: "MOCK00001", Category: "hdd0/PS3ISO"},
	}
	m.secNums = []int{1, 2}
	m.consoles, m.counts, m.numW, m.titleColW = []string{"PS3"}, []int{2}, 1, 20
	m.applyFilter("")
	return m
}

// Every header line has to land on exactly the terminal width: bubbletea's
// line-diff renderer desyncs on a wrapped line and garbles the whole frame,
// which is why View hard-clips. The box math has to be right anyway.
//
// (Below ~100 cols the metrics and the lifetime block genuinely don't both
// fit and boxMid lets the line run long for View to clip — pre-existing, and
// the reason this starts at 100.)
func TestHeaderLinesExactlyFillWidth(t *testing.T) {
	for _, w := range []int{100, 120, 160, 200} {
		m := liveModel(t, w)
		for i, line := range headerLines(m) {
			if got := lipgloss.Width(line); got != w {
				t.Errorf("width %d: header line %d is %d cols\n%s", w, i, got, line)
			}
		}
	}
}

// Whatever the width, turning sparklines on must never make the header wider
// than it would have been without them.
func TestSparklinesNeverWidenTheHeader(t *testing.T) {
	for _, w := range []int{60, 72, 80, 90, 100, 120, 200} {
		m := liveModel(t, w)
		with := headerLines(m)

		bare := liveModel(t, w)
		bare.hCPU, bare.hRSX, bare.hFan = series{}, series{}, series{}
		without := headerLines(bare)

		for i := range with {
			a, b := lipgloss.Width(with[i]), lipgloss.Width(without[i])
			if a > b {
				t.Errorf("width %d: sparklines widened header line %d from %d to %d\n%s", w, i, b, a, with[i])
			}
		}
	}
}

// Before the first poll answers, the header is waiting — not "unreachable".
// online starts false, so the waiting branch used to test for a state that
// can't exist (a status-less model that's online) and every launch opened on
// a red "console unreachable — retrying" until the first reply landed.
func TestHeaderWaitsBeforeTheFirstPoll(t *testing.T) {
	m := testModel(t, t.TempDir(), nil)
	m.width, m.height, m.listH = 120, 30, 20
	if h := m.header(); !strings.Contains(h, "waiting for first status") || strings.Contains(h, "unreachable") {
		t.Errorf("fresh header:\n%s", h)
	}
	// a failed first poll is offline, and says so
	m.Update(statusMsg{err: errTest})
	if h := m.header(); !strings.Contains(h, "unreachable") {
		t.Errorf("header after a failed poll:\n%s", h)
	}
}

// headerLines drops the blank breathing row between the box and the tab strip.
func headerLines(m *model) []string {
	var out []string
	for _, l := range strings.Split(m.header(), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// When the header can't hold everything, the static syscon trivia goes before
// the live sparklines — and the metric readings themselves go last of all.
func TestHeaderDegradesInPriorityOrder(t *testing.T) {
	hasSpark := func(s string) bool { return strings.ContainsAny(s, string(blocks)) }

	full := liveModel(t, 160).header()
	if !hasSpark(full) || !strings.Contains(full, "1234 boots") {
		t.Errorf("160 cols should carry sparklines AND the full lifetime block:\n%s", full)
	}

	// tight enough that the boot counters have to go, but the shapes stay
	mid := liveModel(t, 110).header()
	if !hasSpark(mid) {
		t.Errorf("110 cols dropped the sparklines before the boot counters:\n%s", mid)
	}
	if strings.Contains(mid, "1234 boots") {
		t.Errorf("110 cols kept the full lifetime block:\n%s", mid)
	}
	if !strings.Contains(mid, "∞ 100d") {
		t.Errorf("110 cols dropped the ∞ day counter too eagerly:\n%s", mid)
	}

	// narrow enough that even the shapes go — but never the readings
	narrow := liveModel(t, 72).header()
	if hasSpark(narrow) {
		t.Errorf("sparkline survived at 72 cols:\n%s", narrow)
	}
	for _, want := range []string{"58°", "61°", "26%", "123G"} {
		if !strings.Contains(narrow, want) {
			t.Errorf("narrow header dropped %q:\n%s", want, narrow)
		}
	}
}

// A parse break has to look like a parse break, not like a cold console.
func TestMissingMetricsRenderAsGaps(t *testing.T) {
	m := liveModel(t, 120)
	m.st.CPUTemp, m.st.RSXTemp, m.st.FanPct = unknown, unknown, unknown
	m.st.MemFreeKB, m.st.HDDFreeGB = unknown, unknown

	head := m.header()
	if strings.Contains(head, "0°") {
		t.Errorf("unknown temp rendered as a number:\n%s", head)
	}
	if n := strings.Count(head, "—"); n < 5 {
		t.Errorf("gaps = %d, want 5 (CPU/RSX/FAN/HDD/MEM):\n%s", n, head)
	}
	for i, line := range headerLines(m) {
		if got := lipgloss.Width(line); got != m.width {
			t.Errorf("degraded header line %d is %d cols, want %d", i, got, m.width)
		}
	}
}

func TestRowsFitListWidth(t *testing.T) {
	dir := t.TempDir()
	h := mustLoad(t, dir)
	h.fold(sessionRec{ID: "MOCK30982", Title: "Sample Game™ 2", End: time.Now(), Secs: 137520})

	for _, w := range []int{80, 100, 160} {
		m := liveModel(t, w)
		m.hist = h
		m.recalcPlayCol()
		for i := range m.order {
			for _, sel := range []bool{false, true} {
				row := m.renderRow(m.order[i], sel)
				if got := lipgloss.Width(row); got > m.listWidth() {
					t.Errorf("width %d sel=%v: row %d is %d cols, list width is %d\n%s",
						w, sel, i, got, m.listWidth(), row)
				}
			}
		}
		// the played total must actually show up
		if !strings.Contains(m.renderRow(0, false), "38h12m") {
			t.Errorf("width %d: play total missing from row", w)
		}
	}
}

// The selected row is background-filled edge to edge; the play column must not
// punch a hole in it or run past the edge.
func TestSelectedRowStillFillsWidth(t *testing.T) {
	dir := t.TempDir()
	h := mustLoad(t, dir)
	h.fold(sessionRec{ID: "MOCK30982", Title: "Sample Game™ 2", End: time.Now(), Secs: 3600})

	m := liveModel(t, 120)
	m.hist = h
	m.recalcPlayCol()
	if got := lipgloss.Width(m.renderRow(0, true)); got != m.listWidth() {
		t.Errorf("selected row is %d cols, want %d", got, m.listWidth())
	}
}

// A full frame must render without panicking in the states ps3top actually
// sits in all day.
func TestViewRendersInEveryState(t *testing.T) {
	states := map[string]func(*model){
		"in game":       func(m *model) {},
		"offline":       func(m *model) { m.online, m.lastErr = false, context_deadline{} },
		"no status yet": func(m *model) { m.haveStatus = false },
		"xmb":           func(m *model) { m.st.InGame, m.st.MountedISO = false, "" },
		"mounted":       func(m *model) { m.st.InGame, m.st.MountedISO = false, "/dev_hdd0/PS3ISO/SampleGame2.iso" },
		"no games":      func(m *model) { m.games, m.order, m.consoles = nil, nil, nil },
		"filtering":     func(m *model) { m.filterTyping = true; m.applyFilter("zzz") },
		"confirming":    func(m *model) { m.confirm = &confirmAction{label: "eject", danger: true} },
		"alarming":      func(m *model) { m.alarming = true; m.st.CPUTemp = 82 },
		"recent sort":   func(m *model) { m.sortMode = sortRecent; m.applyFilter("") },
		"degraded":      func(m *model) { m.st.CPUTemp, m.st.RSXTemp = unknown, unknown },
		"thermal":       func(m *model) { m.thermalOn = true },
		"thermal cold":  func(m *model) { m.thermalOn = true; m.hCPU, m.hRSX, m.hFan = series{}, series{}, series{} },
		"thermal off":   func(m *model) { m.thermalOn = true; m.online, m.st = false, Status{} },
	}
	for name, setup := range states {
		m := liveModel(t, 120)
		setup(m)
		out := m.frame()
		if out == "" {
			t.Errorf("%s: empty frame", name)
		}
		for i, line := range strings.Split(out, "\n") {
			if got := lipgloss.Width(line); got > m.width {
				t.Errorf("%s: line %d is %d cols, over the %d-col terminal\n%s", name, i, got, m.width, line)
			}
		}
	}
}

// context_deadline stands in for a network error in the offline state.
type context_deadline struct{}

func (context_deadline) Error() string { return "dial tcp: i/o timeout" }

// --- action safety ---

// Mount, eject and play can interrupt a running game and lose unsaved progress
// — /mount_ps3 bypasses webMAN's own in-game protection, so the confirm dialog
// is the only net there is. The guard used to be `online && InGame`, which
// means it fired precisely when ps3top KNEW a game was running and stood aside
// whenever it didn't: on the first frames before any status arrived, and after
// any failed poll, when the last reading is stale and a game may well have
// started since. Weakest exactly where certainty is absent.
func TestGuardedActionsFailClosed(t *testing.T) {
	cases := []struct {
		name        string
		setup       func(*model)
		wantConfirm bool
		wantUnsure  bool
	}{
		{
			name:        "before the first status",
			setup:       func(m *model) { m.haveStatus, m.online, m.st = false, false, Status{} },
			wantConfirm: true, wantUnsure: true,
		},
		{
			name:        "offline, last seen in game",
			setup:       func(m *model) { m.online = false }, // liveModel's st is in-game
			wantConfirm: true, wantUnsure: true,
		},
		{
			name:        "offline, last seen on XMB",
			setup:       func(m *model) { m.online, m.st.InGame = false, false },
			wantConfirm: true, wantUnsure: true,
		},
		{
			name:        "online, in game",
			setup:       func(m *model) {},
			wantConfirm: true, wantUnsure: false,
		},
		{
			name:        "online, on XMB",
			setup:       func(m *model) { m.st.InGame = false },
			wantConfirm: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, k := range []string{"u", "p", "enter"} {
				m := liveModel(t, 120)
				c.setup(m)
				_, cmd := m.handleKey(key(k))

				if c.wantConfirm {
					if m.confirm == nil {
						t.Fatalf("%q fired at the console with no confirmation", k)
					}
					if cmd != nil {
						t.Errorf("%q dispatched a command as well as prompting", k)
					}
					if !m.confirm.danger {
						t.Errorf("%q prompt isn't styled as dangerous", k)
					}
					if m.confirm.unsure != c.wantUnsure {
						t.Errorf("%q unsure = %v, want %v", k, m.confirm.unsure, c.wantUnsure)
					}
				} else if m.confirm != nil {
					t.Errorf("%q asked on a console known to be sitting on the XMB", k)
				}
			}
		})
	}
}

// The likeliest way to relaunch the game you're already playing: it's mounted,
// the cursor is on its row, you press enter. That path went straight to
// /play.ps3 with no prompt — while the guard sat on the mount branch right
// next to it — even though the state was fully known.
func TestLaunchingAMountedGameIsGuarded(t *testing.T) {
	m := liveModel(t, 120)
	m.st.MountedISO = m.games[0].Path
	if !m.mounted(m.games[0]) {
		t.Fatal("fixture doesn't have the selected game mounted")
	}
	_, cmd := m.handleKey(key("enter"))
	if m.confirm == nil {
		t.Fatal("launching over a running game asked nothing")
	}
	if cmd != nil {
		t.Error("launch dispatched before the prompt was answered")
	}
	if !strings.Contains(m.confirm.label, "launch") {
		t.Errorf("prompt = %q, want it to name the launch", m.confirm.label)
	}
}

// The prompt raised because state is UNKNOWN must not quote the last title as
// if it were current — that's the reading it exists to distrust.
func TestUnsurePromptDoesNotClaimAGameIsRunning(t *testing.T) {
	m := liveModel(t, 120)
	m.online = false
	m.handleKey(key("u"))
	foot := m.footer()
	if !strings.Contains(foot, "unknown") {
		t.Errorf("footer doesn't flag the uncertainty:\n%s", foot)
	}
	if strings.Contains(foot, "Sample Game") {
		t.Errorf("footer states a stale title as fact:\n%s", foot)
	}
}

// --- parser degradation ---

// The "markup changed" flash used to latch on a single bool, so it was spent by
// whatever degraded first and stayed silent forever after. One transient bad
// page early on bought permanent silence — including for the real break hours
// later, when the fields the flash exists to explain went blank.
func TestMarkupWarningFiresOnGettingWorse(t *testing.T) {
	// missingN builds a status that fails to parse exactly n of the core fields
	missingN := func(n int) Status {
		st := Status{
			CPUTemp: 58, RSXTemp: 61, FanPct: 26, MemFreeKB: 1152,
			HDDFreeGB: 123.4, Firmware: "4.93 CEX PS3HEN 3.5.0",
		}
		fields := []func(){
			func() { st.CPUTemp = unknown }, func() { st.RSXTemp = unknown },
			func() { st.FanPct = unknown }, func() { st.MemFreeKB = unknown },
			func() { st.HDDFreeGB = unknown }, func() { st.Firmware = "" },
		}
		for i := 0; i < n; i++ {
			fields[i]()
		}
		if got := st.missing(); got != n {
			t.Fatalf("fixture builder wrong: missing() = %d, want %d", got, n)
		}
		return st
	}

	poll := func(m *model, st Status) string {
		m.flash = ""
		mm, _ := m.Update(statusMsg{st: st})
		return mm.(*model).flash
	}

	m := liveModel(t, 120)
	m.prevMissing = 0

	if got := poll(m, missingN(1)); got == "" {
		t.Error("first degraded page didn't warn")
	}
	if got := poll(m, missingN(1)); got != "" {
		t.Errorf("steady-state break warned again every poll: %q", got)
	}
	if got := poll(m, missingN(0)); got != "" {
		t.Errorf("recovery raised a warning: %q", got)
	}
	// the case the latch lost entirely: a real break after an earlier blip
	if got := poll(m, missingN(6)); got == "" {
		t.Error("a later, worse break went unreported")
	}
	// and worsening again from a degraded state still warns
	m.prevMissing = 1
	if got := poll(m, missingN(3)); got == "" {
		t.Error("going from 1 missing field to 3 went unreported")
	}
}

// An outage is not a markup change. Coming back online into the same degraded
// state the console left in must not re-raise the warning.
func TestOutageDoesNotResetTheMarkupWarning(t *testing.T) {
	m := liveModel(t, 120)
	degraded := Status{CPUTemp: unknown, RSXTemp: 61, FanPct: 26, MemFreeKB: 1152,
		HDDFreeGB: 123.4, Firmware: "4.93 CEX PS3HEN 3.5.0"}

	m.Update(statusMsg{st: degraded}) // warns
	m.Update(statusMsg{err: context_deadline{}})
	m.flash = ""
	mm, _ := m.Update(statusMsg{st: degraded})
	if got := mm.(*model).flash; got != "" {
		t.Errorf("reconnecting into the same state re-warned: %q", got)
	}
}

// --- request serialization ---

// README promised "one request in flight max" while status polls tracked
// inFlight and fan commands tracked fanBusy, neither consulting the other. A
// tick landing mid-fan-command let an older status page overwrite the newer fan
// state, and burned one of webMAN's ~4 session slots doing it.
func TestPollsDeferWhileTheConsoleIsBusy(t *testing.T) {
	m := liveModel(t, 120)
	m.fanBusy = true

	mm, _ := m.Update(tickMsg{})
	m = mm.(*model)
	if m.inFlight {
		t.Error("tick started a status poll on top of a fan command")
	}
	if !m.needStatus {
		t.Error("the skipped poll wasn't remembered")
	}

	// the fan reply releases it, but only once the queue is empty
	m.fanQueue = []string{fanUp}
	mm, _ = m.Update(fanMsg{st: Status{FanPct: 30, FanMode: "manual"}})
	m = mm.(*model)
	if !m.needStatus {
		t.Error("deferred poll dropped while the queue was still draining")
	}
	if !m.fanBusy {
		t.Fatal("queued command never dispatched")
	}

	mm, cmd := m.Update(fanMsg{st: Status{FanPct: 31, FanMode: "manual"}})
	m = mm.(*model)
	if m.needStatus {
		t.Error("deferred poll never taken after the queue drained")
	}
	if !m.inFlight || cmd == nil {
		t.Error("catch-up poll didn't go out")
	}
}

// Two actions in flight land in whatever order the console gets to them, and
// each costs a session slot.
func TestActionsDoNotStack(t *testing.T) {
	m := liveModel(t, 120)
	m.st.InGame = false // no prompt in the way

	if _, cmd := m.handleKey(key("u")); cmd == nil {
		t.Fatal("first eject never dispatched")
	}
	if !m.actBusy {
		t.Fatal("dispatched action didn't claim the gate")
	}
	before := m.flash
	m.handleKey(key("u"))
	if m.flash == before {
		t.Error("second action was silently swallowed with no explanation")
	}

	// the reply frees it, and triggers the refresh that confirms the new state
	mm, _ := m.Update(actionMsg{label: "eject"})
	m = mm.(*model)
	if m.actBusy {
		t.Error("gate still held after the action replied")
	}
	if !m.inFlight {
		t.Error("successful action didn't trigger its follow-up status read")
	}
	if _, cmd := m.handleKey(key("u")); cmd == nil {
		t.Error("actions never became possible again")
	}
}

// A background status poll carries no console state, so it must not eat a
// command the user has already confirmed — a 15s cadence against a 6s timeout
// means an unlucky eject would simply vanish.
func TestAPollInFlightDoesNotSwallowAnAction(t *testing.T) {
	m := liveModel(t, 120)
	m.st.InGame = false
	m.inFlight = true // a scheduled poll is mid-request

	_, cmd := m.handleKey(key("u"))
	if cmd == nil {
		t.Fatal("eject dropped because a status poll happened to be running")
	}
	if !m.actBusy {
		t.Error("action didn't claim the action gate")
	}
	// but the poll that comes due meanwhile is still deferred, not raced
	mm, _ := m.Update(tickMsg{})
	if !mm.(*model).needStatus {
		t.Error("tick during an action neither polled nor deferred")
	}
}

// The selection highlight ends a space past the row's content. A wide window
// hands the list every column the cover can't use, and a bar that ran to the
// list's edge pointed at nothing — except in the no-art layout, where the
// play column sits at that edge and the row has to reach it.
func TestSelectionHighlightStopsAtTheContent(t *testing.T) {
	m := liveModel(t, 200)
	m.artOn = true
	m.playColW = 0
	sel := m.renderRow(m.order[0], true)
	content := 2 + m.numW + 2 + clamp(m.titleColW, 10, m.listWidth()) + 1 + idColW
	if got := lipgloss.Width(sel); got != content+1 {
		t.Errorf("highlight is %d cols wide, want the %d of content plus one", got, content+1)
	}
	// the mounted mark extends it
	m.games[0].Path = "/dev_hdd0/PS3ISO/SampleGame2.iso"
	m.st.MountedISO = m.games[0].Path
	if got := lipgloss.Width(m.renderRow(m.order[0], true)); got != content+lipgloss.Width(" ● mounted")+1 {
		t.Errorf("mounted highlight is %d cols wide, want %d", got, content+lipgloss.Width(" ● mounted")+1)
	}
	// with the play column at the edge (no art), the row spans the list
	m.artOn = false
	m.playColW = 5
	if got := lipgloss.Width(m.renderRow(m.order[0], true)); got != m.listWidth() {
		t.Errorf("row with a play column is %d cols, want the list's %d", got, m.listWidth())
	}
}

// The art panel is centered in the slack between the list's content and the
// right edge, not pushed against either: the cover's height caps its width,
// so a wide window always has columns left over somewhere.
func TestArtPanelIsCenteredInTheSlack(t *testing.T) {
	m := liveModel(t, 200)
	m.artOn = true
	cols, _ := m.artBox()
	list := m.renderList()
	body := m.withArtPanel(list)
	lines := strings.Split(body, "\n")
	// find the box's top border and where it starts
	var boxLine string
	for _, l := range lines {
		if strings.Contains(l, "╭") {
			boxLine = l
			break
		}
	}
	if boxLine == "" {
		t.Fatal("no art box in the body")
	}
	start := lipgloss.Width(boxLine[:strings.Index(boxLine, "╭")])
	listW := lipgloss.Width(list)
	leftGap := start - listW
	rightGap := m.width - start - (cols + 2)
	if leftGap < 2 || rightGap < 0 || abs(leftGap-rightGap) > 2 {
		t.Errorf("panel at col %d: %d cols left of it, %d right, want them balanced (list %d, box %d)", start, leftGap, rightGap, listW, cols+2)
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// The footer carries no keybar any more — one never fit every key at any
// width, so it always hid the ones worth discovering. It points at the help
// screen, which names every key, fits every width, and comes and goes on ?.
func TestFooterPointsAtTheHelpScreen(t *testing.T) {
	for _, w := range []int{72, 90, 120, 160} {
		m := liveModel(t, w)
		if w < 120 {
			// one column below ~120 cols: taller, so a taller terminal
			m.height, m.listH = 45, 45-headerH-footerH
		}
		if foot := m.footer(); !strings.Contains(foot, "? help") || lipgloss.Width(foot) > w {
			t.Errorf("width %d: footer %q", w, foot)
		}
		m.applyFilter("sam")
		if !strings.Contains(m.footer(), "esc clear") {
			t.Error("filter state dropped from the footer")
		}

		m.handleKey(key("?"))
		if !m.helpOn {
			t.Fatal("? didn't open the help screen")
		}
		frame := m.frame()
		for i, line := range strings.Split(frame, "\n") {
			if lipgloss.Width(line) > w {
				t.Errorf("width %d: help line %d is %d cols", w, i, lipgloss.Width(line))
			}
		}
		for _, s := range helpSections {
			for _, k := range s.keys {
				if !strings.Contains(frame, k.keys) {
					t.Errorf("width %d: help screen doesn't name %q", w, k.keys)
				}
			}
		}
		if !strings.Contains(m.tabLine(), "help") {
			t.Error("tab strip doesn't name the screen")
		}
		m.handleKey(key("esc"))
		if m.helpOn {
			t.Error("esc didn't close the help screen")
		}
		// from the thermal screen, ? opens it and esc returns there
		m.handleKey(key("t"))
		m.handleKey(key("?"))
		m.handleKey(key("esc"))
		if m.helpOn || !m.thermalOn {
			t.Error("help from the thermal screen didn't return to it")
		}
	}
}

// In the size sort the row carries a size column: the title must shrink by
// it, or a long title pushes the row past the list width and the frame clip
// cuts the mounted mark.
func TestSizeSortRowFitsTheListWidth(t *testing.T) {
	for _, w := range []int{60, 80} {
		m := liveModel(t, w)
		m.games[0].Title = "Sample Quest: The Beginning - Game of the Year Edition"
		m.titleColW = lipgloss.Width(m.games[0].Title)
		m.st.MountedISO = m.games[0].Path
		m.sizes = map[string]int64{m.games[0].Path: 9 << 30}
		m.sortMode = sortSize
		m.applyFilter("")
		for i := range m.order {
			if got := lipgloss.Width(m.renderRow(m.order[i], false)); got > m.listWidth() {
				t.Errorf("width %d: row %d is %d cols, the list is %d", w, i, got, m.listWidth())
			}
		}
		for _, line := range strings.Split(m.frame(), "\n") {
			if strings.Contains(line, "Sample Quest") && !strings.Contains(line, "mounted") {
				t.Errorf("width %d: the mounted mark was clipped off the row:\n%s", w, line)
			}
		}
	}
}
