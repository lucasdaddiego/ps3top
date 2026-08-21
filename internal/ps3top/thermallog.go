package ps3top

// The long-term thermal log — the record the one-hour rings can't keep.
// One NDJSON line per minute of polling, holding the minute's peak CPU, RSX
// and fan readings (peaks, because the question this answers is "how hot
// does it get", and a minute's max survives the 30s in-game cadence fine).
// Downsampled on the way in, so the file grows ~60 bytes a minute of
// polling and never needs a compactor; read only when the history page is
// opened, never at startup.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type thermalRec struct {
	T   time.Time `json:"t"`
	CPU int       `json:"cpu,omitempty"`
	RSX int       `json:"rsx,omitempty"`
	Fan int       `json:"fan,omitempty"`
}

// thermalLog folds readings into the open minute and appends it when the
// clock moves on. Peaks over unknown readings stay unknown (-1 → omitted).
type thermalLog struct {
	path          string
	minute        time.Time
	open          bool
	cpu, rsx, fan int
}

func newThermalLog(dir string) *thermalLog {
	return &thermalLog{path: filepath.Join(dir, "thermal.ndjson"), cpu: unknown, rsx: unknown, fan: unknown}
}

// note folds one reading in. A reading with nothing known is still a
// minute that was polled, but an empty record says nothing — skipped.
func (l *thermalLog) note(now time.Time, st Status) error {
	if st.CPUTemp == unknown && st.RSXTemp == unknown && st.FanPct == unknown {
		return nil
	}
	minute := now.Truncate(time.Minute)
	var err error
	if l.open && !minute.Equal(l.minute) {
		err = l.flush()
	}
	if !l.open {
		l.minute, l.open = minute, true
		l.cpu, l.rsx, l.fan = unknown, unknown, unknown
	}
	l.cpu = max(l.cpu, st.CPUTemp)
	l.rsx = max(l.rsx, st.RSXTemp)
	l.fan = max(l.fan, st.FanPct)
	return err
}

// flush appends the open minute — called when the minute rolls and on quit,
// so the last minute of a session isn't lost.
func (l *thermalLog) flush() error {
	if !l.open {
		return nil
	}
	rec := thermalRec{T: l.minute, CPU: max(l.cpu, 0), RSX: max(l.rsx, 0), Fan: max(l.fan, 0)}
	l.open = false
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// loadThermal reads the whole log, oldest first. Junk lines are skipped,
// like the play history — a half-written line from a power cut is not a
// reason to lose the rest.
func loadThermal(path string) ([]thermalRec, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var recs []thermalRec
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var r thermalRec
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil || r.T.IsZero() {
			continue
		}
		recs = append(recs, r)
	}
	if err := sc.Err(); err != nil {
		return recs, fmt.Errorf("%s: %w", path, err)
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].T.Before(recs[j].T) })
	return recs, nil
}

// weekStat is one week of the log as the history page shows it. Minutes is
// how many were polled; the peaks are the week's hottest readings and the
// averages the mean of the per-minute peaks; the fan bands are the share of
// minutes the fan sat in each of the header's colour bands.
type weekStat struct {
	Start            time.Time // Monday 00:00 local
	Minutes          int
	PeakCPU, PeakRSX int
	AvgCPU, AvgRSX   int
	FanLow, FanMid   int // % of minutes under 50 / 50–69
	FanHigh          int // % of minutes at 70+
}

// weekOf is the local Monday a time falls in.
func weekOf(t time.Time) time.Time {
	t = t.Local()
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	back := (int(d.Weekday()) + 6) % 7 // Monday → 0 … Sunday → 6
	return d.AddDate(0, 0, -back)
}

// weeklyStats folds the log into weeks, newest first.
func weeklyStats(recs []thermalRec) []weekStat {
	type acc struct {
		weekStat
		sumCPU, sumRSX, nCPU, nRSX int
		low, mid, high, nFan       int
	}
	byWeek := map[time.Time]*acc{}
	for _, r := range recs {
		w := weekOf(r.T)
		a := byWeek[w]
		if a == nil {
			a = &acc{weekStat: weekStat{Start: w}}
			byWeek[w] = a
		}
		a.Minutes++
		if r.CPU > 0 {
			a.sumCPU, a.nCPU = a.sumCPU+r.CPU, a.nCPU+1
			a.PeakCPU = max(a.PeakCPU, r.CPU)
		}
		if r.RSX > 0 {
			a.sumRSX, a.nRSX = a.sumRSX+r.RSX, a.nRSX+1
			a.PeakRSX = max(a.PeakRSX, r.RSX)
		}
		if r.Fan > 0 {
			a.nFan++
			switch {
			case r.Fan >= 70:
				a.high++
			case r.Fan >= 50:
				a.mid++
			default:
				a.low++
			}
		}
	}
	out := make([]weekStat, 0, len(byWeek))
	for _, a := range byWeek {
		s := a.weekStat
		if a.nCPU > 0 {
			s.AvgCPU = a.sumCPU / a.nCPU
		}
		if a.nRSX > 0 {
			s.AvgRSX = a.sumRSX / a.nRSX
		}
		if a.nFan > 0 {
			s.FanLow, s.FanMid, s.FanHigh = 100*a.low/a.nFan, 100*a.mid/a.nFan, 100*a.high/a.nFan
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.After(out[j].Start) })
	return out
}
