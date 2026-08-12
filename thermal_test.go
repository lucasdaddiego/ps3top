package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestBigTextGeometry(t *testing.T) {
	rows := bigText("74°", lipgloss.NewStyle())
	if len(rows) != digitH {
		t.Fatalf("height = %d, want %d", len(rows), digitH)
	}
	want := bigTextWidth("74°")
	for i, r := range rows {
		if got := lipgloss.Width(r); got != want {
			t.Errorf("row %d is %d cols, want %d\n%q", i, got, want, r)
		}
	}
	// three glyphs at 3 pixels wide, doubled, plus two gaps
	if want != 3*digitW*pixelW+2*glyphGap*pixelW {
		t.Errorf("bigTextWidth = %d, unexpected", want)
	}

	// a rune with no glyph is skipped, not drawn as tofu or a blank column
	if bigTextWidth("7x4") != bigTextWidth("74") {
		t.Error("unmapped rune consumed width")
	}
	if got := bigText("", lipgloss.NewStyle()); lipgloss.Width(strings.Join(got, "")) != 0 {
		t.Error("empty string drew something")
	}
}

func TestEveryGlyphIsWellFormed(t *testing.T) {
	for r, g := range glyphs {
		for i, row := range g {
			if len([]rune(row)) != digitW {
				t.Errorf("glyph %q row %d is %d px wide, want %d", r, i, len([]rune(row)), digitW)
			}
			for _, px := range row {
				if px != ' ' && px != '█' {
					t.Errorf("glyph %q row %d has unexpected pixel %q", r, i, px)
				}
			}
		}
	}
	// the readout needs every digit plus the two suffixes it renders
	for _, r := range "0123456789°—%" {
		if _, ok := glyphs[r]; !ok {
			t.Errorf("no glyph for %q", r)
		}
	}
}

func TestPlotRowsGeometry(t *testing.T) {
	cpu, rsx := make([]int, 120), make([]int, 120)
	for i := range cpu {
		cpu[i], rsx[i] = 58+i/8, 60+i/20
	}
	for _, dim := range [][2]int{{4, 40}, {8, 80}, {12, 120}, {20, 200}} {
		rows, cols := dim[0], dim[1]
		out := plotRows(rows, cols, []int{70, 78}, plotLine{cpu, cpuSt}, plotLine{rsx, rsxSt})
		if len(out) != rows {
			t.Fatalf("%dx%d: got %d rows", rows, cols, len(out))
		}
		for i, line := range out {
			if got := lipgloss.Width(line); got != cols {
				t.Errorf("%dx%d: row %d is %d cols\n%s", rows, cols, i, got, line)
			}
		}
	}
}

func TestPlotRowsRefusesUselessSizes(t *testing.T) {
	vals := []int{60, 61, 62}
	if plotRows(plotMinH-1, 80, nil, plotLine{vals, cpuSt}) != nil {
		t.Error("drew a plot too short to read")
	}
	if plotRows(8, 11, nil, plotLine{vals, cpuSt}) != nil {
		t.Error("drew a plot narrower than its own gutter")
	}
	if plotRows(8, 80, nil, plotLine{nil, cpuSt}) != nil {
		t.Error("drew a plot with no data")
	}
	if plotRows(8, 80, nil, plotLine{[]int{unknown, unknown}, cpuSt}) != nil {
		t.Error("drew a plot from nothing but gaps")
	}
}

// Two thresholds can land on the same row in a short plot. Picking the label
// by ranging a map would make which one shows flicker between frames.
func TestPlotGridlineLabelsAreStable(t *testing.T) {
	vals := make([]int, 60)
	for i := range vals {
		vals[i] = 60 + i/6
	}
	first := strings.Join(plotRows(4, 60, []int{70, 71, 72, 78, 80}, plotLine{vals, cpuSt}), "\n")
	for i := 0; i < 200; i++ {
		if got := strings.Join(plotRows(4, 60, []int{70, 71, 72, 78, 80}, plotLine{vals, cpuSt}), "\n"); got != first {
			t.Fatalf("plot output changed between identical calls (run %d)", i)
		}
	}
}

func TestPlotRange(t *testing.T) {
	// a flat trace must not fill the plot with sensor noise
	lo, hi, ok := plotRange(nil, plotLine{[]int{60, 60, 61}, cpuSt})
	if !ok || hi-lo < tempSpan {
		t.Errorf("flat trace range = %d..%d, want at least %d span", lo, hi, tempSpan)
	}

	// a threshold just above the data gets pulled in, so you can see the
	// headroom rather than a full-height trace with no reference
	_, hi, _ = plotRange([]int{70, 78}, plotLine{[]int{60, 74}, cpuSt})
	if hi < 78 {
		t.Errorf("hi = %d, want the 78 threshold pulled in", hi)
	}

	// but a far-off threshold must not flatten the trace
	_, hi, _ = plotRange([]int{78}, plotLine{[]int{40, 45}, cpuSt})
	if hi >= 78 {
		t.Errorf("hi = %d, a distant threshold shouldn't stretch the axis", hi)
	}

	if _, _, ok := plotRange(nil, plotLine{nil, cpuSt}); ok {
		t.Error("empty data reported a range")
	}
}

func TestResample(t *testing.T) {
	// short history reads as a short history, left-padded — not stretched to
	// look like a full window
	got := resample([]int{60, 61, 62}, 10)
	if len(got) != 10 {
		t.Fatalf("len = %d, want 10", len(got))
	}
	for i := 0; i < 7; i++ {
		if got[i] != unknown {
			t.Errorf("col %d = %d, want a gap", i, got[i])
		}
	}
	if got[7] != 60 || got[9] != 62 {
		t.Errorf("tail = %v, want the samples right-aligned", got[7:])
	}

	// long history is bucket-averaged down, oldest first
	long := make([]int, 80)
	for i := range long {
		long[i] = i
	}
	down := resample(long, 8)
	if len(down) != 8 {
		t.Fatalf("len = %d, want 8", len(down))
	}
	for i := 1; i < len(down); i++ {
		if down[i] <= down[i-1] {
			t.Errorf("monotonic input resampled non-monotonically: %v", down)
		}
	}
	if resample(nil, 4)[0] != unknown {
		t.Error("empty input should resample to gaps")
	}
}

func TestBigMetricBands(t *testing.T) {
	m := liveModel(t, 120)
	cases := []struct {
		temp int
		want string
	}{
		{58, "normal"},
		{74, "warm"},
		{79, "HOT"},
		{82, "HOT"},
		{unknown, "no reading"},
	}
	for _, c := range cases {
		if got := m.bigMetric("CPU", c.temp, cpuSt); !strings.Contains(got, c.want) {
			t.Errorf("temp %d: want band %q in\n%s", c.temp, c.want, got)
		}
	}
	// the alarm threshold overrides the default band edge
	m.alarm = 65
	if got := m.bigMetric("CPU", 66, cpuSt); !strings.Contains(got, "HOT") {
		t.Errorf("66° with --alarm 65 should read HOT:\n%s", got)
	}
}

func TestThermalViewFitsFrame(t *testing.T) {
	for _, dim := range [][2]int{{80, 24}, {100, 24}, {110, 30}, {160, 50}, {84, 20}, {70, 16}} {
		m := liveModel(t, dim[0])
		m.height = dim[1]
		m.listH = max(3, m.height-headerH-footerH)
		m.thermalOn = true

		body := strings.Split(m.thermalView(), "\n")
		if len(body) > m.listH {
			t.Errorf("%dx%d: thermal body is %d rows, list area is %d", dim[0], dim[1], len(body), m.listH)
		}
		for i, line := range body {
			if got := lipgloss.Width(line); got > m.width {
				t.Errorf("%dx%d: body row %d is %d cols\n%s", dim[0], dim[1], i, got, line)
			}
		}
		// the fan row is the one thing that must survive every size — it's the
		// only place the controls are documented
		if !strings.Contains(m.thermalView(), "f mode") && !strings.Contains(m.thermalView(), "f take over") {
			t.Errorf("%dx%d: thermal view dropped the fan controls", dim[0], dim[1])
		}
	}
}

// The screen has to work before any history exists — it's reachable on the
// first frame.
func TestThermalViewWithNoHistory(t *testing.T) {
	m := liveModel(t, 120)
	m.hCPU, m.hRSX, m.hFan = series{}, series{}, series{}
	m.thermalOn = true
	out := m.thermalView()
	if !strings.Contains(out, "collecting samples") {
		t.Errorf("empty history should say so:\n%s", out)
	}
	if lipgloss.Width(m.View()) == 0 {
		t.Error("empty frame")
	}
}

func TestThermalTitleBarReplacesTabs(t *testing.T) {
	m := liveModel(t, 120)
	m.thermalOn = true
	tab := m.tabLine()
	if !strings.Contains(tab, "thermals") {
		t.Errorf("no title on the thermal screen:\n%s", tab)
	}
	if strings.Contains(tab, "PS3 · ") {
		t.Errorf("console tabs still shown while the list is hidden:\n%s", tab)
	}
	if got := lipgloss.Width(tab); got != m.width {
		t.Errorf("title bar is %d cols, want %d", got, m.width)
	}
	// the footer names the keys through the same hints as the fan row, so it
	// can't keep calling ↑↓ "fan speed" on a console whose ↑↓ move the target
	if foot := m.footer(); !strings.Contains(foot, m.fanHint()) || !strings.Contains(foot, m.fanModeHint()) {
		t.Errorf("thermal footer missing the fan keys:\n%s", foot)
	}
}

// In dynamic mode ↑↓ move the TARGET TEMPERATURE, not the fan speed. The fan
// row said "target" while the footer two lines below insisted on "fan speed",
// which is the more prominent of the two and the wrong one.
func TestThermalFooterNamesWhatTheKeysDo(t *testing.T) {
	m := liveModel(t, 120)
	m.thermalOn = true
	for mode, want := range map[string]string{"dynamic": "target", "manual": "speed"} {
		m.st.FanMode = mode
		if foot := m.footer(); !strings.Contains(foot, want) {
			t.Errorf("%s mode: footer should say %q\n%s", mode, want, foot)
		}
	}
	m.st.FanMode = "dynamic"
	if strings.Contains(m.footer(), "speed") {
		t.Errorf("dynamic mode: footer still calls the target a speed\n%s", m.footer())
	}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestThermalKeyBlock(t *testing.T) {
	m := liveModel(t, 120)

	m.handleKey(key("t"))
	if !m.thermalOn {
		t.Fatal("t didn't open the thermal screen")
	}

	// list keys are inert while the list is hidden
	before := m.cursor
	for _, k := range []string{"j", "k", "G", "/"} {
		m.handleKey(key(k))
	}
	if m.cursor != before {
		t.Errorf("cursor moved to %d behind the thermal screen", m.cursor)
	}
	if m.filterTyping {
		t.Error("/ opened the filter behind the thermal screen")
	}
	if !m.thermalOn {
		t.Error("a stray key closed the thermal screen")
	}

	// q closes the screen rather than quitting the app — quitting out of a
	// subscreen on the key that normally means "back" would be a nasty
	// surprise mid-session
	_, cmd := m.handleKey(key("q"))
	if m.thermalOn {
		t.Error("q didn't close the thermal screen")
	}
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Error("q quit ps3top from inside the thermal screen")
		}
	}

	// esc closes too
	m.handleKey(key("t"))
	m.handleKey(key("esc"))
	if m.thermalOn {
		t.Error("esc didn't close the thermal screen")
	}
}

func TestFanKeysOnlyBindInsideThermal(t *testing.T) {
	m := liveModel(t, 120)

	// on the main view these must not fire anything at the console
	for _, k := range []string{"+", "-", "f"} {
		if _, cmd := m.handleKey(key(k)); cmd != nil {
			t.Errorf("%q issued a command from the main view", k)
		}
	}
	if len(m.fanQueue) != 0 {
		t.Error("main-view keys queued a fan command")
	}

	m.handleKey(key("t"))
	for _, k := range []string{"+", "=", "up", "k"} {
		m.fanBusy, m.fanQueue = false, nil
		if _, cmd := m.handleKey(key(k)); cmd == nil {
			t.Errorf("%q stepped the fan nowhere", k)
		}
	}
	for _, k := range []string{"-", "_", "down", "j"} {
		m.fanBusy, m.fanQueue = true, nil // busy: the press should queue, not fire
		m.handleKey(key(k))
		if got := m.fanQueue; len(got) != 1 || got[0] != fanDown {
			t.Errorf("%q queued %v, want [%s]", k, got, fanDown)
		}
	}
	m.fanBusy, m.fanQueue, m.st.FanMode = true, nil, "manual"
	m.handleKey(key("f"))
	if got := m.fanQueue; len(got) != 1 || got[0] != fanMode {
		t.Errorf("f queued %v, want [%s]", got, fanMode)
	}
}

// The fan keys used to work from the very first frame, before any status had
// come back. ↑↓ move the fan percentage in manual mode and the TARGET
// TEMPERATURE in dynamic, and f is a one-way door out of SYSCON — so with the
// mode unknown, none of the three can say what it is about to do, and f can
// walk out of SYSCON without ever raising the warning that is the whole point
// of that prompt.
func TestFanKeysWaitForAKnownMode(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		have       bool
	}{
		{"before any status", "", false},
		{"status parsed but mode unreadable", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := liveModel(t, 120)
			m.handleKey(key("t"))
			m.haveStatus, m.st.FanMode = tc.have, tc.mode

			for _, k := range []string{"up", "down", "+", "-", "f"} {
				m.fanBusy, m.fanQueue, m.confirm = false, nil, nil
				m.handleKey(key(k))
				if len(m.fanQueue) != 0 || m.fanBusy {
					t.Errorf("%q acted with the fan mode unknown (queue=%v busy=%v)", k, m.fanQueue, m.fanBusy)
				}
				if m.confirm != nil {
					t.Errorf("%q raised a prompt it can't word honestly", k)
				}
				if m.flash == "" {
					t.Errorf("%q was silently ignored — no explanation", k)
				}
			}
		})
	}
}

// ?mode only cycles webMAN's own strategies, and each of them re-enables
// webMAN fan control — so f is a one-way door out of SYSCON, and getting back
// means unticking a checkbox on a 143-field setup form ps3top won't submit.
func TestLeavingSysconAsks(t *testing.T) {
	m := liveModel(t, 120)
	m.handleKey(key("t"))
	m.st.FanMode = "SYSCON"

	m.handleKey(key("f"))
	if m.confirm == nil {
		t.Fatal("f left SYSCON without asking")
	}
	if !strings.Contains(m.confirm.label, "SYSCON") {
		t.Errorf("prompt doesn't name the problem: %q", m.confirm.label)
	}
	if len(m.fanQueue) != 0 || m.fanBusy {
		t.Error("the toggle fired before the prompt was answered")
	}

	// declining leaves the console alone
	m.handleKey(key("n"))
	if len(m.fanQueue) != 0 || m.fanBusy {
		t.Error("declining still sent the toggle")
	}

	// accepting goes through
	m.st.FanMode = "SYSCON"
	m.handleKey(key("f"))
	if _, cmd := m.handleKey(key("y")); cmd == nil {
		t.Error("confirming didn't send the toggle")
	}
	if !m.fanBusy {
		t.Error("confirmed toggle never went in flight")
	}

	// and the label says what the key will do from here
	m.st.FanMode = "SYSCON"
	if got := m.fanModeHint(); got != "f take over" {
		t.Errorf("hint from SYSCON = %q, want %q", got, "f take over")
	}
	m.st.FanMode = "manual"
	if got := m.fanModeHint(); got != "f mode" {
		t.Errorf("hint from manual = %q, want %q", got, "f mode")
	}
}

// Arrows drive the fan on this screen and the list everywhere else; a leak
// either way would be a nasty surprise.
func TestArrowsDriveFanOnlyInThermal(t *testing.T) {
	m := liveModel(t, 120)
	m.cursor = 0
	m.handleKey(key("down"))
	if m.cursor != 1 {
		t.Errorf("down didn't move the list cursor on the main view (cursor %d)", m.cursor)
	}
	if len(m.fanQueue) != 0 {
		t.Error("main-view arrow queued a fan step")
	}

	m.handleKey(key("t"))
	before := m.cursor
	m.handleKey(key("up"))
	m.handleKey(key("up"))
	if m.cursor != before {
		t.Errorf("arrows moved the list cursor behind the thermal screen (%d → %d)", before, m.cursor)
	}
	// one dispatched, one still queued
	if !m.fanBusy || len(m.fanQueue) != 1 {
		t.Errorf("busy=%v queue=%v, want one in flight and one queued", m.fanBusy, m.fanQueue)
	}
}

// Presses are queued and sent one at a time: webMAN has ~4 session slots, and
// one GET per keypress turns a quick 24%→40% adjustment into a pile-up that
// reads as an unresponsive UI.
func TestFanPressesSerializeAndDrain(t *testing.T) {
	m := liveModel(t, 120)
	m.handleKey(key("t"))

	for i := 0; i < 5; i++ {
		m.handleKey(key("up"))
	}
	if !m.fanBusy {
		t.Fatal("nothing in flight after five presses")
	}
	if len(m.fanQueue) != 4 {
		t.Errorf("queue = %v, want 4 (one of the five dispatched)", m.fanQueue)
	}

	// each reply dispatches exactly one more
	for want := 3; want >= 0; want-- {
		mm, cmd := m.Update(fanMsg{cmd: fanUp, prevPct: 26, st: Status{FanPct: 27, FanMode: "manual"}})
		m = mm.(*model)
		if len(m.fanQueue) != want {
			t.Fatalf("queue = %v after a reply, want %d", m.fanQueue, want)
		}
		if want > 0 && !m.fanBusy {
			t.Fatalf("queue still at %d but nothing in flight", want)
		}
		if cmd == nil {
			t.Fatal("reply produced no follow-up")
		}
	}
	m.Update(fanMsg{cmd: fanUp, prevPct: 27, st: Status{FanPct: 28, FanMode: "manual"}})
	if m.fanBusy || len(m.fanQueue) != 0 {
		t.Errorf("drained to busy=%v queue=%v, want idle", m.fanBusy, m.fanQueue)
	}

	// a leaned-on key must not build a backlog the console works through for
	// a minute after you let go
	for i := 0; i < 200; i++ {
		m.handleKey(key("up"))
	}
	if len(m.fanQueue) > fanQueueMax {
		t.Errorf("queue = %d, want capped at %d", len(m.fanQueue), fanQueueMax)
	}

	// and a failure clears the backlog instead of hammering a sick console
	mm, _ := m.Update(fanMsg{cmd: fanUp, err: context_deadline{}})
	m = mm.(*model)
	if len(m.fanQueue) != 0 || m.fanBusy {
		t.Errorf("after an error: queue=%v busy=%v, want cleared", m.fanQueue, m.fanBusy)
	}
}

// The queue is ordered, not netted. ?up moves the fan percentage in manual mode
// and the target temperature in dynamic, so a step queued before a ?mode means
// something different once the mode change lands. The old shape stored steps as
// one net integer and the mode change as a separate boolean, and always sent the
// boolean FIRST — so pressing up, up, f in manual mode sent mode, up, up, and
// the two presses meant to spin the fan faster raised the temperature ceiling
// instead. Exactly backwards, on the screen that exists to keep the console cool.
func TestFanQueuePreservesPressOrder(t *testing.T) {
	// the queue is read directly: drainFan pops its head, so this IS the order
	// the console will be sent
	m := liveModel(t, 120)
	m.handleKey(key("t"))
	m.st.FanMode = "manual"
	m.fanBusy = true // hold everything in the queue so the whole order is visible

	for _, k := range []string{"up", "up", "f"} {
		m.handleKey(key(k))
	}
	want := []string{fanUp, fanUp, fanMode}
	if !equalStrs(m.fanQueue, want) {
		t.Errorf("queue = %v, want %v — a mode change must not overtake presses made before it", m.fanQueue, want)
	}

	// and the other way round, a mode change first stays first
	m.fanQueue = nil
	for _, k := range []string{"f", "up"} {
		m.handleKey(key(k))
	}
	if want := []string{fanMode, fanUp}; !equalStrs(m.fanQueue, want) {
		t.Errorf("queue = %v, want %v", m.fanQueue, want)
	}

	// an immediate reversal cancels rather than costing two round trips…
	m.fanQueue = nil
	m.handleKey(key("up"))
	m.handleKey(key("down"))
	if len(m.fanQueue) != 0 {
		t.Errorf("queue = %v, want up/down to cancel", m.fanQueue)
	}
	// …but only against the tail, so it can never reach across a mode change
	m.fanQueue = nil
	for _, k := range []string{"up", "f", "down"} {
		m.handleKey(key(k))
	}
	if want := []string{fanUp, fanMode, fanDown}; !equalStrs(m.fanQueue, want) {
		t.Errorf("queue = %v, want %v — cancelling across a ?mode reorders intent", m.fanQueue, want)
	}
}

func equalStrs(a, b []string) bool {
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

// A press has to register visually before the console answers — the round trip
// is ~half a second, and silence reads as a dropped keystroke.
func TestFanRowShowsPendingPresses(t *testing.T) {
	m := liveModel(t, 120)
	m.handleKey(key("t"))
	m.handleKey(key("up"))
	if row := m.fanRow(m.width)[0]; !strings.Contains(row, "⋯") {
		t.Errorf("in-flight press invisible:\n%s", row)
	}
	m.handleKey(key("up"))
	m.handleKey(key("up"))
	if row := m.fanRow(m.width)[0]; !strings.Contains(row, "⋯+2") {
		t.Errorf("queued presses not counted:\n%s", row)
	}
	m.handleKey(key("down"))
	if row := m.fanRow(m.width)[0]; !strings.Contains(row, "⋯+1") {
		t.Errorf("down should net against queued ups:\n%s", row)
	}
}

// The target is worth showing, but only as a real reading — a hand-built
// Status has MaxTemp 0, which must not render as "target 0°".
func TestFanRowTarget(t *testing.T) {
	m := liveModel(t, 120)
	m.st.FanMode, m.st.MaxTemp = "dynamic", 86
	row := strings.Join(m.fanRow(m.width), "")
	if !strings.Contains(row, "DYNAMIC") || !strings.Contains(row, "target 86°") {
		t.Errorf("dynamic row missing mode or target:\n%s", row)
	}

	for _, bad := range []int{0, unknown} {
		m.st.MaxTemp = bad
		if got := strings.Join(m.fanRow(m.width), ""); strings.Contains(got, "target 0°") ||
			strings.Contains(got, "target -1°") {
			t.Errorf("MaxTemp %d rendered a target:\n%s", bad, got)
		}
	}

}

// A target at or past the alarm means webMAN won't ramp until you're already
// in the band you asked to be warned about, so it's colored like one. Tested
// as a predicate because lipgloss strips color in a non-TTY test run, which
// would make a "did it render red" assertion pass whatever the code did.
func TestTargetOverAlarm(t *testing.T) {
	cases := []struct {
		max, alarm int
		want       bool
	}{
		{98, 80, true},  // observed on a real console after one ?up in dynamic mode
		{80, 80, true},  // at the threshold counts
		{60, 80, false}, // a sane target
		{0, 80, false},  // no reading
		{unknown, 80, false},
	}
	for _, c := range cases {
		if got := targetOverAlarm(c.max, c.alarm); got != c.want {
			t.Errorf("targetOverAlarm(%d, %d) = %v, want %v", c.max, c.alarm, got, c.want)
		}
	}
}

// ↑↓ moves the target in dynamic mode and the speed in manual — labelling both
// "adjust" would hide the one thing that makes those keys confusing.
func TestFanHintNamesWhatArrowsMove(t *testing.T) {
	m := liveModel(t, 120)
	for mode, want := range map[string]string{
		"dynamic": "↑↓ target",
		"manual":  "↑↓ speed",
		"SYSCON":  "↑↓ adjust",
		"":        "↑↓ adjust",
	} {
		m.st.FanMode = mode
		if got := m.fanHint(); got != want {
			t.Errorf("mode %q: hint = %q, want %q", mode, got, want)
		}
	}
}

func TestFanRowDegradesButKeepsControls(t *testing.T) {
	for _, w := range []int{40, 60, 70, 84, 120, 200} {
		m := liveModel(t, w)
		m.st.FanMode, m.st.MaxTemp = "dynamic", 86
		row := m.fanRow(w)[0]
		if got := lipgloss.Width(row); got > w {
			t.Errorf("width %d: fan row is %d cols\n%s", w, got, row)
		}
		if !strings.Contains(row, "f mode") && !strings.Contains(row, "f take over") {
			t.Errorf("width %d: fan row dropped its controls hint\n%s", w, row)
		}
	}
}

// --- fan control ---

// The fan endpoints answer with the whole status page, so the reply is the new
// state: one request instead of an action plus a re-poll.
func TestFanUsesTheResponseAsTheNewStatus(t *testing.T) {
	page, err := os.ReadFile("testdata/cpursx_ingame.html")
	if err != nil {
		t.Fatal(err)
	}
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.Write(page)
	}))
	defer srv.Close()

	cli := NewClient(strings.TrimPrefix(srv.URL, "http://"))
	st, err := cli.Fan(t.Context(), fanUp)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/cpursx.ps3?up" {
		t.Errorf("requested %q, want /cpursx.ps3?up", gotPath)
	}
	if st.FanPct != 26 || st.CPUTemp != 58 {
		t.Errorf("response not parsed as a status: fan=%d cpu=%d", st.FanPct, st.CPUTemp)
	}

	for _, c := range []struct{ cmd, want string }{
		{fanDown, "/cpursx.ps3?dn"},
		{fanMode, "/cpursx.ps3?mode"},
	} {
		if _, err := cli.Fan(t.Context(), c.cmd); err != nil {
			t.Fatal(err)
		}
		if gotPath != c.want {
			t.Errorf("cmd %q requested %q, want %q", c.cmd, gotPath, c.want)
		}
	}
}

// fanReport diffs every field a fan command can move rather than assuming
// which one it moved. That generality is the whole point: ?up steps the
// *target* in dynamic mode and the *percentage* in manual, so a report written
// around one of them announces "unchanged" while the other quietly moves —
// which is exactly how this was wrong the first time.
func TestFanReport(t *testing.T) {
	cases := []struct {
		name string
		msg  fanMsg
		want string
	}{
		{"stepped up in manual",
			fanMsg{cmd: fanUp, prevPct: 26, prevMax: unknown, prevMode: "manual",
				st: Status{FanPct: 31, MaxTemp: unknown, FanMode: "manual"}},
			"fan 26% → 31%"},
		{"stepped down in manual",
			fanMsg{cmd: fanDown, prevPct: 31, prevMax: unknown, prevMode: "manual",
				st: Status{FanPct: 26, MaxTemp: unknown, FanMode: "manual"}},
			"fan 31% → 26%"},
		// the case that used to report "fan unchanged at 31%" while the target moved
		{"stepped the target in dynamic",
			fanMsg{cmd: fanUp, prevPct: 31, prevMax: 86, prevMode: "dynamic",
				st: Status{FanPct: 31, MaxTemp: 98, FanMode: "dynamic"}},
			"target 86° → 98°"},
		{"mode cycled",
			fanMsg{cmd: fanMode, prevPct: 26, prevMode: "SYSCON",
				st: Status{FanPct: 26, FanMode: "dynamic", MaxTemp: 86}},
			"mode syscon → dynamic"},
		{"mode and speed both moved",
			fanMsg{cmd: fanMode, prevPct: 26, prevMode: "dynamic",
				st: Status{FanPct: 42, FanMode: "manual"}},
			"mode dynamic → manual · fan 26% → 42%"},
		{"mode unknown before",
			fanMsg{cmd: fanMode, prevPct: 26, prevMode: "",
				st: Status{FanPct: 26, FanMode: "manual"}},
			"mode manual"},
		{"genuinely refused to move",
			fanMsg{cmd: fanUp, prevPct: 100, prevMax: unknown, prevMode: "manual",
				st: Status{FanPct: 100, MaxTemp: unknown, FanMode: "manual"}},
			"nothing changed (fan 100%, manual)"},
		{"no reading",
			fanMsg{cmd: fanUp, prevPct: 26, st: Status{FanPct: unknown}},
			"fan: no reading"},
	}
	for _, c := range cases {
		if got := fanReport(c.msg); got != c.want {
			t.Errorf("%s: fanReport = %q, want %q", c.name, got, c.want)
		}
	}
}
