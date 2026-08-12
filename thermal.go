package main

// The thermal screen (`t`): big CPU/RSX readouts, a multi-row plot of the
// history rings the header sparklines already fill, and fan control sitting
// next to the temperatures it changes.
//
// It replaces the list body rather than floating over it — the frame keeps its
// header and footer rails, and there is no z-order or background compositing
// to get wrong.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	digitH   = 5 // pixel rows per glyph
	digitW   = 3 // pixel columns per glyph
	pixelW   = 2 // terminal cells per pixel — terminal cells are ~2:1 tall
	glyphGap = 1 // pixel columns between glyphs

	plotMinH  = 4
	bigBlockH = digitH + 2 // glyph rows plus the name above and the band below
)

// A 3×5 pixel font. Doubled horizontally at render time, so a glyph occupies
// 6 cells and reads roughly square.
var glyphs = map[rune][digitH]string{
	'0': {"███", "█ █", "█ █", "█ █", "███"},
	'1': {" █ ", "██ ", " █ ", " █ ", "███"},
	'2': {"███", "  █", "███", "█  ", "███"},
	'3': {"███", "  █", "███", "  █", "███"},
	'4': {"█ █", "█ █", "███", "  █", "  █"},
	'5': {"███", "█  ", "███", "  █", "███"},
	'6': {"███", "█  ", "███", "█ █", "███"},
	'7': {"███", "  █", "  █", "  █", "  █"},
	'8': {"███", "█ █", "███", "█ █", "███"},
	'9': {"███", "█ █", "███", "  █", "███"},
	'°': {"███", "█ █", "███", "   ", "   "},
	'%': {"█ █", "  █", " █ ", "█  ", "█ █"},
	'—': {"   ", "   ", "███", "   ", "   "},
}

// bigText renders a short string in the block font, styled per line. Unknown
// runes are skipped rather than drawn as tofu.
func bigText(s string, st lipgloss.Style) []string {
	rows := make([]string, digitH)
	first := true
	for _, r := range s {
		g, ok := glyphs[r]
		if !ok {
			continue
		}
		for i := 0; i < digitH; i++ {
			if !first {
				rows[i] += strings.Repeat(" ", glyphGap*pixelW)
			}
			for _, px := range g[i] {
				if px == ' ' {
					rows[i] += strings.Repeat(" ", pixelW)
				} else {
					rows[i] += strings.Repeat("█", pixelW)
				}
			}
		}
		first = false
	}
	for i := range rows {
		rows[i] = st.Render(rows[i])
	}
	return rows
}

func bigTextWidth(s string) int {
	n := 0
	for _, r := range s {
		if _, ok := glyphs[r]; ok {
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return n*digitW*pixelW + (n-1)*glyphGap*pixelW
}

// --- plot ---

type plotLine struct {
	vals  []int
	style lipgloss.Style
}

// plotRows draws the given series as lines on one shared, auto-scaled y axis,
// with dotted gridlines at the threshold values. Each cell's sub-position picks
// a block from the same 8-level vocabulary the header sparkline uses, so a line
// resolves to an eighth of a row rather than snapping to whole cells.
//
// Returned rows include the y-axis gutter, so the whole thing is `cols` wide.
func plotRows(rows, cols int, marks []int, lines ...plotLine) []string {
	if rows < plotMinH || cols < 12 {
		return nil
	}
	lo, hi, ok := plotRange(marks, lines...)
	if !ok {
		return nil
	}

	// gutter holds the widest axis label plus " ┤"
	gutter := len(fmt.Sprintf("%d", hi)) + 2
	pw := cols - gutter
	if pw < 8 {
		return nil
	}

	// cell grid: each cell is either empty, a gridline dot, or a line glyph
	type cell struct {
		r  rune
		st lipgloss.Style
	}
	grid := make([][]cell, rows)
	for i := range grid {
		grid[i] = make([]cell, pw)
	}

	levelOf := func(v int) int { // 0 .. rows*8-1, bottom-up
		return clamp((v-lo)*(rows*8-1)/max(1, hi-lo), 0, rows*8-1)
	}

	// gridlines first, so a data line drawn over one wins the cell.
	// labelAt is keyed by row and filled in marks order — several thresholds
	// can collapse onto one row in a short plot, and ranging a map for the
	// label would make which one shows flicker between frames.
	labelAt := map[int]string{}
	for _, mv := range marks {
		if mv < lo || mv > hi {
			continue
		}
		r := rows - 1 - levelOf(mv)/8
		labelAt[r] = fmt.Sprintf("%d", mv)
		st := dimSt
		if mv >= 78 {
			st = critSt
		} else if mv >= 70 {
			st = warnSt
		}
		for c := 0; c < pw; c++ {
			if c%2 == 0 {
				grid[r][c] = cell{'·', st.Faint(true)}
			}
		}
	}

	for _, ln := range lines {
		for c, v := range resample(ln.vals, pw) {
			if v == unknown {
				continue
			}
			lvl := levelOf(v)
			grid[rows-1-lvl/8][c] = cell{blocks[lvl%8], ln.style}
		}
	}

	// the axis bounds always win their own row over a threshold label
	labelAt[0] = fmt.Sprintf("%d", hi)
	labelAt[rows-1] = fmt.Sprintf("%d", lo)

	out := make([]string, rows)
	for r := 0; r < rows; r++ {
		lab := labelAt[r]
		axis := "┤"
		if r == rows-1 {
			axis = "┼"
		}
		var sb strings.Builder
		sb.WriteString(dimSt.Render(fmt.Sprintf("%*s %s", gutter-2, lab, axis)))
		for _, c := range grid[r] {
			if c.r == 0 {
				sb.WriteRune(' ')
				continue
			}
			sb.WriteString(c.st.Render(string(c.r)))
		}
		out[r] = sb.String()
	}
	return out
}

// plotRange picks the shared y bounds: the data's own span, widened to include
// any threshold close above it and floored at tempSpan so a flat trace doesn't
// fill the plot with noise.
func plotRange(marks []int, lines ...plotLine) (int, int, bool) {
	lo, hi, have := 0, 0, false
	for _, ln := range lines {
		for _, v := range ln.vals {
			if v == unknown {
				continue
			}
			switch {
			case !have:
				lo, hi, have = v, v, true
			case v < lo:
				lo = v
			case v > hi:
				hi = v
			}
		}
	}
	if !have {
		return 0, 0, false
	}
	// pull in the warn threshold when the trace is already near it, so you can
	// see how much headroom is left rather than a full-height trace with no
	// reference
	for _, mv := range marks {
		if mv > hi && mv-hi <= 8 {
			hi = mv
		}
	}
	if hi-lo < tempSpan {
		mid := (hi + lo) / 2
		lo, hi = mid-tempSpan/2, mid+tempSpan/2
	}
	return lo, hi, true
}

// resample compresses vals to exactly n columns by bucket mean, matching the
// header sparkline so the two read as the same data at different zooms. Short
// input is left-padded with gaps rather than stretched — a 5-minute history
// should look like 5 minutes of an hour, not a full plot.
func resample(vals []int, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = unknown
	}
	if len(vals) == 0 {
		return out
	}
	if len(vals) < n {
		copy(out[n-len(vals):], vals)
		return out
	}
	for c := 0; c < n; c++ {
		sum, cnt := 0, 0
		for _, v := range vals[c*len(vals)/n : (c+1)*len(vals)/n] {
			if v != unknown {
				sum, cnt = sum+v, cnt+1
			}
		}
		if cnt > 0 {
			out[c] = sum / cnt
		}
	}
	return out
}

// --- the screen ---

func (m *model) thermalView() string {
	w, h := m.width, m.listH
	var out []string

	// big readouts, dropped first when the terminal is short: they're the
	// glanceable layer, the plot is the one carrying information the header
	// can't already show
	if h >= bigBlockH+1+plotMinH+2+1 {
		out = append(out, m.bigReadouts(w)...)
		out = append(out, "")
	}

	fan := m.fanRow(w)
	plotH := h - len(out) - len(fan) - 2 // axis caption + spacer
	if rows := plotRows(plotH, w-2, m.plotMarks(),
		plotLine{m.hCPU.samples(), cpuSt}, plotLine{m.hRSX.samples(), rsxSt}); rows != nil {
		for _, r := range rows {
			out = append(out, " "+r)
		}
		out = append(out, m.plotCaption(w))
	} else {
		out = append(out, dimSt.Render("  (collecting samples…)"))
	}

	out = append(out, "")
	out = append(out, fan...)
	return strings.Join(out, "\n")
}

// plotMarks are the threshold lines worth drawing: the two band edges from the
// header's color rules, plus the alarm if it sits somewhere else.
func (m *model) plotMarks() []int {
	marks := []int{70, 78}
	if m.alarm != 78 && m.alarm > 0 {
		marks = append(marks, m.alarm)
	}
	// manual mode's override ceiling only earns a gridline when the trace gets
	// near it — plotRange won't stretch the axis to reach a distant mark
	if m.st.MaxTemp > 0 {
		marks = append(marks, m.st.MaxTemp)
	}
	return marks
}

func (m *model) bigReadouts(w int) []string {
	cpu := m.bigMetric("CPU", m.st.CPUTemp, cpuSt)
	rsx := m.bigMetric("RSX", m.st.RSXTemp, rsxSt)

	// lipgloss.Width on a multiline block is its widest line
	gap := 6
	if lipgloss.Width(cpu)+lipgloss.Width(rsx)+gap > w {
		gap = 2
	}
	joined := lipgloss.JoinHorizontal(lipgloss.Top, cpu, strings.Repeat(" ", gap), rsx)
	return strings.Split(lipgloss.NewStyle().Width(w).Align(lipgloss.Center).Render(joined), "\n")
}

// bigMetric is the glyph block plus its label and band word. The digits take
// the band color, so the readout is readable — and diagnostic — from across
// the room.
func (m *model) bigMetric(name string, v int, nameSt lipgloss.Style) string {
	text, band := "—", dimSt.Render("no reading")
	st := dimSt
	if v != unknown {
		text = fmt.Sprintf("%d°", v)
		switch {
		case v >= m.alarm || v >= 78:
			st, band = critSt, critSt.Render("HOT")
		case v >= 70:
			st, band = warnSt, warnSt.Render("warm")
		default:
			st, band = okSt, okSt.Render("normal")
		}
	}
	rows := bigText(text, st)
	width := max(bigTextWidth(text), 12)
	center := lipgloss.NewStyle().Width(width).Align(lipgloss.Center)
	head := center.Render(nameSt.Bold(true).Render(name))
	foot := center.Render(band)
	return strings.Join(append(append([]string{head}, rows...), foot), "\n")
}

func (m *model) plotCaption(w int) string {
	left := dimSt.Render(fmt.Sprintf(" └ %s ago", fmtDur(int(m.interval.Seconds())*histLen)))
	right := cpuSt.Render("── CPU") + "  " + rsxSt.Render("── RSX") + dimSt.Render("  now ┘")
	pad := w - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + right
}

// targetOverAlarm reports whether a dynamic-mode target sits at or above the
// alarm threshold — the point where webMAN won't ramp the fan until you're
// already in the band you asked to be warned about. ?up will push it past the
// ~85° the console shuts itself down at, at which point the target does
// nothing at all.
func targetOverAlarm(max, alarm int) bool { return max > 0 && max >= alarm }

// fanHint names what ↑↓ actually moves, which depends on the mode: the target
// temperature in dynamic, the speed in manual. Labelling it "adjust"
// everywhere would hide the one thing that makes those keys confusing.
func (m *model) fanHint() string {
	switch strings.ToLower(m.st.FanMode) {
	case "dynamic":
		return "↑↓ target"
	case "manual":
		return "↑↓ speed"
	}
	return "↑↓ adjust"
}

// fanModeHint: f cycles webMAN's strategies but can't hand control back to
// SYSCON, so from SYSCON it's labelled as the one-way step it is.
func (m *model) fanModeHint() string {
	if strings.EqualFold(m.st.FanMode, "SYSCON") {
		return "f take over"
	}
	return "f mode"
}

// fanQueueLabel summarises what's still queued. A pending mode change is called
// out rather than folded into the count: it's the one command that changes what
// the others will do when they land.
func fanQueueLabel(q []string) string {
	net, mode := 0, false
	for _, c := range q {
		switch c {
		case fanUp:
			net++
		case fanDown:
			net--
		case fanMode:
			mode = true
		}
	}
	s := "⋯"
	if net != 0 {
		s += fmt.Sprintf("%+d", net)
	}
	if mode {
		s += "·mode"
	}
	return s
}

// fanRow carries the fan state and its own controls — the point of putting fan
// control on this screen is that the feedback is right above it.
func (m *model) fanRow(w int) []string {
	mode := m.st.FanMode
	if mode == "" {
		mode = "?"
	}
	modeSt := dimSt
	if !strings.EqualFold(mode, "SYSCON") {
		// anything but SYSCON means the console's own ramp is off, which is
		// the state worth noticing before a long session
		modeSt = accentSt
	}
	base := "  " + dimSt.Render("FAN  ") + fanVal(m.st.FanPct) + "  " + modeSt.Render(strings.ToUpper(mode))

	// dynamic mode holds a target temperature instead of a fixed speed, and
	// that target is what ↑↓ moves there. A target at or above the alarm
	// means webMAN won't ramp until you're already in trouble — and ?up will
	// happily push it past the ~85° the console shuts itself down at — so it
	// gets the same red as a temperature in that band.
	ceiling := ""
	if m.st.MaxTemp > 0 {
		st := dimSt
		if targetOverAlarm(m.st.MaxTemp, m.alarm) {
			st = critSt
		}
		ceiling = st.Render(fmt.Sprintf(" · target %d°", m.st.MaxTemp))
	}

	// pending presses show immediately, so a key registers before the console
	// has answered — the round trip is ~half a second and silence reads as a
	// dropped keystroke
	pending := ""
	switch {
	case len(m.fanQueue) > 0:
		pending = accentSt.Render(" " + fanQueueLabel(m.fanQueue))
	case m.fanBusy:
		pending = accentSt.Render(" ⋯")
	}

	trace := ""
	if sp := spark(m.hFan.samples(), sparkW*2, fanSpan); sp != "" {
		trace = "  " + dimSt.Render(sp)
	}

	// widest that fits; the controls hint is never what gives way, since it's
	// the only place this screen documents itself
	right := dimSt.Render(m.fanHint() + " · " + m.fanModeHint() + " ")
	left := base + pending
	for _, cand := range []string{base + pending + ceiling + trace, base + pending + ceiling, base + pending} {
		if lipgloss.Width(cand)+lipgloss.Width(right)+1 <= w {
			left = cand
			break
		}
	}
	pad := w - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	return []string{left + strings.Repeat(" ", pad) + right}
}
