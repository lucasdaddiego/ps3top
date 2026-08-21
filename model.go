package main

// The model: every piece of state the TUI owns, the messages that mutate it,
// and the pure list mechanics (filter / sort / cursor / tabs). Anything that
// talks to the network lives in update.go; anything that draws lives in
// view.go.

import (
	"context"
	"sort"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/sahilm/fuzzy"
)

const (
	scrollMargin = 2 // rows kept visible above/below the cursor
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
	// rescanMsg is /refresh.ps3's reply; the list reload it earns is scheduled
	// separately (reloadMsg) so webMAN has a beat to finish writing the XML.
	rescanMsg struct{ err error }
	reloadMsg struct{}
	// coverTickMsg is the debounce behind a console cover fetch: it fires
	// once the cursor has rested, and only the newest generation is honoured.
	coverTickMsg struct{ gen int }
	// coverMsg carries a loaded cover; coverShownMsg follows once the bytes
	// have gone to the terminal, and is what lets the panel reference them.
	coverMsg struct {
		icon string
		c    cover
		err  error
	}
	coverShownMsg struct{ icon string }
	// discoverMsg is a background LAN sweep's answer, run when the cached
	// address stops answering (DHCP moved the console, or it was off).
	discoverMsg struct {
		host string
		err  error
	}
	// fanMsg carries the status page the fan endpoint returned, plus what the
	// fan read before it — so the UI can report the move that actually
	// happened rather than assume the step landed.
	fanMsg struct {
		prevPct  int
		prevMax  int
		prevMode string
		st       Status
		err      error
	}
)

// coverRef is one cover's life in the terminal: the kitty image id it was
// (or will be) transmitted under, the placement shaped to its aspect, and
// whether the bytes have actually gone out — placeholder cells for an id the
// terminal doesn't hold yet render as nothing.
type coverRef struct {
	id         int
	cols, rows int
	shown      bool
}

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
	// hostCache is the last-good-address file, set only when the host was
	// auto-discovered: it's what licenses a background re-sweep when that
	// address stops answering. A --host the user typed is never second-guessed.
	hostCache string
	scanning  bool   // a sweep is out; the header says so
	scanNets  string // the subnets it's probing, for that message
	swept     bool   // one sweep per offline stretch — 254 dials is not a retry loop

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
	actBusy    bool // mount/play/eject/popup/power/rescan out
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

	covers      map[string]coverRef // icon path → what the terminal holds for it
	coverFailed map[string]bool     // icons that failed this session; a list reload retries them
	coverBusy   string              // icon path of the one load in flight ("" = idle)
	coverGen    int                 // invalidates stale coverTickMsg debounce ticks
	nextID      int
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
		covers:      map[string]coverRef{},
		coverFailed: map[string]bool{},
	}
	if m.hist == nil { // every read path dereferences it; an empty log is the no-op
		m.hist = &history{stats: map[string]gameStat{}}
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

// busy reports whether any request carrying console state is out. This is what
// polling defers on: a status page fetched alongside a fan reply can arrive
// after it and overwrite the newer reading with the older one.
func (m *model) busy() bool { return m.inFlight || m.actBusy || m.fanBusy }

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

// sameGame says whether two entries name the same library item across a list
// reload. Path is the identity webMAN itself mounts by; PSX entries can lack
// an ID, and titles repeat across consoles, so the fallbacks stay in that order.
func sameGame(a, b Game) bool {
	if a.Path != "" && b.Path != "" {
		return samePath(a.Path, b.Path)
	}
	if a.ID != "" || b.ID != "" {
		return a.ID == b.ID
	}
	return a.Title == b.Title && a.Category == b.Category
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

// reselect puts the cursor back on a game after the games slice itself was
// rebuilt — indexes don't survive a reload, so this matches by identity. A
// game that vanished (or an empty prev) leaves the cursor at the top.
func (m *model) reselect(prev Game, had bool) {
	if !had {
		return
	}
	for i, gi := range m.order {
		if sameGame(m.games[gi], prev) {
			m.cursor = i
			break
		}
	}
	m.ensureVisible()
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
