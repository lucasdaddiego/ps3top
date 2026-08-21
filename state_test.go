package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// exec runs a tea.Cmd and hands its message back through Update, which is what
// the bubbletea runtime does. Without it the async half of the model — the part
// that owns every network transition — is never exercised.
func exec(t *testing.T, m *model, cmd tea.Cmd) *model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	if msg == nil {
		return m
	}
	if parts := cmdsIn(msg); parts != nil { // a batch or a sequence
		for _, c := range parts {
			m = exec(t, m, c)
		}
		return m
	}
	mm, _ := m.Update(msg)
	return mm.(*model)
}

// liveServer answers status, games and covers, so a model can be driven end to
// end without touching a console.
func liveServer(t *testing.T) *Client {
	t.Helper()
	status, err := os.ReadFile("testdata/cpursx_ingame.html")
	if err != nil {
		t.Fatal(err)
	}
	games, err := os.ReadFile("testdata/mygames.xml")
	if err != nil {
		t.Fatal(err)
	}
	png := tinyPNG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "mygames.xml"):
			w.Write(games)
		case strings.HasSuffix(strings.ToUpper(r.URL.Path), ".PNG"):
			w.Write(png)
		default:
			w.Write(status)
		}
	}))
	t.Cleanup(srv.Close)
	return NewClient(strings.TrimPrefix(srv.URL, "http://"))
}

// msgsOf executes a command and returns every message it produces, flattening
// batches, without feeding any of them back — for asserting what a transition
// *scheduled* rather than where it ended up.
func msgsOf(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, msgsOf(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// Init is the status poll and the poll timer, nothing else: the game list
// follows the first good status rather than riding alongside it, so launch is
// one connection at a time and a host that isn't webMAN is never asked for a
// list. This pins both halves — Init alone establishes a status and opens the
// session, and it's the transition to online that fetches the games.
func TestInitFetchesStatusThenGamesOnTheWayOnline(t *testing.T) {
	dir := t.TempDir()
	m := testModel(t, dir, nil)
	m.cli = liveServer(t)
	m.width, m.height, m.listH = 120, 30, 20

	var first statusMsg
	for _, msg := range msgsOf(m.Init()) {
		switch msg := msg.(type) {
		case gamesMsg:
			t.Fatal("Init fetched the game list alongside the first status")
		case statusMsg:
			first = msg
		}
	}
	mm, _ := m.Update(first)
	m = mm.(*model)
	if !m.haveStatus || !m.online {
		t.Fatal("Init didn't establish a status")
	}
	if m.st.CPUTemp != 58 {
		t.Errorf("CPUTemp = %d, want 58", m.st.CPUTemp)
	}
	// a game was running, so the poll opened a session
	if !m.sesOn || m.sesSecs != 5025 {
		t.Errorf("session = %v/%ds, want an open session at 5025s", m.sesOn, m.sesSecs)
	}

	// the first good status is what schedules the list...
	m.online = false
	_, cmd := m.Update(statusMsg{st: m.st})
	var got gamesMsg
	found := false
	for _, msg := range msgsOf(cmd) {
		if g, ok := msg.(gamesMsg); ok {
			got, found = g, true
		}
	}
	if !found {
		t.Fatal("coming online didn't fetch the game list")
	}
	mm, _ = m.Update(got)
	m = mm.(*model)
	if len(m.games) != 22 {
		t.Errorf("loaded %d games, want 22", len(m.games))
	}
	if len(m.consoles) != 2 {
		t.Errorf("consoles = %v, want PSX and PS3", m.consoles)
	}

	// ...and a status while already online doesn't re-fetch it every 15s
	_, cmd = m.Update(statusMsg{st: m.st})
	for _, msg := range msgsOf(cmd) {
		if _, ok := msg.(gamesMsg); ok {
			t.Error("a routine poll re-fetched the game list")
		}
	}
}

// A console that's off at launch used to be fatal (discovery refused to start)
// and, once online, listless (the one games fetch had failed and nothing
// retried it). Now an auto-discovered address that doesn't answer gets one
// background sweep per outage, the dashboard stays up, and recovery loads the
// list. A --host the user typed is never second-guessed by a sweep.
func TestOfflineCachedHostSweepsOncePerOutage(t *testing.T) {
	loopbackOnly(t)
	defer func(p string) { sweepPort = p }(sweepPort)
	sweepPort = "1" // the sweep finds nothing, fast

	m := liveModel(t, 120)
	m.cli = NewClient("127.0.0.1:1")
	m.hostCache = filepath.Join(t.TempDir(), "host")

	_, cmd := m.Update(statusMsg{err: errors.New("dial: refused")})
	if cmd == nil || !m.scanning {
		t.Fatal("a dead auto-discovered address didn't start a sweep")
	}
	if m.scanNets == "" {
		t.Error("the header has no subnets to name while scanning")
	}
	if !strings.Contains(m.header(), "scanning") {
		t.Error("header doesn't say a sweep is running")
	}
	// the sweep comes back empty: stay on the cached address, keep polling
	m = exec(t, m, cmd)
	if m.scanning {
		t.Error("still scanning after the sweep answered")
	}
	if m.host != "test" {
		t.Errorf("an empty sweep changed the host to %q", m.host)
	}
	// a second failure in the same outage is NOT another 254 dials
	if _, cmd := m.Update(statusMsg{err: errors.New("still dead")}); cmd != nil {
		t.Error("every failed poll swept the LAN again")
	}
	// r is the explicit "look again", so it re-arms the latch
	if _, cmd := m.handleKey(key("r")); cmd == nil || !m.scanning {
		t.Error("r while offline didn't re-sweep")
	}
	m.scanning = false // as the sweep's reply would leave it

	// recovery clears the latch: the NEXT outage sweeps once more
	mm, _ := m.Update(statusMsg{st: m.st})
	m = mm.(*model)
	if m.swept {
		t.Error("coming online didn't re-arm the one-sweep latch")
	}

	// a typed --host (no cache path) never sweeps
	m.hostCache = ""
	if _, cmd := m.Update(statusMsg{err: errors.New("dial: refused")}); cmd != nil {
		t.Error("a --host address was second-guessed by a sweep")
	}
}

// A sweep that finds the console somewhere else switches the client, writes
// the cache back, and polls the new address straight away.
func TestSweepAdoptsTheNewAddress(t *testing.T) {
	m := liveModel(t, 120)
	m.online = false
	m.hostCache = filepath.Join(t.TempDir(), "sub", "host")
	host := strings.TrimPrefix(liveServer(t).base, "http://")

	mm, cmd := m.Update(discoverMsg{host: host})
	m = mm.(*model)
	if m.host != host || m.cli.base != "http://"+host {
		t.Errorf("host = %q / client %q, want %q", m.host, m.cli.base, host)
	}
	if b, err := os.ReadFile(m.hostCache); err != nil || strings.TrimSpace(string(b)) != host {
		t.Errorf("new address not cached: %q, %v", b, err)
	}
	if !strings.Contains(m.flash, host) {
		t.Errorf("flash = %q, want the new address named", m.flash)
	}
	if cmd == nil || !m.inFlight {
		t.Fatal("the new address wasn't polled immediately")
	}
	// and that poll, against the new client, answers
	m = exec(t, m, m.fetchStatus())
	if !m.online {
		t.Error("the adopted address doesn't answer")
	}
}

// A failed game-list fetch must not wipe the library that's on screen.
func TestGamesFetchFailureFlashes(t *testing.T) {
	m := liveModel(t, 120)
	before := len(m.games)
	mm, cmd := m.Update(gamesMsg{err: errors.New("boom")})
	m = mm.(*model)
	if m.flash == "" || cmd == nil {
		t.Error("a failed game list said nothing")
	}
	if len(m.games) != before {
		t.Errorf("library went from %d to %d games on a failed reload", before, len(m.games))
	}
}

// A games reload must keep the cursor on the game it was on — indexes don't
// survive the rebuild, so the selection is re-found by identity. Without this,
// every r (which now reloads the list too) dumped you back to row 1.
func TestGamesReloadKeepsTheCursorOnItsGame(t *testing.T) {
	m := liveModel(t, 120)
	m.moveCursor(1) // sit on "Alpha", not row 1
	sel, ok := m.selectedGame()
	if !ok || sel.Title != "Alpha" {
		t.Fatalf("fixture cursor on %q, want Alpha", sel.Title)
	}

	// the reload surfaces a new ISO that sorts ABOVE the selection
	reloaded := []Game{
		{Title: "Aardvark Quest", ID: "MOCK00009", Category: "hdd0/PS3ISO", Path: "/dev_hdd0/PS3ISO/AQ.iso"},
		m.games[1], // Alpha
		m.games[0], // Sample Game™ 2
	}
	mm, _ := m.Update(gamesMsg{games: reloaded})
	m = mm.(*model)

	if got, _ := m.selectedGame(); got.Title != "Alpha" {
		t.Errorf("cursor landed on %q after the reload, want Alpha", got.Title)
	}

	// a game that vanished can't be re-found; the cursor falls back to the top
	mm, _ = m.Update(gamesMsg{games: reloaded[:1]})
	m = mm.(*model)
	if m.cursor != 0 {
		t.Errorf("cursor = %d after its game vanished, want 0", m.cursor)
	}
}

func TestFetchGamesAndActionErrorsSurface(t *testing.T) {
	m := liveModel(t, 120)
	m.cli = NewClient("127.0.0.1:1") // nothing listening

	m = exec(t, m, m.fetchGames())
	if !strings.Contains(m.flash, "games list:") {
		t.Errorf("game fetch error not reported: %q", m.flash)
	}

	m.flash = ""
	m = exec(t, m, m.fetchStatus())
	if m.online {
		t.Error("a failed status left the console marked online")
	}
	if m.lastErr == nil {
		t.Error("failed status recorded no error")
	}

	// an action that fails names itself in the flash
	m.actBusy = false
	m = exec(t, m, m.act("eject", func(ctx context.Context) error { return errors.New("refused") }))
	if !strings.Contains(m.flash, "eject") || !strings.Contains(m.flash, "refused") {
		t.Errorf("action error = %q", m.flash)
	}
	if m.actBusy {
		t.Error("a failed action never released the gate")
	}
}

// The tick reschedules itself forever — a poll loop that stops after one
// failure would leave the dashboard frozen with no indication.
func TestTickReschedulesAndBacksOffOffline(t *testing.T) {
	m := liveModel(t, 120)
	m.online = false
	_, cmd := m.Update(tickMsg{})
	if cmd == nil {
		t.Fatal("tick produced no follow-up")
	}
	// the offline retry floor is longer than a fast --interval would ask for
	m.interval = time.Second
	m.online = false
	if next := m.interval; next >= offlineRetry {
		t.Skip("interval already above the offline floor")
	}
	m.Update(tickMsg{})
}

func TestCoverLifecycle(t *testing.T) {
	dir := t.TempDir()
	m := testModel(t, dir, nil)
	m.cli = liveServer(t)
	m.width, m.height, m.listH = 120, 30, 20
	m.artOn = true
	m = exec(t, m, m.fetchGames())

	if !m.artShown() {
		t.Fatal("art panel not shown at 120 cols")
	}
	g, ok := m.selectedGame()
	if !ok {
		t.Fatal("no game selected")
	}

	// before the cover arrives the panel renders a placeholder, not nothing
	if panel := m.artPanel(); !strings.Contains(panel, "· · ·") {
		t.Errorf("no placeholder while the cover loads:\n%s", panel)
	}

	// an uncached cover is a console request, so it waits out the debounce:
	// ensureCover schedules a tick rather than fetching on the spot
	if cmd := m.ensureCover(); cmd == nil {
		t.Fatal("selecting a game with no cached cover scheduled nothing")
	} else if m.coverBusy != "" {
		t.Error("an uncached cover was fetched before the cursor settled")
	}
	gen := m.coverGen

	// the tick that lands is the one that fetches
	mm, cmd := m.Update(coverTickMsg{gen})
	m = mm.(*model)
	if cmd == nil {
		t.Fatal("the settle tick didn't start the load")
	}
	if m.coverBusy != g.IconPath {
		t.Errorf("in-flight cover = %q, want %q", m.coverBusy, g.IconPath)
	}
	// a second request while the first is in flight is deduplicated
	if again := m.ensureCover(); again != nil {
		t.Error("a duplicate cover fetch went out")
	}
	// the loaded PNG comes back as a message; what Update returns for it is
	// the transmission followed by the "shown" marker — the load stays busy
	// until the marker lands, so the bytes are always ahead of the frame
	// that references them
	mm, seq := m.Update(cmd())
	m = mm.(*model)
	if m.coverBusy != g.IconPath {
		t.Error("load released before the bytes reached the terminal")
	}
	out := raw(seq)
	if !strings.Contains(out, "\x1b_Gf=100") {
		t.Errorf("cover never transmitted to the terminal: %q", out)
	}
	m = exec(t, m, seq)
	if ref := m.covers[g.IconPath]; !ref.shown || ref.id == 0 {
		t.Errorf("transmitted cover not recorded: %+v", ref)
	} else if ref.cols < 1 || ref.rows < 1 {
		t.Errorf("placement unshaped: %+v", ref)
	}
	if m.coverBusy != "" {
		t.Error("cover still marked busy after it arrived")
	}
	// now the panel references the image instead of the placeholder
	if panel := m.artPanel(); !strings.Contains(panel, string(placeholder)) {
		t.Error("art panel didn't switch to the transmitted image")
	}
	// and an already-transmitted cover is never fetched again
	if m.ensureCover() != nil {
		t.Error("a transmitted cover was fetched a second time")
	}

	// the fetch wrote the cache, so a fresh model finds it on disk and shows it
	// without waiting: no tick, the load starts on the spot
	m.covers = map[string]coverRef{}
	if cmd := m.ensureCover(); cmd == nil || m.coverBusy != g.IconPath {
		t.Error("a cached cover waited out the debounce")
	} else {
		m = exec(t, m, cmd)
		m.coverBusy = "" // the shown marker would clear it
	}

	// a failed download clears the busy flag and is remembered, so the row
	// doesn't cost the console a request on every visit. A fresh cache dir,
	// or the entry just written would serve the request and succeed.
	m.cli = NewClient("127.0.0.1:1")
	m.cacheDir = t.TempDir()
	m.covers = map[string]coverRef{}
	m = exec(t, m, m.startCover(g.IconPath))
	if m.coverBusy != "" {
		t.Error("a failed cover stayed busy forever")
	}
	if !m.coverFailed[g.IconPath] {
		t.Error("a failed cover wasn't remembered")
	}
	if m.ensureCover() != nil {
		t.Error("a failed cover was retried on the next selection")
	}
	if panel := m.artPanel(); !strings.Contains(panel, "no cover") {
		t.Errorf("panel doesn't say the cover is missing:\n%s", panel)
	}
	// a list reload is the moment to try again
	mm, _ = m.Update(gamesMsg{games: m.games})
	m = mm.(*model)
	if m.coverFailed[g.IconPath] {
		t.Error("a games reload didn't clear the failed-cover memory")
	}
}

// Scrolling through uncached rows must fetch where the cursor stops, not
// everywhere it went: every move bumps the debounce generation, so the ticks
// of rows it left are dead on arrival, and a load already in flight re-checks
// the selection when it lands instead of letting another one start beside it.
func TestCoverFetchesWhereTheCursorStops(t *testing.T) {
	m := liveModel(t, 120)
	m.cli = liveServer(t)
	m.artOn = true
	m.games[0].IconPath = "/dev_hdd0/tmp/wmtmp/One.PNG"
	m.games[1].IconPath = "/dev_hdd0/tmp/wmtmp/Two.PNG"

	m.ensureCover() // row 1
	stale := m.coverGen
	m.moveCursor(1)
	if m.ensureCover() == nil {
		t.Fatal("moving to an uncached row scheduled nothing")
	}
	// the tick for the row the cursor left does nothing
	if _, cmd := m.Update(coverTickMsg{stale}); cmd != nil || m.coverBusy != "" {
		t.Error("a stale debounce tick fetched the row the cursor had left")
	}
	// the current one fetches the row it's on
	mm, cmd := m.Update(coverTickMsg{m.coverGen})
	m = mm.(*model)
	if m.coverBusy != "/dev_hdd0/tmp/wmtmp/Two.PNG" {
		t.Errorf("fetching %q, want the row the cursor stopped on", m.coverBusy)
	}
	// while it's out, moving back schedules a tick but that tick must not
	// start a second console request beside the first
	m.moveCursor(-1)
	m.ensureCover()
	if _, c := m.Update(coverTickMsg{m.coverGen}); c != nil {
		t.Error("two cover loads in flight at once")
	}
	// when the first has been shown, the selection is re-checked and row 1
	// follows
	mm, seq := m.Update(cmd())
	m = mm.(*model)
	m = exec(t, m, seq)
	if m.coverBusy != "" {
		t.Error("shown marker didn't release the load")
	}
	if next := m.ensureCover(); next == nil {
		t.Error("a landed cover didn't leave the next selection fetchable")
	}
}

// Art is off below the width threshold and when disabled, and neither may cost
// a request or an image id.
func TestCoverSkippedWhenArtIsHidden(t *testing.T) {
	m := liveModel(t, 120)
	m.artOn = false
	if m.ensureCover() != nil {
		t.Error("--no-art still fetched a cover")
	}
	m.artOn = true
	m.width = 60 // panel hidden
	if m.ensureCover() != nil {
		t.Error("a hidden panel still fetched a cover")
	}
	if m.artPanel() == "" && m.artShown() {
		t.Error("inconsistent art state")
	}
	// no game selected → nothing to fetch
	m.width = 120
	m.order = nil
	if m.ensureCover() != nil {
		t.Error("fetched a cover with no game selected")
	}
	if got := m.artPanel(); got != "" {
		t.Errorf("art panel rendered with no selection: %q", got)
	}
}

// 255 is a hard ceiling: the image id is carried in a 256-colour foreground, so
// beyond it art turns itself off rather than drawing the wrong covers.
func TestCoverIDsExhaust(t *testing.T) {
	m := liveModel(t, 120)
	m.artOn = true
	m.games[0].IconPath = "/dev_hdd0/tmp/wmtmp/SampleGame2.PNG"
	m.nextID = 254 // the next allocation is 255, the last encodable one
	if m.startCover(m.games[0].IconPath) == nil {
		t.Fatal("id 255 should still be usable")
	}
	m.covers, m.coverBusy = map[string]coverRef{}, ""
	if m.startCover(m.games[0].IconPath) != nil {
		t.Error("allocated an unencodable image id")
	}
	if m.artOn {
		t.Error("art should switch itself off once ids run out")
	}
}

// --- list mechanics ---

func TestTabSwitchingAndCursorMovement(t *testing.T) {
	dir := t.TempDir()
	m := testModel(t, dir, nil)
	m.cli = liveServer(t)
	m.width, m.height, m.listH = 120, 30, 8
	m = exec(t, m, m.fetchGames())

	start := m.activeConsole()
	m.handleKey(key("tab"))
	if m.activeConsole() == start {
		t.Error("tab didn't change console")
	}
	m.handleKey(key("shift+tab"))
	if m.activeConsole() != start {
		t.Error("shift+tab didn't come back")
	}
	// wrapping is modular in both directions
	for i := 0; i < len(m.consoles)+1; i++ {
		m.handleKey(key("tab"))
	}

	// a single-console library has nothing to switch to
	solo := liveModel(t, 120)
	solo.consoles = []string{"PS3"}
	if cmd := solo.switchTab(1); cmd != nil || solo.activeTab != 0 {
		t.Error("switched tabs in a single-console library")
	}

	// paging and jumps stay inside the list
	m.handleKey(key("end"))
	if m.cursor != len(m.order)-1 {
		t.Errorf("end left the cursor at %d of %d", m.cursor, len(m.order))
	}
	m.handleKey(key("pgup"))
	m.handleKey(key("pgdown"))
	m.handleKey(key("home"))
	if m.cursor != 0 {
		t.Errorf("home left the cursor at %d", m.cursor)
	}
	m.handleKey(key("up")) // already at the top
	if m.cursor != 0 {
		t.Errorf("cursor went negative: %d", m.cursor)
	}
	for i := 0; i < len(m.order)+5; i++ {
		m.handleKey(key("down"))
	}
	if m.cursor != len(m.order)-1 {
		t.Errorf("cursor ran past the end: %d of %d", m.cursor, len(m.order))
	}

	// ensureVisible with no list area is a no-op rather than a panic
	m.listH = 0
	m.ensureVisible()
}

func TestSelectedGameOutOfRange(t *testing.T) {
	m := liveModel(t, 120)
	m.cursor = -1
	if _, ok := m.selectedGame(); ok {
		t.Error("a negative cursor selected a game")
	}
	m.cursor = len(m.order) + 10
	if _, ok := m.selectedGame(); ok {
		t.Error("a cursor past the end selected a game")
	}
}

func TestFilterAndSortKeys(t *testing.T) {
	dir := t.TempDir()
	m := testModel(t, dir, nil)
	m.cli = liveServer(t)
	m.width, m.height, m.listH = 120, 30, 20
	m = exec(t, m, m.fetchGames())
	all := len(m.order)

	m.handleKey(key("/"))
	if !m.filterTyping {
		t.Fatal("/ didn't open the filter")
	}
	for _, r := range "storm" {
		m.handleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if len(m.order) == 0 || len(m.order) >= all {
		t.Errorf("filter matched %d of %d rows", len(m.order), all)
	}
	// arrows and tabs still work while typing
	m.handleKey(key("down"))
	m.handleKey(key("up"))
	m.handleKey(key("tab"))
	m.handleKey(key("shift+tab"))
	m.handleKey(key("enter")) // keep the filter, stop typing
	if m.filterTyping {
		t.Error("enter didn't leave the filter input")
	}
	if m.filterQ == "" {
		t.Error("enter discarded the filter")
	}
	m.handleKey(key("esc")) // esc with a filter set clears it
	if m.filterQ != "" || len(m.order) != all {
		t.Errorf("esc left %d of %d rows and query %q", len(m.order), all, m.filterQ)
	}
	m.handleKey(key("esc")) // esc with nothing to clear is a no-op

	// esc while typing abandons the filter outright
	m.handleKey(key("/"))
	m.handleKey(key("z"))
	m.handleKey(key("esc"))
	if m.filterTyping || m.filterQ != "" {
		t.Error("esc didn't abandon the filter")
	}

	// sort toggle keeps the cursor on the same game
	m.hist.fold(sessionRec{ID: m.games[m.order[2]].ID, Title: m.games[m.order[2]].Title,
		End: time.Now(), Secs: 3600})
	m.cursor = 2
	want := m.order[2]
	m.handleKey(key("s"))
	if !m.sortRecent {
		t.Error("s didn't switch to recently-played")
	}
	if m.order[m.cursor] != want {
		t.Error("the sort toggle lost the cursor's game")
	}
	m.handleKey(key("s"))
}

// The popup is the one action with free text, so it has its own modal.
func TestPopupModal(t *testing.T) {
	m := liveModel(t, 120)
	m.handleKey(key("m"))
	if !m.popupOn {
		t.Fatal("m didn't open the popup input")
	}
	m.handleKey(key("esc"))
	if m.popupOn {
		t.Error("esc didn't close the popup input")
	}

	m.handleKey(key("m"))
	m.handleKey(key("enter")) // empty message sends nothing
	if m.popupOn {
		t.Error("popup stayed open on an empty send")
	}
	if m.actBusy {
		t.Error("an empty popup message was sent anyway")
	}

	m.handleKey(key("m"))
	for _, r := range "hi" {
		m.handleKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	_, cmd := m.handleKey(key("enter"))
	if cmd == nil || !m.actBusy {
		t.Error("a typed popup message never went out")
	}
}

func TestPowerKeysAlwaysConfirm(t *testing.T) {
	for _, k := range []string{"S", "R"} {
		m := liveModel(t, 120)
		m.st.InGame = false // even with nothing running
		if _, cmd := m.handleKey(key(k)); cmd != nil {
			t.Errorf("%q fired without confirmation", k)
		}
		if m.confirm == nil {
			t.Fatalf("%q raised no prompt", k)
		}
		// declining is the default: any key that isn't y/enter cancels
		m.handleKey(key("n"))
		if m.confirm != nil || m.actBusy {
			t.Errorf("%q went ahead after being declined", k)
		}
	}
	// confirming with enter works as well as y
	m := liveModel(t, 120)
	m.handleKey(key("S"))
	if _, cmd := m.handleKey(key("enter")); cmd == nil {
		t.Error("enter didn't confirm the prompt")
	}
}

func TestQuitFlushesAndCleansUp(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		dir := t.TempDir()
		m := testModel(t, dir, nil)
		m.trackSession(Status{InGame: true, GameID: "MOCK1", GameTitle: "Game", PlaySecs: 600})

		// with no images in the terminal there's nothing to free: plain quit
		_, cmd := m.handleKey(key(k))
		msgs := leaves(cmd)
		if len(msgs) != 1 {
			t.Fatalf("%q produced %d messages, want just the quit", k, len(msgs))
		}
		if _, ok := msgs[0].(tea.QuitMsg); !ok {
			t.Errorf("%q didn't quit: %T", k, msgs[0])
		}
		if s := mustLoad(t, dir).stats["MOCK1"]; s.Secs != 600 {
			t.Errorf("%q didn't write the open session (got %ds)", k, s.Secs)
		}

		// with covers transmitted, the delete-all goes out first, then the
		// quit — in that order, so the store is freed before the program ends
		m.covers["/x.PNG"] = coverRef{id: 3, cols: 24, rows: 7, shown: true}
		_, cmd = m.handleKey(key(k))
		msgs = leaves(cmd)
		if len(msgs) != 2 {
			t.Fatalf("%q with images produced %d messages, want delete then quit", k, len(msgs))
		}
		if r, ok := msgs[0].(tea.RawMsg); !ok || r.Msg != deleteAllImages {
			t.Errorf("%q didn't free the terminal's images first: %#v", k, msgs[0])
		}
		if _, ok := msgs[1].(tea.QuitMsg); !ok {
			t.Errorf("%q didn't end with a quit: %T", k, msgs[1])
		}
	}
}

func TestThermalScreenToggles(t *testing.T) {
	m := liveModel(t, 120)
	for _, k := range []string{"esc", "q", "t"} {
		m.thermalOn = false
		m.handleKey(key("t"))
		if !m.thermalOn {
			t.Fatal("t didn't open the thermal screen")
		}
		m.handleKey(key(k))
		if m.thermalOn {
			t.Errorf("%q didn't close the thermal screen", k)
		}
	}
	// r refreshes from in there, and is ignored while busy
	m.handleKey(key("t"))
	if _, cmd := m.handleKey(key("r")); cmd == nil {
		t.Error("r didn't refresh from the thermal screen")
	}
	m.inFlight = true
	if _, cmd := m.handleKey(key("r")); cmd != nil {
		t.Error("r polled on top of a request already in flight")
	}
	// an unbound key is simply ignored
	m.handleKey(key("x"))
}

func TestWindowResizeAndUnknownKeys(t *testing.T) {
	m := liveModel(t, 120)
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	m = mm.(*model)
	if m.width != 200 || m.listH != 60-headerH-footerH {
		t.Errorf("resize gave %dx%d, listH %d", m.width, m.height, m.listH)
	}
	// a tiny terminal still leaves a usable list area
	mm, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 5})
	if got := mm.(*model).listH; got < 3 {
		t.Errorf("listH = %d in a 5-row terminal, want at least 3", got)
	}
	// messages the model doesn't handle pass through untouched
	if _, cmd := m.Update(struct{}{}); cmd != nil {
		t.Error("an unknown message produced a command")
	}
}

// The flash generation counter exists so a stale clear can't wipe a newer
// message four seconds after the fact.
func TestFlashClearIsGenerational(t *testing.T) {
	m := liveModel(t, 120)
	m.flash = "first"
	m.clearFlashLater()
	stale := m.flashGen
	m.flash = "second"
	m.clearFlashLater()

	mm, _ := m.Update(clearFlashMsg{gen: stale})
	if got := mm.(*model).flash; got != "second" {
		t.Errorf("a stale clear wiped the newer flash (flash = %q)", got)
	}
	mm, _ = m.Update(clearFlashMsg{gen: m.flashGen})
	if got := mm.(*model).flash; got != "" {
		t.Errorf("the current clear didn't fire (flash = %q)", got)
	}
}

// --- small helpers ---

func TestValueFormattersHandleUnknown(t *testing.T) {
	m := liveModel(t, 120)
	for name, got := range map[string]string{
		"fan":  fanVal(unknown),
		"hdd":  hddVal(unknown),
		"mem":  memVal(unknown),
		"temp": tempVal(unknown, m.alarm),
	} {
		if !strings.Contains(got, "—") {
			t.Errorf("%s with no reading rendered %q, want an em dash", name, got)
		}
	}
	// 0 is a real reading webMAN reports, and must not render as absent
	if got := fanVal(0); strings.Contains(got, "—") {
		t.Errorf("fanVal(0) = %q, want a real zero", got)
	}
	// the alarm threshold, not just the fixed band, drives the colour
	m.alarm = 60
	if tempVal(61, m.alarm) == tempVal(30, m.alarm) {
		t.Error("a temperature over --alarm renders the same as a cool one")
	}
	// low disk and high memory pressure get their own treatment
	hddVal(2.0)
	memVal(200)
	memVal(9000)
}

func TestShortFW(t *testing.T) {
	if got := shortFW("4.93 CEX PS3HEN 3.5.0"); !strings.Contains(got, "4.93") {
		t.Errorf("shortFW dropped the version: %q", got)
	}
	if got := shortFW(""); got != "" {
		t.Errorf("shortFW(empty) = %q", got)
	}
	if got := shortFW("4.93"); got != "4.93" {
		t.Errorf("shortFW(short) = %q", got)
	}
}

func TestSamePath(t *testing.T) {
	if !samePath("/a/b c.iso", "/a/b%20c.iso") {
		t.Error("escaped and unescaped forms of one path didn't match")
	}
	if samePath("/a.iso", "/b.iso") {
		t.Error("different paths matched")
	}
	// an invalid escape falls back to a literal comparison instead of erroring
	if !samePath("/a%zz", "/a%zz") {
		t.Error("identical unparseable paths didn't match")
	}
	if samePath("/a%zz", "/b%zz") {
		t.Error("different unparseable paths matched")
	}
}

func TestTruncPadAndClamp(t *testing.T) {
	if got := truncPad("abc", 0); got != "" {
		t.Errorf("truncPad to 0 = %q", got)
	}
	if got := truncPad("abcdef", 4); len([]rune(got)) != 4 {
		t.Errorf("truncPad(6 chars, 4) = %q", got)
	}
	if got := truncPad("ab", 5); got != "ab   " {
		t.Errorf("truncPad padded to %q", got)
	}
	// display width, not rune count — a CJK rune is two columns
	if got := truncPad("日本", 4); got != "日本" {
		t.Errorf("truncPad(CJK) = %q", got)
	}

	if got := clamp(5, 1, 3); got != 3 {
		t.Errorf("clamp above = %d", got)
	}
	if got := clamp(0, 1, 3); got != 1 {
		t.Errorf("clamp below = %d", got)
	}
	if got := clamp(2, 1, 3); got != 2 {
		t.Errorf("clamp inside = %d", got)
	}
	// an inverted range collapses to the low bound rather than panicking
	if got := clamp(5, 3, 1); got != 3 {
		t.Errorf("clamp with hi<lo = %d, want 3", got)
	}
}

func TestModeSuffix(t *testing.T) {
	if got := modeSuffix(""); got != "" {
		t.Errorf("modeSuffix(empty) = %q", got)
	}
	if got := modeSuffix("manual"); got != ", manual" {
		t.Errorf("modeSuffix = %q", got)
	}
}

func TestFanQueueLabel(t *testing.T) {
	for _, c := range []struct {
		q    []string
		want string
	}{
		{[]string{fanUp, fanUp}, "⋯+2"},
		{[]string{fanDown}, "⋯-1"},
		{[]string{fanMode}, "⋯·mode"},
		{[]string{fanUp, fanMode}, "⋯+1·mode"},
		{[]string{fanUp, fanDown}, "⋯"}, // net zero either side of a mode change
	} {
		if got := fanQueueLabel(c.q); got != c.want {
			t.Errorf("fanQueueLabel(%v) = %q, want %q", c.q, got, c.want)
		}
	}
}

func TestPlural(t *testing.T) {
	if got := plural(1, "session"); got != "session" {
		t.Errorf("plural(1) = %q", got)
	}
	if got := plural(0, "session"); got != "sessions" {
		t.Errorf("plural(0) = %q", got)
	}
	if got := plural(2, "session"); got != "sessions" {
		t.Errorf("plural(2) = %q", got)
	}
}

// The data dir is deliberately NOT the cache dir: cache cleaners eat
// ~/Library/Caches, and this file is the only copy of how long you've played
// anything.
func TestDataDir(t *testing.T) {
	got, err := dataDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "ps3top") {
		t.Errorf("dataDir = %q", got)
	}
	if strings.Contains(strings.ToLower(got), "cache") {
		t.Errorf("history landed in a cache directory: %q", got)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("dataDir = %q, want an absolute path", got)
	}
}

func TestRecStartAndAbsDur(t *testing.T) {
	now := time.Now()
	// stored value wins
	if got := recStart(sessionRec{Started: now, End: now.Add(time.Hour), Secs: 60}); !got.Equal(now) {
		t.Error("recStart ignored the stored start")
	}
	// legacy record: derived from End − Secs
	want := now.Add(-time.Hour)
	if got := recStart(sessionRec{End: now, Secs: 3600}); !got.Equal(want) {
		t.Errorf("recStart derived %v, want %v", got, want)
	}
	// nothing to derive from
	if got := recStart(sessionRec{Secs: 60}); !got.IsZero() {
		t.Errorf("recStart with no End = %v, want zero", got)
	}

	if got := absDur(-time.Minute); got != time.Minute {
		t.Errorf("absDur(-1m) = %v", got)
	}
	if got := absDur(time.Minute); got != time.Minute {
		t.Errorf("absDur(1m) = %v", got)
	}
}

// A record with no start time at all can't be merged with anything, and must
// still be counted rather than dropped.
func TestFoldWithoutTimestamps(t *testing.T) {
	h := mustLoad(t, t.TempDir())
	h.fold(sessionRec{ID: "MOCK1", Title: "Game", Secs: 600})
	h.fold(sessionRec{ID: "MOCK1", Title: "Game", Secs: 600})
	if s := h.stats["MOCK1"]; s.Secs != 1200 || s.Sessions != 2 {
		t.Errorf("timestampless records folded to %ds over %d sessions, want 1200/2", s.Secs, s.Sessions)
	}
}

func TestWriteCacheFailurePaths(t *testing.T) {
	// an unwritable directory is a silent no-op, not a crash
	writeCache(filepath.Join(t.TempDir(), "nope", "x.png"), []byte("data"))

	// a successful write lands and is readable
	dir := t.TempDir()
	path := filepath.Join(dir, "x.png")
	writeCache(path, tinyPNG(t))
	if b, err := os.ReadFile(path); err != nil {
		t.Errorf("writeCache didn't produce a file: %v", err)
	} else if _, err := decodeCover(b); err != nil {
		t.Errorf("writeCache didn't produce a usable file: %v", err)
	}
}

func TestLoadCoverSurfacesFetchErrors(t *testing.T) {
	if _, err := loadCover(NewClient("127.0.0.1:1"), t.TempDir(), "/icon.png"); err == nil {
		t.Error("a dead console reported a cover")
	}
}

func TestDecodeCoverUndecodable(t *testing.T) {
	// correct magic, truncated body — the signature passes, the decode must not
	if _, err := decodeCover([]byte("\x89PNG\r\n\x1a\n")); err == nil {
		t.Error("a header with no image data was accepted")
	}
}
