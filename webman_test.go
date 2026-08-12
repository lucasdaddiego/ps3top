package main

import (
	"os"
	"strings"
	"testing"
)

// The fixtures in testdata/ are synthetic: byte-faithful to real webMAN
// (sMAN skin) output in structure, but every title, ID, and counter is
// invented. The fake title IDs keep the real 9-char shape ([A-Z0-9]{9})
// and use two 4-char "pools" (MOCK/DEMO) so the franchise-ID sort hint has
// both a same-pool and a cross-pool case to chew on.

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
	if s.Lifetime == "" {
		t.Error("Lifetime empty")
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
		if g.IsPSX() {
			psx++
		}
		if g.MountURL == "" || g.Title == "" {
			t.Errorf("incomplete entry: %+v", g)
		}
		if !g.IsPSX() && g.ID == "" {
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
			if prev.Console() == g.Console() && lessGames(g, prev) {
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

func TestFranchiseIDHint(t *testing.T) {
	// same colon-franchise + same prefix pool → registration (≈release) order,
	// regardless of subtitle alphabet
	older := Game{Title: "Demo Saga: Chains", ID: "MOCK98110", Category: "hdd0/PS3ISO"}
	newer := Game{Title: "Demo Saga: Ascension", ID: "MOCK98232", Category: "hdd0/PS3ISO"}
	if !lessGames(older, newer) || lessGames(newer, older) {
		t.Error("same-pool franchise entries should sort by title ID")
	}
	// different pools (DEMO vs MOCK) → hint unusable, alphabetical fallback
	apoc := Game{Title: "FIXTURE STORM®: APOCALYPSE", ID: "DEMO00484", Category: "hdd0/PS3ISO"}
	rift := Game{Title: "FIXTURE STORM®: PACIFIC RIFT", ID: "MOCK98155", Category: "hdd0/PS3ISO"}
	if !lessGames(apoc, rift) {
		t.Error("cross-pool franchise entries should fall back to title order")
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
