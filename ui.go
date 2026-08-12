package main

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
	"github.com/sahilm/fuzzy"
)

const (
	headerH      = 6 // 4-line status box + breathing line + tab line
	footerH      = 1 // single bookend keybar
	scrollMargin = 2 // rows kept visible above/below the cursor
	offlineRetry = 15 * time.Second
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

type (
	tickMsg       struct{}
	clearFlashMsg struct{ gen int }
	statusMsg     struct {
		st  Status
		err error
	}
	gamesMsg struct {
		games []Game
		err   error
	}
	actionMsg struct {
		label string
		err   error
	}
	coverMsg struct {
		icon string
		id   int
		err  error
	}
	// fanMsg carries the status page the fan endpoint returned, plus what the
	// fan read before it — so the UI can report the move that actually
	// happened rather than assume the step landed.
	fanMsg struct {
		cmd      string
		prevPct  int
		prevMax  int
		prevMode string
		st       Status
		err      error
	}
)

type confirmAction struct {
	label  string
	danger bool
	// unsure means the prompt is up because ps3top can't tell whether a game is
	// running, not because it knows one is — the footer has to say so rather
	// than name a title from a status read that may be minutes stale.
	unsure bool
	run    func(context.Context) error
	// raw runs instead of run when set, as a thunk so the action is built at
	// confirm time rather than when the prompt is raised — fan commands mutate
	// the queue and report their own delta, so they can't go through doAction.
	raw func() tea.Cmd
}

type model struct {
	cli      *Client
	host     string
	interval time.Duration
	alarm    int
	artOn    bool
	cacheDir string

	width, height int
	listH         int

	st         Status
	online     bool
	haveStatus bool
	lastErr    error
	// Three flags rather than one because they gate different things. busy()
	// is the union, and is what keeps a scheduled poll from racing a reply;
	// but a plain status poll must not block a state-changing command, or a
	// confirmed eject would get dropped for landing in the wrong 6 seconds.
	inFlight   bool // status poll out
	actBusy    bool // mount/play/eject/popup/power out
	needStatus bool // a poll came due while busy; take it once the path is clear
	// how many fields the last successful poll couldn't find, so the
	// "markup changed" flash can fire on the way INTO a worse state rather
	// than once per process — see the statusMsg handler
	prevMissing int

	hCPU, hRSX, hFan series

	hist       *history
	sortRecent bool
	playColW   int  // width reserved for the per-row play total (0 = no history)
	thermalOn  bool // `t` — full-body temperature screen with fan control

	// fan presses are queued and sent one at a time, in press order — see queueFan
	fanBusy  bool
	fanQueue []string // pending fanUp/fanDown/fanMode, oldest first

	// open session, closed out when the game stops or ps3top quits. Length
	// comes from webMAN's PlayTime, so it survives a mid-game restart.
	sesOn                       bool
	sesID, sesTitle             string
	sesSecs, sesPeakC, sesPeakR int

	games     []Game
	consoles  []string // consoles present, chronological (PSX, PS2, PSP, PS3)
	counts    []int    // library size per console, aligned with consoles
	activeTab int
	titleColW int
	secNums   []int // per-console 1-based number, aligned with games
	numW      int
	order     []int // visible games (active tab + filter) as indexes into games
	cursor    int   // index into order
	offset    int   // first visible row

	filterQ      string
	filterTyping bool
	filterInput  textinput.Model

	confirm    *confirmAction
	popupOn    bool
	popupInput textinput.Model
	flash      string
	flashGen   int // invalidates stale clearFlashMsg ticks
	alarming   bool

	coverIDs    map[string]int
	transmitted map[int]bool
	coverBusy   map[string]bool
	nextID      int

	flip bool // alternates per frame — see View
}

func newModel(cli *Client, host string, interval time.Duration, alarm int, artOn bool, cacheDir string, hist *history) *model {
	m := &model{
		cli:         cli,
		host:        host,
		interval:    interval,
		alarm:       alarm,
		artOn:       artOn && artSupported(),
		cacheDir:    cacheDir,
		hist:        hist,
		coverIDs:    map[string]int{},
		transmitted: map[int]bool{},
		coverBusy:   map[string]bool{},
	}
	if m.hist == nil { // every read path dereferences it; an empty log is the no-op
		m.hist = &history{stats: map[string]gameStat{}, firstHDD: unknown, lastHDD: unknown}
	}
	pi := textinput.New()
	pi.Placeholder = "message to the TV"
	pi.CharLimit = 120
	m.popupInput = pi
	fi := textinput.New()
	fi.Placeholder = "type to filter"
	fi.CharLimit = 60
	m.filterInput = fi
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.fetchStatus(), m.fetchGames(), m.tick(m.interval))
}

// --- commands ---

func (m *model) tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *model) fetchStatus() tea.Cmd {
	m.inFlight = true
	cli := m.cli
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		st, err := cli.Status(ctx)
		return statusMsg{st, err}
	}
}

func (m *model) fetchGames() tea.Cmd {
	cli := m.cli
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		g, err := cli.Games(ctx)
		return gamesMsg{g, err}
	}
}

// fanQueueMax bounds the queue so a leaned-on key can't build a backlog of
// requests the console will still be working through a minute later.
const fanQueueMax = 20

// fanReady reports whether the fan controls know what they would be doing.
// ↑↓ move the fan percentage in manual mode and the target temperature in
// dynamic, and f is a one-way door out of SYSCON — none of that can be
// labelled honestly, or confirmed, before a status has said which mode the
// console is in. The keys used to work anyway from the very first frame.
func (m *model) fanReady() bool {
	return m.haveStatus && m.st.FanMode != ""
}

// queueFan records a press and starts draining if nothing is in flight. Fan
// requests are serialized deliberately: webMAN's server has ~4 session slots,
// and firing one GET per keypress turns a quick 24%→40% adjustment into a
// pile-up that reads as an unresponsive UI.
//
// The queue is an ordered list, not a net counter plus a mode flag. Those
// commands don't all mean the same thing — ?up moves the percentage in manual
// mode and the target temperature in dynamic — and the old shape always sent a
// queued ?mode FIRST, ahead of presses made before it. So: manual mode, press
// up twice to cool the console, then press f. The mode change went out first,
// and the two ups that were meant to raise the fan raised the temperature
// ceiling instead. Cooling intent, inverted, on the screen whose whole job is
// to not cook the console. Press order is now preserved exactly.
func (m *model) queueFan(cmd string) tea.Cmd {
	// an immediate reversal cancels instead of queueing both — leaning on ↑ and
	// correcting with ↓ shouldn't cost two round trips. Tail only, so it can
	// never reorder anything across a ?mode.
	if n := len(m.fanQueue); n > 0 && m.fanQueue[n-1] == inverseFan(cmd) {
		m.fanQueue = m.fanQueue[:n-1]
		return m.drainFan()
	}
	if len(m.fanQueue) >= fanQueueMax {
		return m.drainFan()
	}
	m.fanQueue = append(m.fanQueue, cmd)
	return m.drainFan()
}

// inverseFan is the command that undoes cmd, or "" for one that can't be undone
// (a mode change is a one-way cycle, never cancelled against a pending step).
func inverseFan(cmd string) string {
	switch cmd {
	case fanUp:
		return fanDown
	case fanDown:
		return fanUp
	}
	return ""
}

func (m *model) drainFan() tea.Cmd {
	if m.fanBusy || len(m.fanQueue) == 0 {
		return nil // the in-flight reply will drain the rest
	}
	cmd := m.fanQueue[0]
	m.fanQueue = m.fanQueue[1:]
	m.fanBusy = true
	cli, prevPct, prevMax, prevMode := m.cli, m.st.FanPct, m.st.MaxTemp, m.st.FanMode
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		st, err := cli.Fan(ctx, cmd)
		return fanMsg{cmd, prevPct, prevMax, prevMode, st, err}
	}
}

// fanReport says what the console actually did, by diffing every field a fan
// command can move rather than assuming which one it moved.
//
// That generality is the point: ?up/?dn adjust the *target temperature* in
// dynamic mode and the *fan percentage* in manual, so a report written around
// one of them announces "unchanged" while the other quietly moves — which is
// exactly how this was wrong the first time.
func fanReport(msg fanMsg) string {
	var parts []string
	prevMode, nowMode := strings.ToLower(msg.prevMode), strings.ToLower(msg.st.FanMode)
	switch {
	case nowMode != "" && prevMode == "":
		parts = append(parts, "mode "+nowMode)
	case nowMode != "" && nowMode != prevMode:
		parts = append(parts, "mode "+prevMode+" → "+nowMode)
	}
	if msg.prevPct != unknown && msg.st.FanPct != unknown && msg.prevPct != msg.st.FanPct {
		parts = append(parts, fmt.Sprintf("fan %d%% → %d%%", msg.prevPct, msg.st.FanPct))
	}
	if msg.prevMax > 0 && msg.st.MaxTemp > 0 && msg.prevMax != msg.st.MaxTemp {
		parts = append(parts, fmt.Sprintf("target %d° → %d°", msg.prevMax, msg.st.MaxTemp))
	}
	if len(parts) > 0 {
		return strings.Join(parts, " · ")
	}
	if msg.st.FanPct == unknown {
		return "fan: no reading"
	}
	return fmt.Sprintf("nothing changed (fan %d%%%s)", msg.st.FanPct, modeSuffix(nowMode))
}

func modeSuffix(mode string) string {
	if mode == "" {
		return ""
	}
	return ", " + mode
}

func doAction(label string, fn func(context.Context) error) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		return actionMsg{label, fn(ctx)}
	}
}

// busy reports whether any request carrying console state is out. This is what
// polling defers on: a status page fetched alongside a fan reply can arrive
// after it and overwrite the newer reading with the older one.
func (m *model) busy() bool { return m.inFlight || m.actBusy || m.fanBusy }

// act runs a state-changing request. Every action path goes through here, and
// only one runs at a time: webMAN answers on ~4 session slots with no ordering
// guarantee, so two actions in flight land in whichever order it gets to them.
//
// A status poll in flight is deliberately NOT a reason to refuse — it carries
// no state, and refusing on it would mean an eject the user had already
// confirmed could vanish because a background poll happened to be mid-request.
func (m *model) act(label string, fn func(context.Context) error) tea.Cmd {
	if m.actBusy || m.fanBusy {
		m.flash = "busy — waiting for the last command"
		return m.clearFlashLater()
	}
	m.actBusy = true
	return doAction(label, fn)
}

// refreshNow polls if the path is clear, and otherwise records that a poll is
// owed so catchUp can take it later.
func (m *model) refreshNow() tea.Cmd {
	if m.busy() {
		m.needStatus = true
		return nil
	}
	return m.fetchStatus()
}

// catchUp takes the poll that was skipped while the console was busy, once
// nothing is in flight and the fan queue has drained.
func (m *model) catchUp() tea.Cmd {
	if !m.needStatus || m.busy() || len(m.fanQueue) > 0 {
		return nil
	}
	m.needStatus = false
	return m.fetchStatus()
}

func (m *model) ensureCover() tea.Cmd {
	// artShown, not artOn: with the panel hidden (narrow window) a fetch would
	// hit the PS3 and burn a transmission id for zero visible output
	if !m.artShown() {
		return nil
	}
	g, ok := m.selectedGame()
	if !ok || g.IconPath == "" {
		return nil
	}
	icon := g.IconPath
	if id, seen := m.coverIDs[icon]; seen && m.transmitted[id] {
		return nil
	}
	if m.coverBusy[icon] {
		return nil
	}
	id, seen := m.coverIDs[icon]
	if !seen {
		m.nextID++
		if m.nextID > 255 {
			m.artOn = false // out of 256-color-encodable ids; give up gracefully
			return nil
		}
		id = m.nextID
		m.coverIDs[icon] = id
	}
	m.coverBusy[icon] = true
	cli, cacheDir := m.cli, m.cacheDir
	return func() tea.Msg {
		png, err := loadCover(cli, cacheDir, icon)
		if err != nil {
			return coverMsg{icon, id, err}
		}
		rawWrite(transmitEscapes(id, png))
		return coverMsg{icon, id, nil}
	}
}

// --- history ---

func (m *model) pushSamples(st Status) {
	m.hCPU.push(st.CPUTemp)
	m.hRSX.push(st.RSXTemp)
	m.hFan.push(st.FanPct)
}

// trackSession folds one poll into the open play session, closing it out when
// the game stops or changes. Reports whether it set a flash.
func (m *model) trackSession(st Status) bool {
	if !st.InGame {
		return m.flushSession()
	}
	flashed := false
	if m.sesOn && statKey(m.sesID, m.sesTitle) != statKey(st.GameID, st.GameTitle) {
		flashed = m.flushSession() // straight from one game into another
		if m.sesOn {
			// the append failed and the old session is still open — folding the
			// new game's counter into it would corrupt both
			return flashed
		}
	}
	// PlayTime that didn't parse reads as 0. That's absence of information, not
	// a session that restarted: letting it through would zero the counter and
	// throw the session away if no good poll followed.
	if st.PlaySecs <= 0 {
		return flashed
	}
	// The counter only climbs within one run of a game, so a drop means the game
	// stopped and started again between two polls — a new session under a title
	// the check above can't distinguish. Without this the earlier session was
	// silently overwritten by the shorter reading and never recorded.
	if m.sesOn && st.PlaySecs < m.sesSecs {
		if m.flushSession() {
			flashed = true
		}
		if m.sesOn {
			return flashed
		}
	}
	if !m.sesOn {
		m.sesOn = true
		m.sesID, m.sesTitle = st.GameID, st.GameTitle
		m.sesSecs, m.sesPeakC, m.sesPeakR = 0, 0, 0
	}
	// webMAN's own counter, not a clock we started — correct even if ps3top
	// joined the session late or was restarted mid-game
	m.sesSecs = st.PlaySecs
	if st.CPUTemp != unknown && st.CPUTemp > m.sesPeakC {
		m.sesPeakC = st.CPUTemp
	}
	if st.RSXTemp != unknown && st.RSXTemp > m.sesPeakR {
		m.sesPeakR = st.RSXTemp
	}
	return flashed
}

// flushSession appends the open session to the history log. Reports whether it
// set a flash.
func (m *model) flushSession() bool {
	if !m.sesOn {
		return false
	}
	secs, title, id := m.sesSecs, m.sesTitle, m.sesID
	peakC, peakR := m.sesPeakC, m.sesPeakR
	if secs <= 0 || title == "" {
		m.sesOn = false
		return false // nothing worth recording (or PlayTime didn't parse)
	}

	// Started is what makes this record identifiable as one session across ps3top
	// restarts: Secs is webMAN's cumulative PlayTime, so quitting mid-game and
	// coming back writes the total twice, and only a stable start time lets the
	// two be recognised as the same play rather than added together.
	now := timeNow()
	rec := sessionRec{
		ID:      id,
		Title:   title,
		Started: now.Add(-time.Duration(secs) * time.Second),
		End:     now,
		Secs:    secs,
		PeakCPU: peakC,
		PeakRSX: peakR,
	}
	if m.st.HDDFreeGB != unknown {
		rec.HDDFreeGB = m.st.HDDFreeGB
	}
	if err := m.hist.add(rec); err != nil {
		// the session stays open so the next poll retries. Closing it here meant
		// a failed write lost the record for good, while the totals on screen
		// still counted it until restart.
		m.flash = "history: " + err.Error()
		return true
	}
	m.sesOn = false
	m.recalcPlayCol()
	m.flash = fmt.Sprintf("%s — %s", title, fmtDur(secs))
	if peakC > 0 {
		m.flash += fmt.Sprintf(" (peak %d°/%d°)", peakC, peakR)
	}
	return true
}

// recalcPlayCol sizes the per-row play column to the widest total in the
// library, so the column is stable across rows instead of ragged.
func (m *model) recalcPlayCol() {
	m.playColW = 0
	for _, g := range m.games {
		if s, ok := m.hist.stat(g); ok {
			if n := len(fmtDur(s.Secs)); n > m.playColW {
				m.playColW = n
			}
		}
	}
}

// --- list mechanics ---

func (m *model) activeConsole() string {
	if m.activeTab < 0 || m.activeTab >= len(m.consoles) {
		return "PS3"
	}
	return m.consoles[m.activeTab]
}

func (m *model) selectedGame() (Game, bool) {
	if m.cursor < 0 || m.cursor >= len(m.order) {
		return Game{}, false
	}
	return m.games[m.order[m.cursor]], true
}

// applyFilter rebuilds the visible rows: active console's games, narrowed by
// the fuzzy filter when set.
func (m *model) applyFilter(q string) {
	m.filterQ = q
	console := m.activeConsole()
	var pool []int
	for i, g := range m.games {
		if g.Console() == console {
			pool = append(pool, i)
		}
	}
	if m.sortRecent {
		// stable, so games with no history keep the alphabetical order they
		// arrived in and just fall below the played ones
		sort.SliceStable(pool, func(i, j int) bool {
			si, oki := m.hist.stat(m.games[pool[i]])
			sj, okj := m.hist.stat(m.games[pool[j]])
			if oki != okj {
				return oki
			}
			if !oki {
				return false
			}
			return si.Last.After(sj.Last)
		})
	}
	if q == "" {
		m.order = pool
	} else {
		targets := make([]string, len(pool))
		for i, gi := range pool {
			targets[i] = m.games[gi].Title + " " + m.games[gi].ID
		}
		m.order = m.order[:0]
		matched := map[int]bool{}
		for _, mt := range fuzzy.Find(q, targets) {
			matched[pool[mt.Index]] = true
		}
		for _, gi := range pool { // keep alphabetical order
			if matched[gi] {
				m.order = append(m.order, gi)
			}
		}
	}
	m.cursor, m.offset = 0, 0
}

// reorder re-applies the current filter under a changed sort, keeping the
// cursor on whatever game it was on — a sort toggle that dumps you back at
// row 1 makes it useless for "where did that one go".
func (m *model) reorder() {
	sel, had := 0, false
	if m.cursor >= 0 && m.cursor < len(m.order) {
		sel, had = m.order[m.cursor], true
	}
	m.applyFilter(m.filterQ)
	if had {
		for i, gi := range m.order {
			if gi == sel {
				m.cursor = i
				break
			}
		}
		m.ensureVisible()
	}
}

func (m *model) switchTab(d int) tea.Cmd {
	if len(m.consoles) < 2 {
		return nil
	}
	m.activeTab = (m.activeTab + d + len(m.consoles)) % len(m.consoles)
	m.applyFilter(m.filterQ)
	return m.ensureCover()
}

func (m *model) moveCursor(d int) {
	m.cursor = clamp(m.cursor+d, 0, max(0, len(m.order)-1))
	m.ensureVisible()
}

// ensureVisible scrolls line-by-line, keeping scrollMargin rows of context
// around the cursor — no page flips. The margin shrinks when the list is too
// short to satisfy both sides, else the viewport oscillates on alternate
// keypresses (top and bottom constraints fight over the same rows).
func (m *model) ensureVisible() {
	if m.listH <= 0 {
		return
	}
	mg := min(scrollMargin, (m.listH-1)/2)
	if lo := m.offset + mg; m.cursor < lo {
		m.offset = m.cursor - mg
	} else if hi := m.offset + m.listH - 1 - mg; m.cursor > hi {
		m.offset = m.cursor - m.listH + 1 + mg
	}
	m.offset = clamp(m.offset, 0, max(0, len(m.order)-m.listH))
}

// --- update ---

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.listH = max(3, m.height-headerH-footerH)
		m.ensureVisible()
		// widening past the art threshold must load the now-visible cover
		return m, m.ensureCover()

	case tickMsg:
		next := m.interval
		if !m.online && next < offlineRetry {
			next = offlineRetry
		}
		// a fan command or action mid-flight will answer with fresher state than
		// this poll would, and overlapping them lets an older status page
		// overwrite a newer fan reading — so defer rather than race
		var cmd tea.Cmd
		if m.busy() {
			m.needStatus = true
		} else {
			cmd = m.fetchStatus()
		}
		return m, tea.Batch(cmd, m.tick(next))

	case statusMsg:
		m.inFlight, m.needStatus = false, false
		if msg.err != nil {
			m.online = false
			m.lastErr = msg.err
			// record the outage as gaps so the sparkline's x axis stays
			// honest — an offline stretch is blank, not silently compressed
			m.pushSamples(Status{CPUTemp: unknown, RSXTemp: unknown, FanPct: unknown})
			return m, nil
		}
		m.online, m.haveStatus, m.lastErr = true, true, nil
		m.st = msg.st
		m.pushSamples(msg.st)
		nowAlarming := msg.st.CPUTemp >= m.alarm || msg.st.RSXTemp >= m.alarm
		if nowAlarming && !m.alarming {
			rawWrite("\a")
		}
		m.alarming = nowAlarming

		flashed := m.trackSession(msg.st)
		// Warn on the way INTO a worse state, not once per process. The
		// missing fields stay visible as em dashes for as long as they're
		// missing, so this flash is the explanation, not the signal — but a
		// single latched bool spent it on the first transient blip and then
		// said nothing when the markup genuinely broke hours later. Comparing
		// against the previous count also keeps a steady-state break quiet
		// instead of flashing every 15 seconds.
		if n := msg.st.missing(); n > m.prevMissing {
			m.flash = fmt.Sprintf("status page: %d of %d fields not found — webMAN markup may have changed", n, statusFields)
			flashed = true
		}
		// deliberately not updated on the error path above: an outage isn't a
		// markup change, and recovering from one into the same degraded state
		// shouldn't re-warn
		m.prevMissing = msg.st.missing()
		if flashed {
			return m, m.clearFlashLater()
		}
		return m, nil

	case gamesMsg:
		if msg.err != nil {
			m.flash = "games list: " + msg.err.Error()
			return m, m.clearFlashLater()
		}
		prevConsole := m.activeConsole()
		m.games = msg.games
		m.titleColW = 20
		m.secNums = make([]int, len(msg.games))
		perSec := map[string]int{}
		for i, g := range msg.games {
			c := g.Console()
			perSec[c]++
			m.secNums[i] = perSec[c]
			if n := lipgloss.Width(g.Title); n > m.titleColW {
				m.titleColW = n
			}
		}
		m.consoles = m.consoles[:0]
		m.counts = m.counts[:0]
		maxSec := 1
		for _, c := range consoleOrder {
			if n := perSec[c]; n > 0 {
				m.consoles = append(m.consoles, c)
				m.counts = append(m.counts, n)
				if n > maxSec {
					maxSec = n
				}
			}
		}
		m.numW = len(strconv.Itoa(maxSec))
		m.recalcPlayCol()
		m.activeTab = 0
		for i, c := range m.consoles {
			if c == prevConsole {
				m.activeTab = i
			}
		}
		m.applyFilter(m.filterQ)
		return m, m.ensureCover()

	case actionMsg:
		m.actBusy = false
		if msg.err != nil {
			m.flash = msg.label + ": " + msg.err.Error()
			return m, m.clearFlashLater()
		}
		m.flash = msg.label + " ✓"
		return m, tea.Batch(m.refreshNow(), m.clearFlashLater())

	case coverMsg:
		m.coverBusy[msg.icon] = false
		if msg.err == nil {
			m.transmitted[msg.id] = true
		}
		return m, nil

	case fanMsg:
		m.fanBusy = false
		if msg.err != nil {
			m.fanQueue = nil // don't keep hammering a console that just failed
			m.flash = "fan: " + msg.err.Error()
			return m, tea.Batch(m.clearFlashLater(), m.catchUp())
		}
		// the endpoint answered with the whole status page, so adopt it as the
		// current reading — no follow-up poll needed for the display. It
		// deliberately doesn't push samples or fold the play session: that's
		// what the deferred poll behind catchUp is for, so a burst of fan
		// presses can't over-sample the history rings.
		m.online, m.haveStatus, m.lastErr = true, true, nil
		m.st = msg.st
		m.flash = fanReport(msg)
		// drainFan first: it claims fanBusy for the next command, so catchUp
		// correctly sees the console as still busy and waits its turn
		return m, tea.Batch(m.clearFlashLater(), m.drainFan(), m.catchUp())

	case clearFlashMsg:
		if msg.gen == m.flashGen {
			m.flash = ""
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		m.flushSession()
		clearImages()
		return m, tea.Quit
	}

	// modal: confirm prompt
	if m.confirm != nil {
		c := m.confirm
		m.confirm = nil
		switch msg.String() {
		case "y", "Y", "enter":
			if c.raw != nil {
				return m, c.raw()
			}
			return m, m.act(c.label, c.run)
		}
		m.flash = "cancelled"
		return m, m.clearFlashLater()
	}

	// modal: popup input
	if m.popupOn {
		switch msg.String() {
		case "esc":
			m.popupOn = false
			return m, nil
		case "enter":
			text := strings.TrimSpace(m.popupInput.Value())
			m.popupOn = false
			if text == "" {
				return m, nil
			}
			cli := m.cli
			return m, m.act("popup", func(ctx context.Context) error { return cli.Popup(ctx, text) })
		}
		var cmd tea.Cmd
		m.popupInput, cmd = m.popupInput.Update(msg)
		return m, cmd
	}

	// modal: filter input (list stays visible and filters live)
	if m.filterTyping {
		switch msg.String() {
		case "esc":
			m.filterTyping = false
			m.applyFilter("")
			return m, m.ensureCover()
		case "enter":
			m.filterTyping = false
			return m, nil
		case "up", "down":
			d := 1
			if msg.String() == "up" {
				d = -1
			}
			m.moveCursor(d)
			return m, m.ensureCover()
		case "tab", "shift+tab":
			d := 1
			if msg.String() == "shift+tab" {
				d = -1
			}
			return m, m.switchTab(d)
		}
		var cmd tea.Cmd
		m.filterInput, cmd = m.filterInput.Update(msg)
		m.applyFilter(m.filterInput.Value())
		return m, tea.Batch(cmd, m.ensureCover())
	}

	// screen: thermals. Fan control lives here rather than on the main keybar —
	// one key instead of four, and the controls sit under their own feedback.
	if m.thermalOn {
		switch msg.String() {
		case "t", "esc", "q":
			m.thermalOn = false
			return m, m.ensureCover() // the cover for the selected row comes back
		// arrows drive the fan here: there's no list to navigate on this
		// screen, so they're free and they're the obvious thing to reach for
		case "up", "k", "+", "=":
			return m, m.fanKey(fanUp)
		case "down", "j", "-", "_":
			return m, m.fanKey(fanDown)
		case "f":
			if !m.fanReady() {
				return m, m.fanUnready()
			}
			// ?mode only cycles webMAN's own strategies (Lowest/Manual/Auto)
			// and every one of them re-enables webMAN fan control, so this is
			// a one-way door out of SYSCON: getting back means unticking
			// "Enable dynamic fan control" on /setup.ps3, which is a 143-field
			// form ps3top has no business submitting.
			if strings.EqualFold(m.st.FanMode, "SYSCON") {
				m.confirm = &confirmAction{
					label: "leave SYSCON? only webMAN's setup page can switch back",
					raw:   func() tea.Cmd { return m.queueFan(fanMode) },
				}
				return m, nil
			}
			return m, m.queueFan(fanMode)
		case "r":
			if !m.busy() {
				return m, m.fetchStatus()
			}
		}
		return m, nil
	}

	cli := m.cli
	switch msg.String() {
	case "q":
		m.flushSession()
		clearImages()
		return m, tea.Quit
	case "t":
		m.thermalOn = true
		return m, nil
	case "up", "k":
		m.moveCursor(-1)
		return m, m.ensureCover()
	case "down", "j":
		m.moveCursor(1)
		return m, m.ensureCover()
	case "pgup", "ctrl+u":
		m.moveCursor(-max(1, m.listH-1))
		return m, m.ensureCover()
	case "pgdown", "ctrl+d":
		m.moveCursor(max(1, m.listH-1))
		return m, m.ensureCover()
	case "home":
		m.cursor = 0
		m.ensureVisible()
		return m, m.ensureCover()
	case "end":
		m.cursor = max(0, len(m.order)-1)
		m.ensureVisible()
		return m, m.ensureCover()
	case "tab", "right", "l":
		return m, m.switchTab(1)
	case "shift+tab", "left", "h":
		return m, m.switchTab(-1)
	case "s":
		m.sortRecent = !m.sortRecent
		m.reorder()
		return m, m.ensureCover()
	case "/":
		m.filterTyping = true
		m.filterInput.SetValue(m.filterQ)
		return m, m.filterInput.Focus()
	case "esc":
		if m.filterQ != "" {
			m.applyFilter("")
			return m, m.ensureCover()
		}
		return m, nil
	case "r":
		if !m.busy() {
			return m, m.fetchStatus()
		}
		return m, nil
	case "g":
		return m, m.fetchGames()
	case "m":
		m.popupOn = true
		m.popupInput.SetValue("")
		return m, m.popupInput.Focus()
	case "u":
		return m.guarded("eject", func(ctx context.Context) error { return cli.Eject(ctx) })
	case "S":
		m.confirm = &confirmAction{label: "shutdown", danger: m.st.InGame, run: func(ctx context.Context) error { return cli.Shutdown(ctx) }}
		return m, nil
	case "R":
		m.confirm = &confirmAction{label: "restart", danger: m.st.InGame, run: func(ctx context.Context) error { return cli.Restart(ctx) }}
		return m, nil
	case "p":
		g, ok := m.selectedGame()
		if !ok {
			return m, nil
		}
		return m.guarded("play "+g.Title, func(ctx context.Context) error { return cli.Play(ctx, g) })
	case "enter":
		g, ok := m.selectedGame()
		if !ok {
			return m, nil
		}
		// launch is guarded too: the game you're about to (re)launch is very
		// often the one already running — same title, already mounted, cursor
		// sitting on its row — and /play.ps3 doesn't ask either
		if m.mounted(g) {
			return m.guarded("launch "+g.Title, func(ctx context.Context) error { return cli.Launch(ctx) })
		}
		return m.guarded("mount "+g.Title, func(ctx context.Context) error { return cli.Mount(ctx, g) })
	}
	return m, nil
}

// fanKey applies a fan step, or explains why it can't.
func (m *model) fanKey(cmd string) tea.Cmd {
	if !m.fanReady() {
		return m.fanUnready()
	}
	return m.queueFan(cmd)
}

func (m *model) fanUnready() tea.Cmd {
	m.flash = "fan: waiting for a status read — mode unknown"
	return m.clearFlashLater()
}

// guarded runs immediately on XMB, but demands a red confirm while in-game —
// note /mount_ps3 bypasses webMAN's own in-game mount protection, so this
// dialog is the only net.
//
// It confirms just as hard when it can't tell. Before the first status, or
// after a failed poll, m.st is either empty or stale and "no game is running"
// is a guess, not a reading. The old condition was `online && InGame`, which
// meant the guard was strongest while telemetry was healthy and simply absent
// the moment it wasn't — mount and eject fired immediately on a console that
// might well have been mid-game.
func (m *model) guarded(label string, fn func(context.Context) error) (tea.Model, tea.Cmd) {
	switch {
	case !m.haveStatus || !m.online:
		m.confirm = &confirmAction{label: label, danger: true, unsure: true, run: fn}
		return m, nil
	case m.st.InGame:
		m.confirm = &confirmAction{label: label, danger: true, run: fn}
		return m, nil
	}
	return m, m.act(label, fn)
}

func (m *model) mounted(g Game) bool {
	return m.online && m.st.MountedISO != "" && samePath(m.st.MountedISO, g.Path)
}

func (m *model) clearFlashLater() tea.Cmd {
	m.flashGen++
	gen := m.flashGen
	return tea.Tick(4*time.Second, func(time.Time) tea.Msg { return clearFlashMsg{gen} })
}

func (m *model) artShown() bool { return m.artOn && m.width >= 84 }

func (m *model) listWidth() int {
	w := m.width
	if m.artShown() {
		w -= artCols + 2 + 2 // cover border + gap
	}
	return max(20, w)
}

// --- view ---

func (m *model) View() string {
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
			body = lipgloss.JoinHorizontal(lipgloss.Top, body, "  ", m.artPanel())
		}
	}
	body = lipgloss.NewStyle().Height(m.listH).MaxHeight(m.listH).Render(body)
	// hard-clip every line to the terminal width: a single wrapped line would
	// desync bubbletea's line-diff renderer and garble the whole frame
	frame := lipgloss.NewStyle().MaxWidth(m.width).Render(head + "\n" + body + "\n" + m.footer())
	// force-dirty: bubbletea v1.3.10's flush() never resets its buffer when a
	// written frame equals the previous render (standard_renderer.go), which
	// parks the FPS ticker in a loop that re-allocates the whole frame for the
	// comparison, 30×/s, all day. Alternating an invisible SGR reset makes
	// every frame distinct, so the real render path runs once per message and
	// resets the buffer — idle ticker wakeups then cost ~nothing.
	m.flip = !m.flip
	if m.flip {
		return frame + "\x1b[0m"
	}
	return frame
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
	case !m.haveStatus && m.online:
		state = dimSt.Render("waiting for first status…")
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
	fill := width - lipgloss.Width(s) - lipgloss.Width(tail)
	if fill < 0 {
		fill = 0
	}
	return s + b.Render(strings.Repeat("─", fill)) + tail
}

// boxMid draws "│ left …pad… right │", ANSI-aware.
func boxMid(width int, b lipgloss.Style, left, right string) string {
	line := b.Render("│") + " " + left
	rseg := ""
	if right != "" {
		rseg = right + " "
	}
	tail := b.Render("│")
	pad := width - lipgloss.Width(line) - lipgloss.Width(rseg) - lipgloss.Width(tail)
	if pad < 1 {
		pad = 1
	}
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
		fill := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 2
		if fill < 0 {
			fill = 0
		}
		return left + dimSt.Render(strings.Repeat("─", fill)) + dimSt.Render(right) + dimSt.Render("──")
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
	fill := m.width - lipgloss.Width(left) - lipgloss.Width(pos) - 2
	if fill < 0 {
		fill = 0
	}
	return left + dimSt.Render(strings.Repeat("─", fill)) + dimSt.Render(pos) + dimSt.Render("──")
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

func (m *model) renderRow(gi int, sel bool) string {
	g := m.games[gi]
	w := m.listWidth()
	numW := max(1, m.numW)

	num := fmt.Sprintf("%*d", numW, m.secNums[gi])
	idCol := strings.Repeat(" ", 11)
	if g.ID != "" {
		idCol = "[" + g.ID + "]"
	}
	mark := ""
	if m.mounted(g) {
		mark = " ● mounted"
	}
	// the play column is sized once for the whole library and reserved on
	// every row, so titles don't go ragged between played and unplayed games
	played := ""
	if m.playColW > 0 {
		if s, ok := m.hist.stat(g); ok {
			played = fmtDur(s.Secs)
		}
		played = fmt.Sprintf("%*s", m.playColW, played)
	}

	// stable title column: as wide as the longest title, shrunk only if the
	// window can't fit it
	avail := w - 2 - numW - 2 - 1 - 11 - len(" x mounted") - 1
	if m.playColW > 0 {
		avail -= m.playColW + 2 // +2 keeps it off the mounted mark
	}
	tw := clamp(m.titleColW, 10, avail)
	title := truncPad(g.Title, tw)

	if sel {
		var b strings.Builder
		b.WriteString(selBarSt.Render("▌ "))
		b.WriteString(selDimSt.Render(num + "  "))
		b.WriteString(selMainSt.Render(title + " "))
		b.WriteString(selDimSt.Render(idCol))
		if mark != "" {
			b.WriteString(selOkSt.Render(mark))
		}
		if pad := w - lipgloss.Width(b.String()) - lipgloss.Width(played); pad > 0 {
			b.WriteString(selMainSt.Render(strings.Repeat(" ", pad)))
		}
		if played != "" {
			b.WriteString(selDimSt.Render(played))
		}
		return b.String()
	}

	out := "  " + dimSt.Render(num) + "  " + title + " " + dimSt.Render(idCol)
	if mark != "" {
		out += " " + okSt.Render("● mounted")
	}
	if played != "" {
		if pad := w - lipgloss.Width(out) - lipgloss.Width(played); pad > 0 {
			out += strings.Repeat(" ", pad)
		}
		out += dimSt.Render(played)
	}
	return out
}

func (m *model) artPanel() string {
	g, ok := m.selectedGame()
	if !ok {
		return ""
	}

	var cover string
	if id, seen := m.coverIDs[g.IconPath]; seen && m.transmitted[id] {
		rows := make([]string, artRows)
		for r := 0; r < artRows; r++ {
			rows[r] = placementRow(id, r)
		}
		cover = strings.Join(rows, "\n")
	} else {
		cover = lipgloss.Place(artCols, artRows, lipgloss.Center, lipgloss.Center, dimSt.Render("· · ·"))
	}

	cw := artCols + 2
	center := lipgloss.NewStyle().Width(cw).Align(lipgloss.Center)
	meta := g.ID
	if meta == "" {
		meta = g.Console()
	}
	if cat := strings.TrimPrefix(g.Category, "hdd0/"); cat != "" {
		meta += dimSt.Render(" · " + cat)
	}
	parts := []string{
		coverBoxSt.Render(cover),
		center.Bold(true).Render(g.Title),
		center.Render(dimSt.Render(meta)),
	}
	if s, ok := m.hist.stat(g); ok {
		parts = append(parts, center.Render(dimSt.Render(
			fmt.Sprintf("%s · %d %s", fmtDur(s.Secs), s.Sessions, plural(s.Sessions, "session")))))
		if a := fmtAgo(s.Last); a != "" {
			parts = append(parts, center.Render(dimSt.Render("last "+a)))
		}
	}
	if m.mounted(g) {
		parts = append(parts, center.Render(okSt.Render("● mounted")))
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
	// sit at the end, rather than the ones you're least likely to need.
	variants := []string{
		"⏎ mount/launch · u eject · p play · ⇥ console · / filter · s sort · t thermals · m popup · g reload · r refresh · S/R power · q quit",
		"⏎ mount · p play · u eject · ⇥ console · / filter · s sort · t thermals · q quit",
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
	fill := m.width - lipgloss.Width(lead) - lipgloss.Width(content) - 1
	if fill < 0 {
		fill = 0
	}
	return lead + content + " " + dimSt.Render(strings.Repeat("─", fill))
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
		s = truncate.StringWithTail(s, uint(w), "…")
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
