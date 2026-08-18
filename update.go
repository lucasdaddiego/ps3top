package main

// The async half of the model: every network command, the gate that keeps
// webMAN to one console-state request at a time, and the Update/handleKey
// loop that routes messages and keys into state changes.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	offlineRetry = 15 * time.Second

	// A rescan makes webMAN walk every ISO directory and rewrite the XML, so it
	// gets far longer than the 6s an ordinary action does. The settle delay is
	// the gap between webMAN answering and ps3top re-reading the list — cheap
	// insurance against the XML still being mid-rewrite when the reply lands.
	rescanTimeout = 30 * time.Second
	rescanSettle  = 1500 * time.Millisecond
)

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

// rescan asks webMAN to re-scan the ISO folders (a freshly FTP'd game isn't in
// mygames.xml until it does). It holds the action gate like any other
// state-changing command — the console is genuinely busy while it scans — and
// the flash goes up immediately because the round trip is seconds, not
// milliseconds, and silence would read as a dead key.
func (m *model) rescan() tea.Cmd {
	if m.actBusy || m.fanBusy {
		m.flash = "busy — waiting for the last command"
		return m.clearFlashLater()
	}
	m.actBusy = true
	m.flash = "rescanning library…"
	cli := m.cli
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), rescanTimeout)
		defer cancel()
		return rescanMsg{err: cli.Rescan(ctx)}
	}
}

func doAction(label string, fn func(context.Context) error) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		return actionMsg{label, fn(ctx)}
	}
}

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
		// a reload must not dump the cursor back to row 1 — indexes don't
		// survive the rebuild, so the selection is re-found by identity after
		prevSel, hadSel := m.selectedGame()
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
		m.reselect(prevSel, hadSel)
		return m, m.ensureCover()

	case actionMsg:
		m.actBusy = false
		if msg.err != nil {
			m.flash = msg.label + ": " + msg.err.Error()
			return m, m.clearFlashLater()
		}
		m.flash = msg.label + " ✓"
		return m, tea.Batch(m.refreshNow(), m.clearFlashLater())

	case rescanMsg:
		m.actBusy = false
		if msg.err != nil {
			m.flash = "rescan: " + msg.err.Error()
			return m, tea.Batch(m.clearFlashLater(), m.catchUp())
		}
		m.flash = "library rescanned ✓"
		return m, tea.Batch(
			m.clearFlashLater(),
			tea.Tick(rescanSettle, func(time.Time) tea.Msg { return reloadMsg{} }),
			m.catchUp(),
		)

	case reloadMsg:
		return m, m.fetchGames()

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
		// refresh everything on screen: the status poll defers if the console
		// is busy, the list re-fetch is an idempotent read outside the gate
		return m, tea.Batch(m.refreshNow(), m.fetchGames())
	case "g":
		return m, m.rescan()
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
