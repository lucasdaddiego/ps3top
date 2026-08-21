package ps3top

// What each successful poll feeds: the metric history rings, and the open
// play session that becomes one history.ndjson line when the game stops.

import (
	"fmt"
	"time"
)

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
