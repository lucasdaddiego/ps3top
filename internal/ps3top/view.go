package ps3top

// Every frame of the main screen: the status header, tab strip, game list,
// art panel and footer, plus the styles and the small formatters they share
// (the thermal screen draws itself in thermal.go with these same styles).

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	headerH = 6 // 4-line status box + breathing line + tab line
	footerH = 1 // single bookend keybar
)

var (
	dimSt    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	okSt     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	warnSt   = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	critSt   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	accentSt = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	selSt    = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Bold(true)
	dangerSt = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color("124")).Bold(true)
	askSt    = lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(lipgloss.Color("220")).Bold(true)

	selBg      = lipgloss.Color("236")
	selBarSt   = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Background(selBg).Bold(true)
	selMainSt  = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(selBg).Bold(true)
	selDimSt   = lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Background(selBg)
	selOkSt    = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Background(selBg).Bold(true)
	coverBoxSt = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("238"))

	tabOnSt  = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color("39")).Bold(true)
	tabOffSt = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	// series colors for the thermal plot. Color encodes which sensor; the
	// gridlines encode the thermal bands — so the lines stay identifiable even
	// when both are deep in the red.
	cpuSt = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	rsxSt = lipgloss.NewStyle().Foreground(lipgloss.Color("213"))
)

// View hands the frame to bubbletea. Alt screen is a property of the view in
// v2 rather than a program option, and the renderer diffs cells itself — the
// v1-era trick of appending an alternating SGR reset so idle ticker wakeups
// stayed cheap is gone with the renderer that needed it.
func (m *model) View() tea.View {
	v := tea.NewView(m.frame())
	v.AltScreen = true
	return v
}

// frame is the whole screen as one styled string: header, body, footer.
func (m *model) frame() string {
	if m.width == 0 {
		return "starting…"
	}
	head := m.header()
	var body string
	if m.thermalOn {
		body = m.thermalView() // takes the full width; no art panel beside it
	} else {
		body = m.renderList()
		if m.artShown() {
			body = m.withArtPanel(body)
		}
	}
	body = lipgloss.NewStyle().Height(m.listH).MaxHeight(m.listH).Render(body)
	// hard-clip every line to the terminal width: a wrapped line would push
	// every line below it down a row and desync the frame
	return lipgloss.NewStyle().MaxWidth(m.width).Render(head + "\n" + body + "\n" + m.footer())
}

func (m *model) header() string {
	// the whole frame turns red while the temp alarm is active
	b := dimSt
	if m.alarming {
		b = critSt
	}

	conn := okSt.Render("online")
	if !m.online {
		conn = critSt.Render("offline")
	}
	topLeft := accentSt.Bold(true).Render("ps3top") + dimSt.Render(" · "+m.host+" · ") + conn
	topRight := ""
	if m.st.Firmware != "" {
		topRight = dimSt.Render(shortFW(m.st.Firmware) + " · wM " + m.st.WMVersion)
	}
	l0 := boxEdge(m.width, b, "╭", "╮", topLeft, topRight)

	l1 := m.metricsRow(b)

	// game state closes the box as its last content line
	var state string
	switch {
	case !m.haveStatus && m.lastErr == nil:
		// no poll has answered yet, either way — not the same as offline
		state = dimSt.Render("waiting for first status…")
	case !m.online && m.scanning:
		state = critSt.Render("✕ console unreachable — ") + dimSt.Render("scanning "+m.scanNets+" for webMAN…")
	case !m.online:
		msg := "unreachable"
		if m.lastErr != nil {
			msg = m.lastErr.Error()
		}
		state = critSt.Render("✕ console unreachable — retrying ") + dimSt.Render("("+msg+")")
	case m.st.InGame:
		state = okSt.Render("▶ ") + selSt.Render(m.st.GameTitle)
		if m.st.GameVer != "" {
			state += dimSt.Render(" v" + m.st.GameVer)
			// Sony still lists the patches; when the installed version
			// trails the newest, say so where the version already is
			if latest := m.patches[m.st.GameID]; patchBehind(m.st.GameVer, latest) {
				state += warnSt.Render(" → " + latest + " available")
			}
		}
		state += dimSt.Render(" · " + m.st.GameID)
	case m.st.MountedISO != "":
		state = okSt.Render("○ ") + "XMB" + dimSt.Render(" · ") +
			okSt.Render("● "+path.Base(m.st.MountedISO)) + dimSt.Render(" mounted")
	default:
		state = okSt.Render("○ ") + "XMB" + dimSt.Render(" · no disc")
	}
	play := ""
	if m.online && m.st.InGame && m.st.PlayTime != "" {
		play = dimSt.Render("play " + fmtClock(m.st.PlayTime))
	}
	l2 := boxMid(m.width, b, state, play)
	l3 := boxEdge(m.width, b, "╰", "╯", "", "")

	return l0 + "\n" + l1 + "\n" + l2 + "\n" + l3 + "\n\n" + m.tabLine()
}

// metricsRow packs the metrics, their sparklines, and the syscon clocks into
// one line, giving up the least live thing first when they don't all fit: the
// boot counters only move once per boot, the sparklines move all day. Widest
// combination that fits wins, so a narrow window degrades instead of
// overflowing into View's clip.
func (m *model) metricsRow(b lipgloss.Style) string {
	bare, sparked := "", ""
	if m.online && m.haveStatus {
		bare, sparked = m.metricLine(false), m.metricLine(true)
	}
	clocks := m.clockVariants()
	for _, left := range []string{sparked, bare} {
		for _, right := range clocks {
			if lipgloss.Width(left)+lipgloss.Width(right)+6 <= m.width {
				return boxMid(m.width, b, left, dimSt.Render(right))
			}
		}
	}
	return boxMid(m.width, b, bare, "")
}

// clockVariants lists the right-hand clock block from richest to poorest. The
// ∞ day counter is the part worth keeping longest — the boots/hard-off detail
// is the first to go.
func (m *model) clockVariants() []string {
	life, short, up := "", "", ""
	if m.st.LifeDays > 0 {
		life = fmt.Sprintf("∞ %dd · %d boots · %d hard-off", m.st.LifeDays, m.st.Boots, m.st.HardOffs)
		short = fmt.Sprintf("∞ %dd", m.st.LifeDays)
	}
	if m.online && m.st.Uptime != "" {
		up = "up " + fmtClock(m.st.Uptime)
	}
	var out []string
	for _, parts := range [][]string{{life, up}, {short, up}, {up}, {}} {
		var keep []string
		for _, p := range parts {
			if p != "" {
				keep = append(keep, p)
			}
		}
		if s := strings.Join(keep, " · "); len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}

// Minimum sparkline span per metric — how much change has to happen before the
// plot uses its full height. Without a floor, 1° of idle sensor jitter would
// draw the same dramatic slope as a real climb into the alarm band.
const (
	tempSpan = 10 // °C
	fanSpan  = 20 // percentage points
)

// metricLine builds the metrics row. Sparklines ride CPU/RSX/FAN only: HDD
// moves on a weekly scale and MEM is quantized by whether a game is running,
// so neither has a shape worth reading at minute resolution.
func (m *model) metricLine(sparks bool) string {
	cpu := tempPart("CPU", m.st.CPUTemp, m.alarm)
	rsx := tempPart("RSX", m.st.RSXTemp, m.alarm)
	fan := dimSt.Render("FAN ") + fanVal(m.st.FanPct)
	if !strings.EqualFold(m.st.FanMode, "SYSCON") && m.st.FanMode != "" {
		fan += dimSt.Render(" " + strings.ToLower(m.st.FanMode))
	}
	if sparks {
		cpu += sparkPart(&m.hCPU, tempSpan, true)
		rsx += sparkPart(&m.hRSX, tempSpan, true)
		fan += sparkPart(&m.hFan, fanSpan, false)
	}
	return cpu + "   " + rsx + "   " + fan + "   " +
		dimSt.Render("HDD ") + hddVal(m.st.HDDFreeGB) + "   " +
		dimSt.Render("MEM ") + memVal(m.st.MemFreeKB)
}

func sparkPart(s *series, minSpan int, withTrend bool) string {
	vals := s.samples()
	sp := spark(vals, sparkW, minSpan)
	if sp == "" {
		return ""
	}
	out := " " + dimSt.Render(sp)
	if withTrend {
		out += trendMark(trend(vals))
	}
	return out
}

// trendMark colors by consequence, not by direction: a climb is the thing you
// might have to act on, so it takes the warn color, while cooling is good news
// and stays dim. A move inside the noise band gets no mark at all.
func trendMark(d int) string {
	switch {
	case d >= trendMin:
		return warnSt.Render("↗")
	case d <= -trendMin:
		return dimSt.Render("↘")
	}
	return ""
}

// boxEdge draws "╭─ left ────── right ─╮"-style border lines, ANSI-aware.
func boxEdge(width int, b lipgloss.Style, lc, rc string, left, right string) string {
	s := b.Render(lc + "─")
	if left != "" {
		s += " " + left + " "
	}
	tail := b.Render("─" + rc)
	if right != "" {
		tail = " " + right + " " + tail
	}
	return s + rule(b, width-lipgloss.Width(s)-lipgloss.Width(tail)) + tail
}

// rule is n columns of horizontal line in a style, the frame's one vocabulary
// for "this space is deliberately empty". Never negative: a too-narrow window
// gets no rule rather than a panic.
func rule(st lipgloss.Style, n int) string {
	return st.Render(strings.Repeat("─", max(0, n)))
}

// boxMid draws "│ left …pad… right │", ANSI-aware.
func boxMid(width int, b lipgloss.Style, left, right string) string {
	line := b.Render("│") + " " + left
	rseg := ""
	if right != "" {
		rseg = right + " "
	}
	tail := b.Render("│")
	pad := max(1, width-lipgloss.Width(line)-lipgloss.Width(rseg)-lipgloss.Width(tail))
	return line + strings.Repeat(" ", pad) + rseg + tail
}

// noVal is what an absent field renders as. Every metric formatter routes
// through it, so a webMAN markup change shows up as a visible gap in the
// dashboard instead of a confident, healthy-looking zero.
func noVal() string { return dimSt.Render("—") }

// fanVal: 40% manual is the community-recommended normal on HEN, and SYSCON
// is fine "if it stays under 70% in games" — plain <50, yellow 50–69, red 70+.
func fanVal(pct int) string {
	if pct == unknown {
		return noVal()
	}
	v := fmt.Sprintf("%d%%", pct)
	switch {
	case pct >= 70:
		return critSt.Render(v)
	case pct >= 50:
		return warnSt.Render(v)
	}
	return okSt.Render(v)
}

// hddVal: a dual-layer PS3 ISO runs up to ~45GB — under 60G free means one
// big game left, under 20G means none.
func hddVal(gb float64) string {
	if gb == unknown {
		return noVal()
	}
	v := fmt.Sprintf("%.0fG", gb)
	switch {
	case gb < 20:
		return critSt.Render(v)
	case gb < 60:
		return warnSt.Render(v)
	}
	return okSt.Render(v)
}

// memVal: webMAN reports free available memory (meminfo.avail); ~1MB free
// in-game is normal since the game owns the RAM. Only genuinely tight values
// color: yellow <512K, red <256K.
func memVal(kb int) string {
	if kb == unknown {
		return noVal()
	}
	v := fmt.Sprintf("%.1fM", float64(kb)/1024)
	switch {
	case kb < 256:
		return critSt.Render(v)
	case kb < 512:
		return warnSt.Render(v)
	}
	return okSt.Render(v)
}

// shortFW compresses webMAN's firmware line for the corner:
// "4.93 CEX PS3HEN 3.5.0" → "4.93 HEN"
func shortFW(fw string) string {
	fields := strings.Fields(fw)
	if len(fields) == 0 {
		return fw
	}
	out := fields[0]
	if strings.Contains(fw, "HEN") {
		out += " HEN"
	} else if len(fields) > 1 {
		out += " " + fields[1]
	}
	return out
}

// fmtClock compresses webMAN's HH:MM:SS to "1h31m" / "31m" / "45s". Anything
// that isn't a clock is passed through rather than rendered as a confident
// "0s" — same reasoning as unknown vs 0 on the metrics.
func fmtClock(c string) string {
	if strings.Count(c, ":") != 2 {
		return c
	}
	return fmtDur(clockSecs(c))
}

// tabLine is the single separator line: console tabs (chronological) on the
// left, filter state / cursor position on the right.
// ── ⟨ PSX · 1 ⟩⟨ PS3 · 21 ⟩ ─────────────── 16/21 ──
func (m *model) tabLine() string {
	// the thermal screen borrows the tab strip as its own title bar — the
	// console tabs mean nothing while the list is hidden
	if m.thermalOn {
		left := dimSt.Render("── ") + tabOnSt.Render(" thermals ") + dimSt.Render(" ")
		right := fmt.Sprintf(" alarm %d° · window %s ", m.alarm, fmtDur(int(m.interval.Seconds())*histLen))
		return left + rule(dimSt, m.width-lipgloss.Width(left)-lipgloss.Width(right)-2) + dimSt.Render(right+"──")
	}

	var tabs strings.Builder
	for i, c := range m.consoles {
		label := fmt.Sprintf(" %s · %d ", c, m.counts[i])
		if i == m.activeTab {
			tabs.WriteString(tabOnSt.Render(label))
		} else {
			tabs.WriteString(tabOffSt.Render(label))
		}
		if i < len(m.consoles)-1 {
			tabs.WriteString(dimSt.Render("│"))
		}
	}
	left := dimSt.Render("── ") + tabs.String() + dimSt.Render(" ")

	pos := ""
	switch {
	case m.filterQ != "" && len(m.order) > 0:
		pos = fmt.Sprintf(" “%s” · %d/%d ", m.filterQ, m.cursor+1, len(m.order))
	case m.filterQ != "":
		pos = fmt.Sprintf(" “%s” · 0 matches ", m.filterQ)
	case len(m.order) > 0:
		pos = fmt.Sprintf(" %d/%d ", m.cursor+1, len(m.order))
	}
	if m.sortRecent { // alphabetical is the default, so only the other one is named
		pos = " recent ·" + pos
	}
	return left + rule(dimSt, m.width-lipgloss.Width(left)-lipgloss.Width(pos)-2) + dimSt.Render(pos+"──")
}

// PS3 thermal reality (PSX-Place/GBAtemp consensus, webMAN's own fan target
// is 68°): 60s–low 70s is normal gaming. Plain below 70, yellow 70–77,
// red at 78+ (or the alarm threshold if set lower).
func tempVal(v, alarm int) string {
	if v == unknown {
		return noVal()
	}
	val := fmt.Sprintf("%d°", v)
	switch {
	case v >= alarm || v >= 78:
		return critSt.Render(val)
	case v >= 70:
		return warnSt.Render(val)
	}
	return okSt.Render(val)
}

func tempPart(name string, v int, alarm int) string {
	return dimSt.Render(name+" ") + tempVal(v, alarm)
}

func (m *model) renderList() string {
	if len(m.order) == 0 {
		msg := "no " + m.activeConsole() + " games"
		if m.filterQ != "" {
			msg = "nothing matches “" + m.filterQ + "”"
		}
		return lipgloss.Place(m.listWidth(), m.listH, lipgloss.Center, lipgloss.Center, dimSt.Render(msg))
	}
	out := make([]string, 0, m.listH)
	for i := m.offset; i < len(m.order) && i < m.offset+m.listH; i++ {
		out = append(out, m.renderRow(m.order[i], i == m.cursor))
	}
	for len(out) < m.listH {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

// idColW is the ID column: title IDs are nine characters, every one.
const idColW = 9

// rowStyles is the palette a list row is drawn with. The selected row carries
// a background across its whole width, so every segment — including the
// padding — goes through a style; the plain row's "styles" are mostly the
// identity, which is what lets one render path serve both.
type rowStyles struct {
	bar           string
	dim, main, ok lipgloss.Style
}

var (
	rowPlain = rowStyles{bar: "  ", dim: dimSt, main: lipgloss.NewStyle(), ok: okSt}
	rowSel   = rowStyles{bar: selBarSt.Render("▌ "), dim: selDimSt, main: selMainSt, ok: selOkSt}
)

func (m *model) renderRow(gi int, sel bool) string {
	g := m.games[gi]
	w := m.listWidth()
	numW := max(1, m.numW)
	st := rowPlain
	if sel {
		st = rowSel
	}

	num := fmt.Sprintf("%*d", numW, m.secNums[gi])
	idCol := fmt.Sprintf("%-*s", idColW, g.ID) // aligned, so no brackets to delimit it
	mark := ""
	if m.mounted(g) {
		mark = " ● mounted"
	}
	// the play column is sized once for the whole library and reserved on
	// every row, so titles don't go ragged between played and unplayed games.
	// It only earns its place when the art panel isn't showing: with the
	// panel up the total sits under the cover, and the column's width is
	// worth more to the cover than to a number repeated beside every row.
	played := ""
	if m.playColW > 0 && !m.artShown() {
		if s, ok := m.hist.stat(g); ok {
			played = fmtDur(s.Secs)
		}
		played = fmt.Sprintf("%*s", m.playColW, played)
	}

	// stable title column: as wide as the longest title, shrunk only if the
	// window can't fit it
	avail := w - 2 - numW - 2 - 1 - idColW - len(" x mounted") - 1
	if played != "" {
		avail -= m.playColW + 2 // +2 keeps it off the mounted mark
	}
	title := truncPad(g.Title, clamp(m.titleColW, 10, avail))

	var b strings.Builder
	b.WriteString(st.bar)
	b.WriteString(st.dim.Render(num + "  "))
	b.WriteString(st.main.Render(title + " "))
	b.WriteString(st.dim.Render(idCol))
	if mark != "" {
		b.WriteString(st.ok.Render(mark))
	}
	// the highlight runs a space past the content, not to the list's edge —
	// a wide window hands the list every column the cover can't use, and a
	// selection bar that long points at nothing. The play column is the
	// exception: it sits at the edge, so the row has to reach it.
	end := min(w, lipgloss.Width(b.String())+1)
	if played != "" {
		end = w
	}
	if pad := end - lipgloss.Width(b.String()) - lipgloss.Width(played); pad > 0 {
		b.WriteString(st.main.Render(strings.Repeat(" ", pad)))
	}
	if played != "" {
		b.WriteString(st.dim.Render(played))
	}
	return b.String()
}

// withArtPanel sets the art panel beside the list, centered in whatever the
// list's content leaves: vertically in the list's height, horizontally in
// the columns between the widest row and the right edge. The rows stop at
// their content (so the list block is as wide as its longest row), and the
// cover is capped by the terminal's height, so on a wide window there is
// slack — a panel hugging either side of it reads as misplaced, a centered
// one as framed.
func (m *model) withArtPanel(list string) string {
	panel := m.artPanel()
	if top := (m.listH - lipgloss.Height(panel)) / 2; top > 0 {
		panel = strings.Repeat("\n", top) + panel
	}
	cols, _ := m.artBox()
	// the panel's width by construction — never measured, since its
	// placeholder cells are not something to ask a width function about
	left := max(2, (m.width-lipgloss.Width(list)-(cols+2))/2)
	lines := strings.Split(panel, "\n")
	pad := strings.Repeat(" ", left)
	for i, l := range lines {
		lines[i] = pad + l
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, list, strings.Join(lines, "\n"))
}

func (m *model) artPanel() string {
	g, ok := m.selectedGame()
	if !ok {
		return ""
	}

	artCols, artRows := m.artBox()
	var cover string
	if ref := m.covers[g.IconPath]; ref.shown {
		// the placement sits centered in the box at the image's own aspect;
		// the cells around it are plain blanks, not placeholders. Padded by
		// hand: a placeholder cell is a private-use rune plus two combining
		// marks, which is not something to ask a width function about.
		left := (artCols - ref.cols) / 2
		top := (artRows - ref.rows) / 2
		blank := strings.Repeat(" ", artCols)
		rows := make([]string, 0, artRows)
		for r := 0; r < top; r++ {
			rows = append(rows, blank)
		}
		for r := range ref.rows {
			rows = append(rows, strings.Repeat(" ", left)+placementRow(ref.id, r, ref.cols)+strings.Repeat(" ", max(0, artCols-left-ref.cols)))
		}
		for len(rows) < artRows {
			rows = append(rows, blank)
		}
		cover = strings.Join(rows, "\n")
	} else {
		// "no cover" is a settled answer, "· · ·" a pending one — a cover
		// webMAN can't serve shouldn't look like one still loading
		wait := "· · ·"
		if m.coverFailed[g.IconPath] {
			wait = "no cover"
		}
		cover = lipgloss.Place(artCols, artRows, lipgloss.Center, lipgloss.Center, dimSt.Render(wait))
	}

	// Under the box, artTextRows lines at most — every row the text doesn't
	// take is a row the cover can have. The title is clipped to one line
	// rather than wrapped, the mounted mark rides on it, and the play total
	// and last-played share a line.
	cw := artCols + 2
	center := lipgloss.NewStyle().Width(cw).Align(lipgloss.Center)
	title := g.Title
	if m.mounted(g) {
		title = okSt.Render("● ") + title
	}
	meta := g.ID
	if meta == "" {
		meta = g.Console()
	}
	if cat := strings.TrimPrefix(g.Category, "hdd0/"); cat != "" {
		meta += dimSt.Render(" · " + cat)
	}
	if g.Ver != "" {
		meta += dimSt.Render(" · v" + g.Ver)
	}
	parts := []string{
		coverBoxSt.Render(cover),
		center.Bold(true).Render(ansi.Truncate(title, cw, "…")),
		center.Render(dimSt.Render(ansi.Truncate(meta, cw, "…"))),
	}
	if s, ok := m.hist.stat(g); ok {
		play := fmt.Sprintf("%s · %d %s", fmtDur(s.Secs), s.Sessions, plural(s.Sessions, "session"))
		if a := fmtAgo(s.Last); a != "" {
			play += " · last " + a
		}
		parts = append(parts, center.Render(dimSt.Render(ansi.Truncate(play, cw, "…"))))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// footer is a single bookend line; modal states render inside the same rails.
func (m *model) footer() string {
	if m.confirm != nil {
		switch {
		case m.confirm.unsure:
			// don't name a title here: the reason for the prompt is that the
			// last reading can't be trusted, and quoting it would imply it can
			return m.bookend(dangerSt.Render(" ⚠ console state unknown — a game may be running · " + m.confirm.label + "? y/N "))
		case m.confirm.danger:
			warnGame := m.st.GameTitle
			if warnGame == "" {
				warnGame = "a game"
			}
			return m.bookend(dangerSt.Render(" ⚠ " + warnGame + " is running — unsaved progress will be lost · " + m.confirm.label + "? y/N "))
		}
		return m.bookend(askSt.Render(" " + m.confirm.label + "? y/N "))
	}
	if m.popupOn {
		return m.bookend("popup: " + m.popupInput.View() + dimSt.Render("  (⏎ send · esc cancel)"))
	}
	if m.filterTyping {
		return m.bookend("/ " + m.filterInput.View() + dimSt.Render("  (⏎ keep · esc clear)"))
	}
	if m.flash != "" {
		return m.bookend(warnSt.Render(m.flash))
	}
	if m.thermalOn {
		// through fanHint, not a fixed label: ↑↓ move the target temperature in
		// dynamic mode, and the footer used to insist they were "fan speed"
		// while the fan row two lines above correctly said "target"
		return m.bookend(dimSt.Render(m.fanHint() + " (or +/−) · " + m.fanModeHint() + " · r refresh · esc back"))
	}
	// Widest form that fits: a clipped keybar loses whichever keys happen to
	// sit at the end, rather than the ones you're least likely to need. The
	// refresh keys ride well down the ladder — they're what a just-FTP'd ISO
	// sends you looking for, and the full bar only fits a ~137-col terminal.
	variants := []string{
		"⏎ mount/launch · u eject · p play · x quit game · ⇥ console · / filter · s sort · t thermals · m popup · r refresh · g rescan · S/R power · q quit",
		"⏎ mount · p play · u eject · ⇥ console · / filter · s sort · t thermals · r refresh · g rescan · q quit",
		"⏎ mount · p play · u eject · / filter · s sort · t thermals · r refresh · g rescan · q quit",
		"⏎ mount · p play · / filter · s sort · t thermals · r refresh · q quit",
		"⏎ mount · p play · / filter · s sort · t thermals · q quit",
		"⏎ mount · / filter · t thermals · q quit",
	}
	prefix := ""
	if m.filterQ != "" {
		prefix = "esc clear · "
	}
	keys := variants[len(variants)-1]
	for _, v := range variants {
		if lipgloss.Width(prefix+v)+5 <= m.width {
			keys = v
			break
		}
	}
	return m.bookend(dimSt.Render(prefix + keys))
}

// bookend wraps content in the "── content ────" rule vocabulary.
func (m *model) bookend(content string) string {
	lead := dimSt.Render("── ")
	return lead + content + " " + rule(dimSt, m.width-lipgloss.Width(lead)-lipgloss.Width(content)-1)
}

// --- helpers ---

func samePath(a, b string) bool {
	ua, err1 := url.PathUnescape(a)
	ub, err2 := url.PathUnescape(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return ua == ub
}

// truncPad clips and pads by DISPLAY width (a CJK rune occupies two columns,
// so rune counts would blow the column for Japanese titles).
func truncPad(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	if pad := w - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		hi = lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
