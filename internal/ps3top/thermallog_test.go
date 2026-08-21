package ps3top

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

// One line per minute of polling, holding the minute's peaks; unknown
// readings never lower a peak; the open minute is written when the clock
// rolls and on flush (quit), so the last minute of a session isn't lost.
func TestThermalLogFoldsMinutes(t *testing.T) {
	dir := t.TempDir()
	l := newThermalLog(dir)
	t0 := time.Date(2026, 8, 21, 20, 0, 5, 0, time.UTC)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(l.note(t0, Status{CPUTemp: 60, RSXTemp: 62, FanPct: 30}))
	must(l.note(t0.Add(15*time.Second), Status{CPUTemp: 64, RSXTemp: unknown, FanPct: 31}))
	must(l.note(t0.Add(30*time.Second), Status{CPUTemp: unknown, RSXTemp: unknown, FanPct: unknown})) // nothing known: ignored
	if _, err := os.Stat(l.path); !os.IsNotExist(err) {
		t.Fatal("a minute was written before it was over")
	}
	must(l.note(t0.Add(61*time.Second), Status{CPUTemp: 61, RSXTemp: 63, FanPct: 30})) // rolls the minute
	must(l.flush())

	recs, err := loadThermal(l.path)
	must(err)
	if len(recs) != 2 {
		t.Fatalf("%d records, want 2", len(recs))
	}
	if r := recs[0]; !r.T.Equal(t0.Truncate(time.Minute)) || r.CPU != 64 || r.RSX != 62 || r.Fan != 31 {
		t.Errorf("first minute = %+v, want 20:00 64°/62°/31%%", r)
	}
	if r := recs[1]; r.CPU != 61 || r.RSX != 63 {
		t.Errorf("second minute = %+v", r)
	}
	// flushing with nothing open writes nothing
	must(l.flush())
	if recs, _ := loadThermal(l.path); len(recs) != 2 {
		t.Error("an empty flush wrote a record")
	}
	// junk and a missing file are both tolerated
	f, _ := os.OpenFile(l.path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("{\"t\":\"not a time\"}\n{broken\n")
	f.Close()
	if recs, err := loadThermal(l.path); err != nil || len(recs) != 2 {
		t.Errorf("junk lines: %d records, %v", len(recs), err)
	}
	if recs, err := loadThermal(filepath.Join(dir, "nope.ndjson")); err != nil || recs != nil {
		t.Errorf("missing log: %v, %v", recs, err)
	}
}

// Weeks start on Monday, newest first; peaks are the week's hottest
// minute, averages the mean of the minutes, and the fan bands split the
// minutes the way the header colours them.
func TestWeeklyStats(t *testing.T) {
	mon := time.Date(2026, 8, 17, 10, 0, 0, 0, time.Local) // a Monday
	recs := []thermalRec{
		{T: mon, CPU: 60, RSX: 62, Fan: 30},
		{T: mon.Add(time.Minute), CPU: 70, RSX: 64, Fan: 55},
		{T: mon.AddDate(0, 0, 6), CPU: 62, RSX: 66, Fan: 75}, // Sunday: same week
		{T: mon.AddDate(0, 0, 7), CPU: 58, RSX: 59, Fan: 28}, // next Monday: new week
		{T: mon.AddDate(0, 0, -1), CPU: 75, RSX: 77, Fan: 0}, // the Sunday before: older week, no fan reading
	}
	weeks := weeklyStats(recs)
	if len(weeks) != 3 {
		t.Fatalf("%d weeks, want 3", len(weeks))
	}
	if weeks[0].Start.Day() != 24 || weeks[1].Start.Day() != 17 || weeks[2].Start.Day() != 10 {
		t.Errorf("weeks not newest-first on Mondays: %v / %v / %v", weeks[0].Start, weeks[1].Start, weeks[2].Start)
	}
	w := weeks[1] // the week of the 17th
	if w.Minutes != 3 || w.PeakCPU != 70 || w.AvgCPU != 64 || w.PeakRSX != 66 || w.AvgRSX != 64 {
		t.Errorf("week of the 17th = %+v", w)
	}
	if w.FanLow != 33 || w.FanMid != 33 || w.FanHigh != 33 {
		t.Errorf("fan bands = %d/%d/%d, want a third each", w.FanLow, w.FanMid, w.FanHigh)
	}
	if old := weeks[2]; old.Minutes != 1 || old.PeakCPU != 75 || old.FanLow+old.FanMid+old.FanHigh != 0 {
		t.Errorf("older week = %+v", old)
	}
	if weekOf(mon).Weekday() != time.Monday || weekOf(mon.AddDate(0, 0, 6)).Weekday() != time.Monday {
		t.Error("weeks don't start on Monday")
	}
}

// Inside the thermal screen, h opens the long-term page (reading the log
// fresh), h again returns to the live one, and esc/q/t leave the screen
// from either page. The page renders within the width in every state.
func TestThermalHistoryPage(t *testing.T) {
	m := liveModel(t, 120)
	m.tlog = newThermalLog(t.TempDir())
	now := time.Now()
	for i := range 3 {
		if err := m.tlog.note(now.Add(time.Duration(i)*time.Minute), Status{CPUTemp: 60 + i, RSXTemp: 62, FanPct: 30}); err != nil {
			t.Fatal(err)
		}
	}
	m.handleKey(key("t"))
	_, cmd := m.handleKey(key("h"))
	if m.thermalPage != 1 || cmd == nil {
		t.Fatal("h didn't open the history page with a load")
	}
	m = exec(t, m, cmd)
	if len(m.tHist) != 2 { // two closed minutes; the third is still open
		t.Errorf("history page loaded %d minutes, want 2", len(m.tHist))
	}
	for name, f := range map[string]func(){
		"loaded":  func() {},
		"empty":   func() { m.tHist = nil },
		"failing": func() { m.tHistErr = os.ErrPermission },
	} {
		f()
		for i, line := range strings.Split(m.frame(), "\n") {
			if lipgloss.Width(line) > m.width {
				t.Errorf("%s: line %d is %d cols", name, i, lipgloss.Width(line))
			}
		}
	}
	m.tHist, m.tHistErr = nil, nil
	if !strings.Contains(m.frame(), "no thermal log yet") {
		t.Error("empty log isn't explained")
	}
	if !strings.Contains(m.tabLine(), "thermal history") {
		t.Error("tab strip doesn't name the page")
	}
	m.handleKey(key("h"))
	if m.thermalPage != 0 {
		t.Error("h didn't return to the live page")
	}
	m.handleKey(key("h"))
	m.handleKey(key("esc"))
	if m.thermalOn || m.thermalPage != 0 {
		t.Error("esc from the history page didn't leave the screen clean")
	}
	// quitting flushes the open minute
	m.handleKey(key("q"))
	if recs, _ := loadThermal(m.tlog.path); len(recs) != 3 {
		t.Errorf("quit left the open minute unwritten: %d records", len(recs))
	}
}

// A poll feeds the log; a log that can't be written is flashed once.
func TestStatusFeedsTheThermalLog(t *testing.T) {
	m := liveModel(t, 120)
	m.tlog = newThermalLog(t.TempDir())
	m.Update(statusMsg{st: m.st})
	if !m.tlog.open || m.tlog.cpu != m.st.CPUTemp {
		t.Errorf("poll didn't reach the log: %+v", m.tlog)
	}
	// an unwritable path: the roll-over fails, flashes once, and stays quiet
	m.tlog = newThermalLog(filepath.Join(t.TempDir(), "file-not-dir"))
	os.WriteFile(filepath.Dir(m.tlog.path), []byte("x"), 0o644)
	prev := timeNow
	t.Cleanup(func() { timeNow = prev })
	base := time.Now()
	timeNow = func() time.Time { return base }
	m.Update(statusMsg{st: m.st})
	timeNow = func() time.Time { return base.Add(2 * time.Minute) }
	m.Update(statusMsg{st: m.st})
	if !strings.Contains(m.flash, "thermal log") {
		t.Errorf("write failure not flashed: %q", m.flash)
	}
	m.flash = ""
	timeNow = func() time.Time { return base.Add(4 * time.Minute) }
	m.Update(statusMsg{st: m.st})
	if m.flash != "" {
		t.Errorf("write failure flashed again: %q", m.flash)
	}
}
