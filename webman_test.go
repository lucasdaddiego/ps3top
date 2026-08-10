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

	series := [][]string{
		{"MOCK00103", "MOCK00123", "MOCK00233"}, // Sample Quest: subtitle → 2 → 3
		{"MOCK30113", "MOCK00002", "MOCK31020"}, // Mock Planet: word edition → 2 → 3
		{"DEMO41022", "DEMO31532"},              // Demo Sniper: V2 → 3
		{"DEMO30796", "DEMO30919"},              // Demo Warrior → 2
		{"MOCK30386", "MOCK30982"},              // Sample Game → 2
		{"DEMO30682", "DEMO01807"},              // Demo City IV → V
	}
	for _, s := range series {
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
