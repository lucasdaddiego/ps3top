package ps3top

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
)

// The fixtures in testdata/ are synthetic: byte-faithful to real webMAN
// (sMAN skin) output in structure, but every title, ID, and counter is
// invented. The fake title IDs keep the real 9-char shape ([A-Z0-9]{9})
// and use two 4-char "pools" (MOCK/DEMO) so the franchise-ID sort hint has
// both a same-pool and a cross-pool case to chew on.

// A Lite build renders the firmware line differently: no NOR/NAND prefix and
// "FW:" instead of "Firmware:". Everything else in cpursx_lite.html is
// inherited from cpursx_xmb.html but the edition tag on the version line, so
// a failure here is one of those two lines and nothing else. Regression: reFW
// silently returned "" against a Lite console, which only showed up as a
// missing corner readout.
func TestParseStatusLiteEdition(t *testing.T) {
	b, err := os.ReadFile("testdata/cpursx_lite.html")
	if err != nil {
		t.Fatal(err)
	}
	s := parseStatus(string(b))

	if s.Firmware != "4.93 CEX PS3HEN 3.5.0" {
		t.Errorf("Firmware = %q", s.Firmware)
	}
	// the rest must survive the edition change untouched
	if s.CPUTemp == 0 || s.RSXTemp == 0 {
		t.Errorf("temps = %d/%d", s.CPUTemp, s.RSXTemp)
	}
	if s.WMVersion == "" {
		t.Error("WMVersion not parsed")
	}
	if s.Uptime == "" {
		t.Error("Uptime not parsed")
	}
}

// A Lite build says so on the status page, and prints no pid link: the
// running game shows there only as its title-ID link and the play clock.
func TestParseStatusEditionAndLiteInGame(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if !parseStatus(read("cpursx_lite.html")).Lite {
		t.Error("Lite page not read as Lite")
	}
	if parseStatus(read("cpursx_xmb.html")).Lite {
		t.Error("Full page read as Lite")
	}

	ingame := read("cpursx_ingame.html")
	const pid = `<a href="/gameplugin.ps3mapi?proc=0x1010200"><small>pid=01010200</small></a>`
	if !strings.Contains(ingame, pid) {
		t.Fatal("fixture lost its pid link")
	}
	if s := parseStatus(strings.Replace(ingame, pid, "", 1)); !s.InGame || s.GameID != "MOCK30982" {
		t.Errorf("no pid link: InGame=%v GameID=%q, want a running game", s.InGame, s.GameID)
	}
	if parseStatus(read("cpursx_xmb.html")).InGame {
		t.Error("XMB page read as in-game")
	}
}

func TestParseStatusInGame(t *testing.T) {
	b, err := os.ReadFile("testdata/cpursx_ingame.html")
	if err != nil {
		t.Fatal(err)
	}
	s := parseStatus(string(b))

	if !s.InGame {
		t.Error("InGame = false, want true")
	}
	if s.GameID != "MOCK30982" {
		t.Errorf("GameID = %q", s.GameID)
	}
	if s.GameTitle != "Sample Game™ 2" {
		t.Errorf("GameTitle = %q", s.GameTitle)
	}
	if s.CPUTemp != 58 || s.RSXTemp != 61 {
		t.Errorf("temps = %d/%d, want 58/61", s.CPUTemp, s.RSXTemp)
	}
	if s.FanMode != "SYSCON" {
		t.Errorf("FanMode = %q", s.FanMode)
	}
	if s.FanPct != 26 {
		t.Errorf("FanPct = %d", s.FanPct)
	}
	if s.MemFreeKB != 1152 {
		t.Errorf("MemFreeKB = %d", s.MemFreeKB)
	}
	if s.HDDFreeGB != 123.4 {
		t.Errorf("HDDFreeGB = %v", s.HDDFreeGB)
	}
	if s.PlayTime != "01:23:45" {
		t.Errorf("PlayTime = %q", s.PlayTime)
	}
	if s.Uptime != "01:24:02" {
		t.Errorf("Uptime = %q", s.Uptime)
	}
	if s.MountedISO != "/dev_hdd0/PS3ISO/SampleGame2.iso" {
		t.Errorf("MountedISO = %q", s.MountedISO)
	}
	if s.Firmware != "4.93 CEX PS3HEN 3.5.0" {
		t.Errorf("Firmware = %q", s.Firmware)
	}
	if s.WMVersion != "1.47.48q" {
		t.Errorf("WMVersion = %q", s.WMVersion)
	}
	if s.LifeDays == 0 {
		t.Error("lifetime counters not parsed")
	}
	if s.GameVer != "01.15" {
		t.Errorf("GameVer = %q", s.GameVer)
	}
	if s.LifeDays != 100 || s.Boots != 1234 || s.HardOffs != 34 {
		t.Errorf("lifetime bits = %dd/%d boots/%d hard-offs, want 100/1234/34", s.LifeDays, s.Boots, s.HardOffs)
	}
}

func TestParseGames(t *testing.T) {
	b, err := os.ReadFile("testdata/mygames.xml")
	if err != nil {
		t.Fatal(err)
	}
	games := parseGames(string(b))

	if len(games) != 22 {
		t.Fatalf("len(games) = %d, want 22", len(games))
	}

	var storm *Game
	psx := 0
	for i := range games {
		g := games[i]
		if g.ID == "MOCK98137" {
			storm = &games[i]
		}
		if g.Console() == "PSX" {
			psx++
		}
		if g.MountURL == "" || g.Title == "" {
			t.Errorf("incomplete entry: %+v", g)
		}
		if !(g.Console() == "PSX") && g.ID == "" {
			t.Errorf("PS3 game without title ID: %+v", g)
		}
		if strings.ContainsAny(g.Title, "\n\r") {
			t.Errorf("title with newline: %q", g.Title)
		}
		if i > 0 {
			prev := games[i-1]
			if consoleRank(prev.Console()) > consoleRank(g.Console()) {
				t.Errorf("console order broken: %s %q after %s %q", g.Console(), g.Title, prev.Console(), prev.Title)
			}
			if prev.Console() == g.Console() && lessAt(games, i, i-1) {
				t.Errorf("not sorted: %q > %q", prev.Title, g.Title)
			}
		}
	}
	if psx != 1 {
		t.Errorf("PSX games = %d, want 1", psx)
	}

	if storm == nil {
		t.Fatal("Fixture Storm [MOCK98137] not found")
	}
	if storm.Title != "Fixture Storm™" {
		t.Errorf("storm.Title = %q", storm.Title)
	}
	if storm.Path != "/dev_hdd0/PS3ISO/FixtureStorm.iso" {
		t.Errorf("storm.Path = %q", storm.Path)
	}
	if storm.IconPath != "/dev_hdd0/tmp/wmtmp/FixtureStorm.PNG" {
		t.Errorf("storm.IconPath = %q", storm.IconPath)
	}
}

// In-game detection is the input to the mount/eject guard, so the XMB case is
// safety-relevant and was the one state with no fixture at all.
//
// Note this fixture is DERIVED from the in-game one (game block and PlayTime
// removed), not captured from a console — it proves the parser copes with an
// absent game block, not that this is byte-for-byte what webMAN sends.
func TestParseStatusOnXMB(t *testing.T) {
	b, err := os.ReadFile("testdata/cpursx_xmb.html")
	if err != nil {
		t.Fatal(err)
	}
	s := parseStatus(string(b))

	if s.InGame {
		t.Error("InGame = true on a page with no game process")
	}
	if s.GameID != "" || s.GameTitle != "" || s.GameVer != "" {
		t.Errorf("game fields populated on XMB: %q / %q / %q", s.GameID, s.GameTitle, s.GameVer)
	}
	if s.PlayTime != "" || s.PlaySecs != 0 {
		t.Errorf("PlayTime = %q (%ds), want empty on XMB", s.PlayTime, s.PlaySecs)
	}
	// everything that isn't about the running game must still be there — an XMB
	// page is not a degraded page, and must not raise the markup-changed warning
	if n := s.missing(); n != 0 {
		t.Errorf("missing() = %d on a healthy XMB page, want 0", n)
	}
	if s.CPUTemp != 58 || s.RSXTemp != 61 || s.FanPct != 26 {
		t.Errorf("metrics = %d/%d/%d%%, want 58/61/26", s.CPUTemp, s.RSXTemp, s.FanPct)
	}
	if s.Uptime != "01:24:02" {
		t.Errorf("Uptime = %q", s.Uptime)
	}
	if s.Firmware != "4.93 CEX PS3HEN 3.5.0" {
		t.Errorf("Firmware = %q", s.Firmware)
	}
	// a disc can be mounted while sitting on the XMB — that's the "mounted, not
	// running" state the guard treats very differently from in-game
	if s.MountedISO != "/dev_hdd0/PS3ISO/SampleGame2.iso" {
		t.Errorf("MountedISO = %q", s.MountedISO)
	}
}

// An empty library is a legitimate answer, and has to be distinguishable from a
// parse that found nothing in a page it didn't understand.
func TestParseGamesEmptyLibrary(t *testing.T) {
	b, err := os.ReadFile("testdata/mygames_empty.xml")
	if err != nil {
		t.Fatal(err)
	}
	if games := parseGames(string(b)); len(games) != 0 {
		t.Errorf("empty library parsed to %d games", len(games))
	}
}

// A field the page doesn't carry must not read as a healthy 0. Before the
// unknown sentinel, renaming the temp block made the dashboard show a green
// "CPU 0°" — i.e. a broken parser looked like a perfectly cool console.
func TestMissingFieldsAreUnknownNotZero(t *testing.T) {
	b, err := os.ReadFile("testdata/cpursx_ingame.html")
	if err != nil {
		t.Fatal(err)
	}
	full := string(b)

	if s := parseStatus(full); s.missing() != 0 {
		t.Errorf("intact fixture: missing() = %d, want 0", s.missing())
	}

	// webMAN renames the temperature markers in some future build
	broken := strings.NewReplacer("CPU: ", "CPU_TEMP ", "RSX: ", "RSX_TEMP ").Replace(full)
	s := parseStatus(broken)
	if s.CPUTemp != unknown || s.RSXTemp != unknown {
		t.Errorf("temps = %d/%d, want unknown/unknown", s.CPUTemp, s.RSXTemp)
	}
	if s.missing() != 2 {
		t.Errorf("missing() = %d, want 2", s.missing())
	}
	// the rest of the page must still parse — one moved marker isn't a rewrite
	if s.FanPct != 26 || s.GameID != "MOCK30982" {
		t.Errorf("unrelated fields damaged: fan=%d id=%q", s.FanPct, s.GameID)
	}

	if s := parseStatus("<html>not webMAN at all</html>"); s.missing() != statusFields {
		t.Errorf("garbage page: missing() = %d, want %d", s.missing(), statusFields)
	}
}

// ?mode cycles three states and each renders the same slot differently, so the
// mode has to be read from which marker is present — including neither. All
// three shapes come from captures of a real console cycling through them
// (2026-08-12).
func TestFanModeCyclesThreeMarkerShapes(t *testing.T) {
	b, err := os.ReadFile("testdata/cpursx_ingame.html")
	if err != nil {
		t.Fatal(err)
	}
	syscon := string(b)
	const marker = `<small>[Fan control: SYSCON]</small>`
	if !strings.Contains(syscon, marker) {
		t.Fatal("fixture no longer carries the SYSCON marker")
	}

	s := parseStatus(syscon)
	if s.FanMode != "SYSCON" {
		t.Errorf("FanMode = %q, want SYSCON", s.FanMode)
	}
	if s.MaxTemp != unknown {
		t.Errorf("MaxTemp = %d, want unknown — SYSCON reports no target", s.MaxTemp)
	}

	// second state: the slot becomes a temperature target
	d := parseStatus(strings.Replace(syscon, marker, `(MAX: 86°C)`, 1))
	if d.FanMode != "dynamic" {
		t.Errorf("FanMode = %q, want dynamic", d.FanMode)
	}
	if d.MaxTemp != 86 {
		t.Errorf("MaxTemp = %d, want 86", d.MaxTemp)
	}

	// third state: the slot is empty, and that means manual — not unknown
	m := parseStatus(strings.Replace(syscon, marker, ``, 1))
	if m.FanMode != "manual" {
		t.Errorf("FanMode = %q, want manual", m.FanMode)
	}
	if m.MaxTemp != unknown {
		t.Errorf("MaxTemp = %d, want unknown — manual reports no target", m.MaxTemp)
	}

	// the temps sharing that line must survive every shape
	for name, got := range map[string]Status{"syscon": s, "dynamic": d, "manual": m} {
		if got.CPUTemp != 58 || got.RSXTemp != 61 {
			t.Errorf("%s: temps = %d/%d, want 58/61", name, got.CPUTemp, got.RSXTemp)
		}
		if got.FanPct != 26 {
			t.Errorf("%s: FanPct = %d, want 26", name, got.FanPct)
		}
	}
}

// The Fahrenheit copy of the line carries its own ceiling; matching it would
// report a 186°C fan trip point.
func TestFanMaxIgnoresFahrenheit(t *testing.T) {
	s := parseStatus(`CPU: 58°C (MAX: 86°C)<br>RSX: 59°C</a><hr><a href="x">CPU: 136°F (MAX: 186°F)`)
	if s.MaxTemp != 86 {
		t.Errorf("MaxTemp = %d, want 86", s.MaxTemp)
	}
}

// "no marker" only means manual when the page parsed at all — otherwise a
// broken page would confidently report a mode it never read.
func TestFanModeNeedsAParsedPage(t *testing.T) {
	s := parseStatus(`CPU: 58°C<br>RSX: 59°C<br>FAN SPEED:  27% (0x47)`)
	if s.FanMode != "manual" {
		t.Errorf("FanMode = %q, want manual", s.FanMode)
	}
	if s.FanPct != 27 {
		t.Errorf("FanPct = %d, want 27", s.FanPct)
	}

	if broken := parseStatus(`<html>not webMAN at all</html>`); broken.FanMode != "" {
		t.Errorf("unparseable page reported FanMode = %q, want empty", broken.FanMode)
	}
}

// 0 is a value webMAN can genuinely report, which is exactly why absent has to
// be its own sentinel rather than reusing the zero value.
func TestRealZeroIsNotUnknown(t *testing.T) {
	s := parseStatus(`FAN SPEED:  0% (0x00)<br>MEM: 0 KB<br>HDD:  0.0 GB free`)
	if s.FanPct != 0 {
		t.Errorf("FanPct = %d, want 0", s.FanPct)
	}
	if s.MemFreeKB != 0 {
		t.Errorf("MemFreeKB = %d, want 0", s.MemFreeKB)
	}
	if s.HDDFreeGB != 0 {
		t.Errorf("HDDFreeGB = %v, want 0", s.HDDFreeGB)
	}
}

func TestClockSecs(t *testing.T) {
	cases := map[string]int{
		"01:23:45":  5025,
		"00:00:07":  7,
		"218:07:14": 785234,
		"":          0,
		"garbage":   0,
	}
	for in, want := range cases {
		if got := clockSecs(in); got != want {
			t.Errorf("clockSecs(%q) = %d, want %d", in, got, want)
		}
	}
	// PlaySecs is what every history record's length comes from
	b, _ := os.ReadFile("testdata/cpursx_ingame.html")
	if s := parseStatus(string(b)); s.PlaySecs != 5025 {
		t.Errorf("PlaySecs = %d, want 5025", s.PlaySecs)
	}
}

// a version suffix needs a dot — a title's own trailing digit must survive
func TestGameVerRegex(t *testing.T) {
	if reGameVer.MatchString("MOCK PLANET 2") {
		t.Error("bare trailing digit misread as a version")
	}
	m := reGameVer.FindStringSubmatch("Sample Game™ 2  01.15")
	if m == nil || m[1] != "01.15" {
		t.Errorf("version suffix not extracted: %v", m)
	}
}

// series must come out in release order, not raw-alphabetical
func TestSeriesOrder(t *testing.T) {
	b, err := os.ReadFile("testdata/mygames.xml")
	if err != nil {
		t.Fatal(err)
	}
	games := parseGames(string(b))
	pos := func(id string) int {
		for i, g := range games {
			if g.ID == id {
				return i
			}
		}
		t.Fatalf("game %s not in fixture", id)
		return -1
	}

	// (named franchises, not "series" — that's the metric-history type)
	franchises := [][]string{
		{"MOCK00103", "MOCK00123", "MOCK00233"}, // Sample Quest: subtitle → 2 → 3
		{"MOCK30113", "MOCK00002", "MOCK31020"}, // Mock Planet: word edition → 2 → 3
		{"DEMO41022", "DEMO31532"},              // Demo Sniper: V2 → 3
		{"DEMO30796", "DEMO30919"},              // Demo Warrior → 2
		{"MOCK30386", "MOCK30982"},              // Sample Game → 2
		{"DEMO30682", "DEMO01807"},              // Demo City IV → V
	}
	for _, s := range franchises {
		for i := 1; i < len(s); i++ {
			if pos(s[i-1]) >= pos(s[i]) {
				t.Errorf("series order: %s should precede %s", s[i-1], s[i])
			}
		}
	}
}

// lessAt is the pairwise order parseGames applies, given the library the keys
// were computed over. The library matters: the release-order hint is qualified
// across the whole list, not decided pair by pair.
func lessAt(games []Game, i, j int) bool {
	keys := gameKeys(games)
	return keys[i].less(keys[j])
}

func TestFranchiseIDHint(t *testing.T) {
	// same colon-franchise + same prefix pool → registration (≈release) order,
	// regardless of subtitle alphabet
	saga := []Game{
		{Title: "Demo Saga: Chains", ID: "MOCK98110", Category: "hdd0/PS3ISO"},
		{Title: "Demo Saga: Ascension", ID: "MOCK98232", Category: "hdd0/PS3ISO"},
	}
	if !lessAt(saga, 0, 1) || lessAt(saga, 1, 0) {
		t.Error("same-pool franchise entries should sort by title ID")
	}
	// different pools (DEMO vs MOCK) → hint unusable, alphabetical fallback
	storm := []Game{
		{Title: "FIXTURE STORM®: APOCALYPSE", ID: "DEMO00484", Category: "hdd0/PS3ISO"},
		{Title: "FIXTURE STORM®: PACIFIC RIFT", ID: "MOCK98155", Category: "hdd0/PS3ISO"},
	}
	if !lessAt(storm, 0, 1) {
		t.Error("cross-pool franchise entries should fall back to title order")
	}
}

// sort.Slice requires a strict weak ordering, and the old comparator decided
// the ID hint per PAIR — "these two share a prefix pool, so compare by ID" —
// which isn't transitive. This exact triple cycles: A<B by ID, B<C by title,
// C<A by title. sort's output then depended on the order webMAN happened to
// list the library in.
func TestComparatorHasNoCycles(t *testing.T) {
	games := []Game{
		{Title: "F: Zulu", ID: "MOCK00001", Category: "hdd0/PS3ISO"},
		{Title: "F: Alpha", ID: "MOCK00002", Category: "hdd0/PS3ISO"},
		{Title: "F: Mike", ID: "DEMO00001", Category: "hdd0/PS3ISO"},
		// a colon-less title collating INTO the franchise: ":" isn't a token, so
		// this lands among the "F: …" entries and cycled them from outside even
		// once the franchise agreed with itself
		{Title: "F Kilo", ID: "MOCK00003", Category: "hdd0/PS3ISO"},
		{Title: "Unrelated", ID: "MOCK00004", Category: "hdd0/PS3ISO"},
	}
	keys := gameKeys(games)
	n := len(keys)
	for i := 0; i < n; i++ {
		if keys[i].less(keys[i]) {
			t.Errorf("%d: not irreflexive", i)
		}
		for j := 0; j < n; j++ {
			if keys[i].less(keys[j]) && keys[j].less(keys[i]) {
				t.Errorf("%q and %q each sort before the other", games[i].Title, games[j].Title)
			}
			for k := 0; k < n; k++ {
				if keys[i].less(keys[j]) && keys[j].less(keys[k]) && !keys[i].less(keys[k]) {
					t.Errorf("cycle: %q < %q < %q but not %q < %q",
						games[i].Title, games[j].Title, games[k].Title, games[i].Title, games[k].Title)
				}
			}
		}
	}
}

// The consequence of a broken comparator isn't just theory: the same library
// listed in a different order came out sorted differently.
func TestSortIsIndependentOfInputOrder(t *testing.T) {
	base := []Game{
		{Title: "F: Zulu", ID: "MOCK00001", Category: "hdd0/PS3ISO"},
		{Title: "F: Alpha", ID: "MOCK00002", Category: "hdd0/PS3ISO"},
		{Title: "F: Mike", ID: "DEMO00001", Category: "hdd0/PS3ISO"},
		{Title: "F Kilo", ID: "MOCK00003", Category: "hdd0/PS3ISO"},
	}
	titles := func(gs []Game) string {
		var b strings.Builder
		for _, g := range gs {
			b.WriteString(g.Title + "|")
		}
		return b.String()
	}
	sortCopy := func(gs []Game) []Game {
		in := append([]Game(nil), gs...)
		keys := gameKeys(in)
		order := make([]int, len(in))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool { return keys[order[a]].less(keys[order[b]]) })
		out := make([]Game, len(in))
		for i, gi := range order {
			out[i] = in[gi]
		}
		return out
	}
	want := titles(sortCopy(base))
	// every permutation of the four must land on the same order
	perm := []int{0, 1, 2, 3}
	var walk func(k int)
	walk = func(k int) {
		if k == len(perm) {
			in := make([]Game, len(perm))
			for i, p := range perm {
				in[i] = base[p]
			}
			if got := titles(sortCopy(in)); got != want {
				t.Fatalf("input order %v sorted to\n  %s\nwant\n  %s", perm, got, want)
			}
			return
		}
		for i := k; i < len(perm); i++ {
			perm[k], perm[i] = perm[i], perm[k]
			walk(k + 1)
			perm[k], perm[i] = perm[i], perm[k]
		}
	}
	walk(0)
}

// --- untrusted text ---

// Every one of these strings ends up printed to the terminal, and webMAN is
// unauthenticated HTTP found by a substring match — so the bytes are not
// trustworthy just because they came from the LAN. A raw ESC in a game title
// is a cursor move, a screen rewrite, or an OSC 52 clipboard write.
func TestRemoteTextIsStrippedOfControlSequences(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"CSI colour", "Game\x1b[31mRED", "Game[31mRED"},
		{"OSC 52 clipboard", "Game\x1b]52;c;cGFzcw==\x07", "Game]52;c;cGFzcw=="},
		{"raw ESC", "\x1bGame", "Game"},
		{"C1 eight-bit CSI", "Game\u009b31m", "Game31m"},
		{"bidi override", "Game\u202eexe.gpj", "Gameexe.gpj"},
		{"NUL and DEL", "Ga\x00me\x7f", "Game"},
		{"newline folds to a space", "Line\nTwo", "Line Two"},
		{"clean text untouched", "Sample Game™ 2 — 日本語", "Sample Game™ 2 — 日本語"},
	}
	for _, c := range cases {
		if got := sanitize(c.in); got != c.want {
			t.Errorf("%s: sanitize(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}

	// and it's applied at the parser boundary, so nothing downstream has to
	// remember to do it
	games := parseGames(`<T key="1" include="inc">` +
		`<P key="title"><>Evil\x1b[2JGame</>` +
		`<P key="module_action"><>/mount_ps3/dev_hdd0/PS3ISO/x.iso</>` +
		`<P key="info"><>hdd0/PS3ISO</></T>`)
	if len(games) != 1 {
		t.Fatalf("fixture parsed to %d games", len(games))
	}
	for _, g := range games {
		if strings.ContainsAny(g.Title+g.Path+g.Category+g.IconPath, "\x1b\x00\x07") {
			t.Errorf("control byte survived into %+v", g)
		}
	}
	st := parseStatus(`Firmware: 4.93\x1bCEX <br>` +
		`<a href="https://google.com/search?q=x">Bad\x1b]0;title\x07Game</a>`)
	if strings.ContainsAny(st.Firmware+st.GameTitle, "\x1b\x07") {
		t.Errorf("control byte survived into status: %q / %q", st.Firmware, st.GameTitle)
	}
}

// webMAN 1.47.48q wrote module_action as a bare path; 1.47.48s prefixes it
// with a webrender argument ("0/mount_ps3/..."). With no slash in front of it
// that digit joined the ADDRESS, not the path — a console at 192.168.0.45 was
// asked for 192.168.0.450, every mount died in DNS as "no such host", and the
// header still showed the address you actually typed. The same missing slash
// left Game.Path carrying the prefix, so the mounted-ISO match, /play.ps3 and
// the size join all quietly stopped agreeing with the console.
func TestMountActionPrefixIsNotGluedOntoTheHost(t *testing.T) {
	b, err := os.ReadFile("testdata/mygames_prefixed.xml")
	if err != nil {
		t.Fatal(err)
	}
	games := parseGames(string(b))
	if len(games) != 3 {
		t.Fatalf("len(games) = %d, want 3", len(games))
	}
	for _, g := range games {
		if !strings.HasPrefix(g.MountURL, "/mount_ps3/") {
			t.Errorf("%q: MountURL = %q", g.Title, g.MountURL)
		}
		if !strings.HasPrefix(g.Path, "/dev_hdd0/") {
			t.Errorf("%q: Path = %q", g.Title, g.Path)
		}
	}

	both := map[string]string{
		"/mount_ps3/dev_hdd0/PS3ISO/x.iso":  "/mount_ps3/dev_hdd0/PS3ISO/x.iso", // 1.47.48q
		"0/mount_ps3/dev_hdd0/PS3ISO/x.iso": "/mount_ps3/dev_hdd0/PS3ISO/x.iso", // 1.47.48s
		"12/mount_ps3/dev_hdd0/GAMES/G":     "/mount_ps3/dev_hdd0/GAMES/G",
		"0/some_future_endpoint/x":          "/some_future_endpoint/x",
		// a value naming a host is retargeted at the console we were pointed
		// at, never followed: it was read off an unauthenticated LAN page, and
		// the next thing sent down that path is a mount, an eject or a
		// shutdown. What can't be salvaged as a path is dropped instead.
		"http://192.168.1.9/mount_ps3/x": "/mount_ps3/x",
		"//192.168.1.9/mount_ps3/x":      "/mount_ps3/x",
		"//evil/x":                       "",
		"mount_ps3/x":                    "",
		"":                               "",
	}
	for in, want := range both {
		if got := mountPath(in); got != want {
			t.Errorf("mountPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// …and the same invariant at the other end, so no path from any source can
// bleed into the authority even if a future webMAN invents a new shape.
func TestGetRefusesPathsThatWouldChangeTheHost(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte("webMAN"))
	}))
	defer srv.Close()

	cli := NewClient(strings.TrimPrefix(srv.URL, "http://"))
	for _, path := range []string{"0/mount_ps3/x", "//example.com/mount_ps3/x", ""} {
		if _, err := cli.get(context.Background(), path); err == nil {
			t.Errorf("get(%q) was sent", path)
		}
	}
	if hits != 0 {
		t.Errorf("%d request(s) left the client", hits)
	}
}

// --- host handling ---

// webMAN is addressed as host[:port]. A value carrying a path silently
// prefixed every endpoint — status became /prefix/cpursx.ps3, a mount became
// /prefix/mount_ps3/... — and the failures read as the console misbehaving.
func TestNormalizeHost(t *testing.T) {
	ok := map[string]string{
		"192.168.1.50":        "192.168.1.50",
		"192.168.1.50:80":     "192.168.1.50:80",
		"http://192.168.1.50": "192.168.1.50", // the obvious thing to paste
		"192.168.1.50/":       "192.168.1.50",
		"  192.168.1.50  ":    "192.168.1.50",
		"ps3.local":           "ps3.local",
		"ps3.local:8080":      "ps3.local:8080",
		"[fe80::1]:80":        "[fe80::1]:80",
		"[2001:db8::1]":       "[2001:db8::1]",
	}
	for in, want := range ok {
		got, err := normalizeHost(in)
		if err != nil {
			t.Errorf("normalizeHost(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("normalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
	bad := []string{
		"",
		"127.0.0.1:8080/prefix", // the case that quietly rewrote every endpoint
		"127.0.0.1?x=1",
		"127.0.0.1#frag",
		"user@127.0.0.1",
		"https://127.0.0.1",
		"127.0.0.1:notaport",
		"127.0.0.1:99999",
		"has space",
		"192.168.0.450",    // the typo that used to reach DNS as a hostname
		"192.168.0.450:80", // …and the same with an explicit port
		"192.168.0",        // a dropped octet
		"192.168.0.4.50",   // a stray one
		"999.999.999.999",
	}
	for _, in := range bad {
		if got, err := normalizeHost(in); err == nil {
			t.Errorf("normalizeHost(%q) accepted it as %q", in, got)
		}
	}
}

// A console that redirects is not the console we were pointed at. Following it
// would aim the next mount/eject/shutdown at whatever answered, while the
// header still displayed the address the user chose.
func TestClientRefusesOffHostRedirects(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("webMAN impostor"))
	}))
	defer elsewhere.Close()

	var sameOriginHits int
	home := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/away":
			http.Redirect(w, r, elsewhere.URL+"/cpursx.ps3", http.StatusFound)
		case "/local":
			http.Redirect(w, r, "/landed", http.StatusFound)
		default:
			sameOriginHits++
			w.Write([]byte("CPU: 58°C"))
		}
	}))
	defer home.Close()

	cli := NewClient(strings.TrimPrefix(home.URL, "http://"))
	if _, err := cli.get(context.Background(), "/away"); err == nil {
		t.Error("followed a redirect to another host")
	}
	// a same-origin redirect is still fine — only the authority is policed
	if _, err := cli.get(context.Background(), "/local"); err != nil {
		t.Errorf("refused a same-origin redirect: %v", err)
	}
	if sameOriginHits != 1 {
		t.Errorf("same-origin redirect landed %d times, want 1", sameOriginHits)
	}
}

// Every action is a GET at a fixed path — webMAN's API, not a choice — so the
// thing worth pinning is that each wrapper addresses the endpoint it claims to
// and that a non-200 is an error rather than a silent success.
func TestActionEndpoints(t *testing.T) {
	var got string
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RequestURI()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte("webMAN 1.47.48q MOD CPU: 58°C"))
	}))
	defer srv.Close()
	cli := NewClient(strings.TrimPrefix(srv.URL, "http://"))
	ctx := context.Background()
	game := Game{
		Title:    "Sample Game™ 2",
		Path:     "/dev_hdd0/PS3ISO/SampleGame2.iso",
		MountURL: "/mount_ps3/dev_hdd0/PS3ISO/SampleGame2.iso",
	}

	cases := []struct {
		name string
		call func() error
		want string
	}{
		{"mount", func() error { return cli.Mount(ctx, game) }, "/mount_ps3/dev_hdd0/PS3ISO/SampleGame2.iso"},
		{"eject", func() error { return cli.Eject(ctx) }, "/mount_ps3/unmount"},
		{"launch", func() error { return cli.Launch(ctx) }, "/play.ps3"},
		{"play", func() error { return cli.Play(ctx, game) }, "/play.ps3/dev_hdd0/PS3ISO/SampleGame2.iso"},
		{"shutdown", func() error { return cli.Shutdown(ctx) }, "/shutdown.ps3"},
		{"restart", func() error { return cli.Restart(ctx) }, "/restart.ps3"},
		{"exit game", func() error { return cli.ExitGame(ctx) }, "/xmb.ps3$exit"},
		{"restart game", func() error { return cli.ReloadGame(ctx) }, "/xmb.ps3$reloadgame"},
		// the message is path-escaped, so spaces and slashes can't reshape the URL
		{"popup", func() error { return cli.Popup(ctx, "hello there/x") }, "/popup.ps3/hello%20there%2Fx"},
	}
	for _, c := range cases {
		got = ""
		if err := c.call(); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s hit %q, want %q", c.name, got, c.want)
		}
	}

	// Status and Games parse what they fetch
	if st, err := cli.Status(ctx); err != nil || st.CPUTemp != 58 {
		t.Errorf("Status = %+v, %v", st, err)
	}
	if got != "/cpursx.ps3" {
		t.Errorf("Status hit %q", got)
	}
	if _, err := cli.Games(ctx); err != nil {
		t.Errorf("Games: %v", err)
	}
	if got != "/dev_hdd0/xmlhost/game_plugin/mygames.xml" {
		t.Errorf("Games hit %q", got)
	}
	if _, err := cli.Fan(ctx, fanUp); err != nil {
		t.Errorf("Fan: %v", err)
	}
	if got != "/cpursx.ps3?up" {
		t.Errorf("Fan hit %q", got)
	}
	if b, err := cli.Cover(ctx, "/dev_hdd0/tmp/wmtmp/x.PNG"); err != nil || len(b) == 0 {
		t.Errorf("Cover = %d bytes, %v", len(b), err)
	}

	// a non-200 must reach the caller — webMAN answering "no" is not success
	fail = true
	for _, c := range cases {
		if err := c.call(); err == nil {
			t.Errorf("%s: HTTP 500 reported as success", c.name)
		}
	}
	if _, err := cli.Status(ctx); err == nil {
		t.Error("Status: HTTP 500 reported as success")
	}
	if _, err := cli.Games(ctx); err == nil {
		t.Error("Games: HTTP 500 reported as success")
	}
	if _, err := cli.Fan(ctx, fanUp); err == nil {
		t.Error("Fan: HTTP 500 reported as success")
	}

	// and so must a dead console
	dead := NewClient("127.0.0.1:1")
	if _, err := dead.Status(ctx); err == nil {
		t.Error("Status against a dead address reported success")
	}
	if err := dead.Eject(ctx); err == nil {
		t.Error("Eject against a dead address reported success")
	}
	if _, err := dead.Games(ctx); err == nil {
		t.Error("Games against a dead address reported success")
	}
	if _, err := dead.Fan(ctx, fanUp); err == nil {
		t.Error("Fan against a dead address reported success")
	}
	// an unbuildable request never leaves the process
	if _, err := dead.get(ctx, "://"); err == nil {
		t.Error("a malformed path built a request")
	}
}

// Console and consoleRank drive the tab strip; an unrecognised category is
// deliberately a PS3 title rather than an error.
func TestConsoleClassification(t *testing.T) {
	for cat, want := range map[string]string{
		"hdd0/PSXISO": "PSX",
		"hdd0/PS2ISO": "PS2",
		"hdd0/PSPISO": "PSP",
		"hdd0/PS3ISO": "PS3",
		"":            "PS3",
		"ntfs0/GAMES": "PS3",
	} {
		if got := (Game{Category: cat}).Console(); got != want {
			t.Errorf("category %q → %q, want %q", cat, got, want)
		}
	}
	if consoleRank("PSX") != 0 {
		t.Error("PSX isn't first in release order")
	}
	if consoleRank("PS3") <= consoleRank("PSP") {
		t.Error("PS3 should sort after PSP")
	}
	if consoleRank("DREAMCAST") != len(consoleOrder) {
		t.Error("an unknown console should sort last, not panic")
	}
}

// A value that matches the pattern but doesn't parse as a number is absent, not
// zero — the whole point of the unknown sentinel.
func TestMatchersRejectUnparseableNumbers(t *testing.T) {
	huge := strings.Repeat("9", 40)
	if got := matchInt(reCPU, "CPU: "+huge+"°C"); got != unknown {
		t.Errorf("an int that overflows parsed as %d, want unknown", got)
	}
	// matches the [\d.,]+ class but isn't a number
	if got := matchFloat(reHDD, "HDD: 1.2.3 GB free"); got != unknown {
		t.Errorf("an unparseable float parsed as %v, want unknown", got)
	}
	if got := matchInt(reCPU, "no temperature here"); got != unknown {
		t.Errorf("a missing int = %d, want unknown", got)
	}
	if got := matchFloat(reHDD, "no disk here"); got != unknown {
		t.Errorf("a missing float = %v, want unknown", got)
	}
	if got := matchStr(reFW, "no firmware here"); got != "" {
		t.Errorf("a missing string = %q", got)
	}
}

// Each of the three fields can fail to parse independently, and any of them
// makes the whole clock meaningless rather than partially usable.
func TestClockSecsPartialGarbage(t *testing.T) {
	for in, want := range map[string]int{
		"1:2":      0, // not three parts
		"aa:bb:cc": 0, // three parts, none numeric
		"xx:23:45": 0, // hours unparseable
		"01:xx:45": 0, // minutes unparseable
		"01:23:xx": 0, // seconds unparseable
		"99:59:59": 359999,
	} {
		if got := clockSecs(in); got != want {
			t.Errorf("clockSecs(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestLessTitles(t *testing.T) {
	ordered := [][2]string{
		{"Sample Quest: The Beginning™", "Sample Quest 2: The Sequel™"}, // word beats number
		{"MOCK PLANET SPECIAL EDITION", "MOCK PLANET 2"},                // ditto, all-caps
		{"Demo Sniper V2", "Demo Sniper 3 Ultimate Edition"},            // "V2" parses as 2
		{"Demo City IV: Complete Edition", "Demo City V"},               // roman numerals
		{"Sample™2", "Sample™3"},                                        // embedded digit
		{"Fixture Storm™", "FIXTURE STORM®: APOCALYPSE"},                // ™/® noise + prefix
	}
	for _, p := range ordered {
		if !lessTitles(p[0], p[1]) {
			t.Errorf("want %q < %q", p[0], p[1])
		}
		if lessTitles(p[1], p[0]) {
			t.Errorf("want %q not < %q", p[1], p[0])
		}
	}
}

// With "Add game-ID to game-title" unticked on /setup.ps3 the title ID moves
// into info — "hdd0/PS3ISO | BLES00455 | v01.90" — and with MM COVERS as the
// covers source the icons are multiMAN's JPEGs (with a doubled slash in the
// path). Captured 2026-08-21; before this every game parsed with an empty
// ID, so rows lost their ID column and play history keyed by ID stopped
// matching its game. The ticked shape, ID as a "[BLES00455]" title suffix,
// still has to parse too.
func TestParseGamesReadsIDsFromInfo(t *testing.T) {
	b, err := os.ReadFile("testdata/mygames_info_ids.xml")
	if err != nil {
		t.Fatal(err)
	}
	games := parseGames(string(b))
	if len(games) != 4 {
		t.Fatalf("len(games) = %d, want 4", len(games))
	}
	byID := map[string]Game{}
	for _, g := range games {
		byID[g.ID] = g
	}
	got, ok := byID["MOCK30982"]
	if !ok {
		t.Fatalf("ID not read from info: %+v", games)
	}
	if got.Title != "Sample Game™ 2" || got.Ver != "01.15" || got.Category != "hdd0/PS3ISO" {
		t.Errorf("entry = %+v, want title/ver/category split out of info", got)
	}
	if got.IconPath != "/dev_hdd0//game/MOCK80608/USRDIR/covers/MOCK30982.JPG" {
		t.Errorf("icon path altered: %q", got.IconPath)
	}
	if g := byID["MOCK98137"]; g.Ver != "" {
		t.Errorf("an entry without a version got one: %+v", g)
	}
	// PSX entries carry an ID in this shape, and it wins over the title key
	if g := byID["DEMO00474"]; g.Console() != "PSX" || statKey(g.ID, g.Title) != "DEMO00474" {
		t.Errorf("PSX entry: %+v", g)
	}
	// the legacy suffix still supplies the ID when info doesn't
	if g := byID["MOCK00001"]; g.Title != "Legacy Title" {
		t.Errorf("legacy suffix entry: %+v", g)
	}
	for _, g := range games {
		if g.Console() == "" || g.Console() == "PSP" {
			t.Errorf("console misread from %q: %+v", g.Category, g)
		}
	}
}

func TestSplitInfo(t *testing.T) {
	for _, c := range []struct{ in, cat, id, ver string }{
		{"hdd0/PS3ISO", "hdd0/PS3ISO", "", ""},
		{"hdd0/PS3ISO | BLES00455", "hdd0/PS3ISO", "BLES00455", ""},
		{"hdd0/PS3ISO | BLES00455 | v01.90", "hdd0/PS3ISO", "BLES00455", "01.90"},
		{"hdd0/PSXISO | SLES00474", "hdd0/PSXISO", "SLES00474", ""},
		{"hdd0/PS3ISO | not-an-id | v2", "hdd0/PS3ISO", "", ""}, // neither shape: left empty, not guessed
	} {
		cat, id, ver := splitInfo(c.in)
		if cat != c.cat || id != c.id || ver != c.ver {
			t.Errorf("splitInfo(%q) = %q/%q/%q, want %q/%q/%q", c.in, cat, id, ver, c.cat, c.id, c.ver)
		}
	}
}

// webMAN's folder listing hangs the exact byte size on each row's mount
// link; only mountable rows carry one, so a stray text file in the folder
// is skipped, and the key is the path webMAN mounts by — Game.Path — so
// the join to the library is by identity. Captured 2026-08-21, synthetic
// twin in testdata.
func TestParseSizes(t *testing.T) {
	b, err := os.ReadFile("testdata/dir_ps3iso.html")
	if err != nil {
		t.Fatal(err)
	}
	got := parseSizes(string(b))
	want := map[string]int64{
		"/dev_hdd0/PS3ISO/SampleGame2.iso":  10036969472,
		"/dev_hdd0/PS3ISO/FixtureStorm.iso": 3735420928,
		"/dev_hdd0/PS3ISO/Legacy.iso":       3735420928,
		"/dev_hdd0/PS3ISO/Spaced Name.iso":  1104,
	}
	if len(got) != len(want) {
		t.Errorf("parsed %d entries, want %d: %v", len(got), len(want), got)
	}
	for p, n := range want {
		if got[p] != n {
			t.Errorf("%s = %d, want %d", p, got[p], n)
		}
	}
	if _, ok := got["/dev_hdd0/PS3ISO/Notes.txt"]; ok {
		t.Error("a non-mountable row was taken for a game")
	}
}
