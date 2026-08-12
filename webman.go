package main

// webMAN MOD HTTP client + parsers for the sMAN-skinned build (1.47.48).
// Etiquette: webMAN's web server has ~4 session slots — every request uses a
// fresh connection (no keep-alive), short timeouts, and callers must keep at
// most one status request in flight.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// unknown marks a numeric field the status page didn't carry. It has to be a
// sentinel rather than the zero value: webMAN legitimately reports 0 for some
// of these (FAN SPEED: 0%), so 0 cannot mean "absent". Before this existed a
// markup change made every metric parse as 0 — and 0°C renders as healthy
// green, i.e. a broken parser looked like a perfectly cool console.
const unknown = -1

type Status struct {
	InGame     bool
	GameTitle  string // "Sample Game™ 2" (version suffix stripped)
	GameID     string // "MOCK30982"
	GameVer    string // "01.15"
	CPUTemp    int    // °C, or unknown
	RSXTemp    int    // °C, or unknown
	FanMode    string // "SYSCON" or "manual" ("" if neither marker is present)
	FanPct     int    // %, or unknown
	MaxTemp    int    // manual mode only: °C ceiling at which webMAN forces the fan up; else unknown
	MemFreeKB  int    // free available memory (meminfo.avail) — ~1MB in-game is normal; or unknown
	HDDFreeGB  float64
	PlayTime   string // "00:28:34" (empty on XMB)
	PlaySecs   int    // PlayTime in seconds — the authoritative session length
	Uptime     string // "00:28:53"
	MountedISO string // "/dev_hdd0/PS3ISO/SampleGame2.iso", "" if nothing mounted
	Firmware   string // "4.93 CEX PS3HEN 3.5.0"
	WMVersion  string // "1.47.48q"
	Lifetime   string // "218d 07:19:14 • 2,709 ON • 2,644 OFF (65)" (raw)
	LifeDays   int    // 218 — total powered-on days (syscon counter)
	Boots      int    // 2709 — power-on count
	HardOffs   int    // 65 — power-ons minus clean power-offs = unclean shutdowns
}

// statusFields is how many of the page's core fields parseStatus expects to
// find; missing() reports how many it actually lost, which is the signal that
// webMAN's markup moved under us.
const statusFields = 6

func (s Status) missing() int {
	n := 0
	for _, v := range []int{s.CPUTemp, s.RSXTemp, s.FanPct, s.MemFreeKB} {
		if v == unknown {
			n++
		}
	}
	if s.HDDFreeGB == unknown {
		n++
	}
	if s.Firmware == "" {
		n++
	}
	return n
}

type Game struct {
	Title    string // "Sample Game™ 2"
	ID       string // "MOCK30982"
	Path     string // "/dev_hdd0/PS3ISO/SampleGame2.iso"
	MountURL string // "/mount_ps3/dev_hdd0/PS3ISO/SampleGame2.iso"
	IconPath string // "/dev_hdd0/tmp/wmtmp/SampleGame2.PNG"
	Category string // "hdd0/PS3ISO"
}

func (g Game) IsPSX() bool { return g.Console() == "PSX" }

// consoleOrder is chronological — release order, oldest first.
var consoleOrder = []string{"PSX", "PS2", "PSP", "PS3"}

// Console derives the platform from webMAN's source-folder category
// (hdd0/PSXISO, hdd0/PS2ISO, …; anything unrecognized is a PS3 title).
func (g Game) Console() string {
	switch {
	case strings.Contains(g.Category, "PSX"):
		return "PSX"
	case strings.Contains(g.Category, "PS2"):
		return "PS2"
	case strings.Contains(g.Category, "PSP"):
		return "PSP"
	}
	return "PS3"
}

func consoleRank(name string) int {
	for i, c := range consoleOrder {
		if c == name {
			return i
		}
	}
	return len(consoleOrder)
}

type Client struct {
	base string
	http *http.Client
}

func NewClient(host string) *Client {
	return &Client{
		base: "http://" + host,
		http: &http.Client{
			// Hard backstop only — every caller passes a context with the real
			// deadline (status 6s, games/covers 8s), which also bounds body reads.
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DisableKeepAlives: true,
				DialContext:       (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
			},
		},
	}
}

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Connection", "close")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// fire hits an action endpoint and discards the response body.
func (c *Client) fire(ctx context.Context, path string) error {
	_, err := c.get(ctx, path)
	return err
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	body, err := c.get(ctx, "/cpursx.ps3")
	if err != nil {
		return Status{}, err
	}
	return parseStatus(string(body)), nil
}

func (c *Client) Games(ctx context.Context) ([]Game, error) {
	body, err := c.get(ctx, "/dev_hdd0/xmlhost/game_plugin/mygames.xml")
	if err != nil {
		return nil, err
	}
	return parseGames(string(body)), nil
}

func (c *Client) Cover(ctx context.Context, iconPath string) ([]byte, error) {
	return c.get(ctx, iconPath)
}

func (c *Client) Mount(ctx context.Context, g Game) error { return c.fire(ctx, g.MountURL) }
func (c *Client) Eject(ctx context.Context) error         { return c.fire(ctx, "/mount_ps3/unmount") }
func (c *Client) Launch(ctx context.Context) error        { return c.fire(ctx, "/play.ps3") }
func (c *Client) Play(ctx context.Context, g Game) error  { return c.fire(ctx, "/play.ps3"+g.Path) }
func (c *Client) Shutdown(ctx context.Context) error      { return c.fire(ctx, "/shutdown.ps3") }
func (c *Client) Restart(ctx context.Context) error       { return c.fire(ctx, "/restart.ps3") }

func (c *Client) Popup(ctx context.Context, msg string) error {
	return c.fire(ctx, "/popup.ps3/"+url.PathEscape(msg))
}

// Fan commands, taken from the links the status page hangs on its own
// temperature and fan readings: "up"/"dn" step the fan speed, "mode" toggles
// SYSCON ↔ manual.
//
// These render the whole status page as their response, so the reply IS the
// new state — no follow-up poll, and the caller can report the actual delta
// instead of assuming the step landed.
const (
	fanUp   = "up"
	fanDown = "dn"
	fanMode = "mode"
)

func (c *Client) Fan(ctx context.Context, cmd string) (Status, error) {
	body, err := c.get(ctx, "/cpursx.ps3?"+cmd)
	if err != nil {
		return Status{}, err
	}
	return parseStatus(string(body)), nil
}

// --- parsers (markers pinned from testdata/cpursx_ingame.html) ---

var (
	reCPU     = regexp.MustCompile(`CPU:\s*(\d+)°C`)
	reRSX     = regexp.MustCompile(`RSX:\s*(\d+)°C`)
	reFanMode = regexp.MustCompile(`\[Fan control:\s*([^\]]+)\]`)
	// webMAN doesn't just drop the fan-control label in manual mode — it
	// swaps that slot for the temperature ceiling at which it forces the fan
	// up. °C is required so the Fahrenheit copy of the line ("MAX: 186°F")
	// can't match. Confirmed by a before/after capture across a ?mode toggle.
	reFanMax   = regexp.MustCompile(`\(MAX:\s*(\d+)°C\)`)
	reFanPct   = regexp.MustCompile(`FAN SPEED:\s*(\d+)%`)
	reMem      = regexp.MustCompile(`MEM:\s*([\d,]+)\s*KB`)
	reHDD      = regexp.MustCompile(`HDD:\s*([\d.,]+)\s*GB free`)
	rePlay     = regexp.MustCompile(`(?s)title="Play">.*?</label>\s*([\d:]+)`)
	reUptime   = regexp.MustCompile(`(?s)title="Startup">.*?</label>\s*([\d:]+)`)
	reGameID   = regexp.MustCompile(`/tpl/np/([A-Z0-9]{9})/`)
	reGameName = regexp.MustCompile(`google\.com/search\?q=[^"]*">([^<]+)</a>`)
	rePID      = regexp.MustCompile(`>pid=[0-9A-Fa-fx]+<`)
	reMounted  = regexp.MustCompile(`href="/mount\.ps3(/[^"]+)"`)
	reFW       = regexp.MustCompile(`Firmware:\s*([^<]+?)\s*<`)
	reWM       = regexp.MustCompile(`webMAN\s+([\w.]+)\s+MOD`)
	reLifetime = regexp.MustCompile(`power\.png[^>]*>\s*([^<]+?)\s*</H1>`)
	// the dot is required: a bare trailing digit is part of the title
	// ("MOCK PLANET 2"), only "NN.NN" is an APP_VER suffix
	reGameVer  = regexp.MustCompile(`\s+(\d+\.[\d.]+)$`)
	reLifeBits = regexp.MustCompile(`^(\d+)d [\d:]+ • ([\d,]+) ON • [\d,]+ OFF \((\d+)\)`)
)

func parseStatus(html string) Status {
	s := Status{
		MaxTemp:   unknown,
		PlayTime:  matchStr(rePlay, html),
		Uptime:    matchStr(reUptime, html),
		GameID:    matchStr(reGameID, html),
		Firmware:  matchStr(reFW, html),
		WMVersion: matchStr(reWM, html),
		Lifetime:  matchStr(reLifetime, html),
	}
	s.CPUTemp = matchInt(reCPU, html)
	s.RSXTemp = matchInt(reRSX, html)
	s.FanPct = matchInt(reFanPct, html)
	s.MemFreeKB = matchInt(reMem, html)
	s.HDDFreeGB = matchFloat(reHDD, html)
	s.PlaySecs = clockSecs(s.PlayTime)
	s.InGame = rePID.MatchString(html)

	// ?mode cycles three states, and each renders that one slot differently —
	// so the mode is read from which marker is present, including neither:
	//
	//   [Fan control: SYSCON]   console's own control
	//   (MAX: 98°C)             webMAN ramps to hold a target temp
	//   nothing                 manual fixed %
	//
	// Which matters beyond the label: ?up/?dn move the *target* in dynamic
	// mode and the *percentage* in manual, so callers can't assume what a
	// step did. The FanPct guard keeps a page that didn't parse at all from
	// reporting itself as manual.
	if mode := matchStr(reFanMode, html); mode != "" {
		s.FanMode = mode
	} else if mx := matchInt(reFanMax, html); mx != unknown {
		s.FanMode, s.MaxTemp = "dynamic", mx
	} else if s.FanPct != unknown {
		s.FanMode = "manual"
	}
	if m := reGameName.FindStringSubmatch(html); m != nil {
		if v := reGameVer.FindStringSubmatch(m[1]); v != nil {
			s.GameVer = v[1]
		}
		s.GameTitle = strings.TrimSpace(reGameVer.ReplaceAllString(m[1], ""))
	}
	if m := reMounted.FindStringSubmatch(html); m != nil {
		s.MountedISO = m[1]
	}
	if m := reLifeBits.FindStringSubmatch(s.Lifetime); m != nil {
		s.LifeDays, _ = strconv.Atoi(m[1])
		s.Boots, _ = strconv.Atoi(strings.ReplaceAll(m[2], ",", ""))
		s.HardOffs, _ = strconv.Atoi(m[3])
	}
	return s
}

func matchStr(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func matchInt(re *regexp.Regexp, s string) int {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return unknown
	}
	n, err := strconv.Atoi(strings.ReplaceAll(m[1], ",", ""))
	if err != nil {
		return unknown
	}
	return n
}

func matchFloat(re *regexp.Regexp, s string) float64 {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return unknown
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
	if err != nil {
		return unknown
	}
	return f
}

// clockSecs converts webMAN's "HH:MM:SS" to seconds (0 if unparseable).
func clockSecs(c string) int {
	parts := strings.Split(c, ":")
	if len(parts) != 3 {
		return 0
	}
	h, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	s, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return 0
	}
	return h*3600 + mi*60 + s
}

// mygames.xml is XMB-flavored pseudo-XML (`<>value</>` is not a legal tag),
// so encoding/xml can't touch it — regex it is.
var (
	reEntry = regexp.MustCompile(`(?s)<T key="\d+" include="inc">(.*?)</T>`)
	reField = regexp.MustCompile(`<P key="(icon|title|module_action|info)"><>([^<]*)</>`)
	reTitID = regexp.MustCompile(`\s*\[([A-Z0-9]{9})\]\s*$`)
)

func parseGames(xml string) []Game {
	var games []Game
	for _, e := range reEntry.FindAllStringSubmatch(xml, -1) {
		var g Game
		for _, f := range reField.FindAllStringSubmatch(e[1], -1) {
			switch f[1] {
			case "icon":
				g.IconPath = f[2]
			case "title":
				// titles can contain literal newlines (e.g. "MOCK PLANET\nSPECIAL EDITION")
				g.Title = strings.Join(strings.Fields(f[2]), " ")
			case "module_action":
				g.MountURL = f[2]
			case "info":
				g.Category = f[2]
			}
		}
		if m := reTitID.FindStringSubmatch(g.Title); m != nil {
			g.ID = m[1]
			g.Title = strings.TrimSpace(reTitID.ReplaceAllString(g.Title, ""))
		}
		g.Path = strings.TrimPrefix(g.MountURL, "/mount_ps3")
		if g.MountURL != "" {
			games = append(games, g)
		}
	}
	// consoles in chronological order (PSX → PS2 → PSP → PS3); within a
	// console, series-aware alphabetical (originals before numbered sequels)
	sort.Slice(games, func(i, j int) bool {
		ri, rj := consoleRank(games[i].Console()), consoleRank(games[j].Console())
		if ri != rj {
			return ri < rj
		}
		return lessGames(games[i], games[j])
	})
	return games
}

var reFranchise = regexp.MustCompile(`^([^:]+):`)

// franchiseKey is the normalized text before a title's first colon
// ("FIXTURE STORM®: APOCALYPSE" → "fixture storm"), or "" for colon-less titles.
func franchiseKey(title string) string {
	t := strings.NewReplacer("™", "", "®", "").Replace(strings.ToLower(title))
	m := reFranchise.FindStringSubmatch(t)
	if m == nil {
		return ""
	}
	return strings.Join(strings.Fields(m[1]), " ")
}

// lessGames adds one hint on top of lessTitles: title-ID digits are assigned
// in registration order within a prefix pool, so two entries of the SAME
// colon-franchise with the SAME 4-letter prefix sort by ID — release order
// for subtitle-only sequels the words can't date. It stays a hint only:
// pools differ per region/publisher block (a franchise can hop pools between
// releases, e.g. BLUS → BLES), so everything else uses the title rules.
func lessGames(a, b Game) bool {
	if fa := franchiseKey(a.Title); fa != "" && fa == franchiseKey(b.Title) &&
		len(a.ID) == 9 && len(b.ID) == 9 && a.ID[:4] == b.ID[:4] && a.ID != b.ID {
		return a.ID < b.ID
	}
	return lessTitles(a.Title, b.Title)
}

// lessTitles orders titles for display: token-by-token, numbers compare
// numerically (arabic, roman, and "V2" style), and at the position where one
// title has a word and the other a number, the word wins — so "Sample
// Quest: The Beginning" precedes "Sample Quest 2" and "MOCK PLANET SPECIAL
// EDITION" precedes "MOCK PLANET 2", matching release order for numbered
// series.
func lessTitles(a, b string) bool {
	ta, tb := titleTokens(a), titleTokens(b)
	for i := 0; i < len(ta) && i < len(tb); i++ {
		x, y := ta[i], tb[i]
		switch {
		case x.isNum && y.isNum:
			if x.num != y.num {
				return x.num < y.num
			}
		case x.isNum != y.isNum:
			return y.isNum
		default:
			if x.word != y.word {
				return x.word < y.word
			}
		}
	}
	if len(ta) != len(tb) {
		return len(ta) < len(tb)
	}
	return sortKey(a) < sortKey(b)
}

type ttok struct {
	isNum bool
	num   int
	word  string
}

var (
	reVNum  = regexp.MustCompile(`^v(\d+)$`)
	reRoman = regexp.MustCompile(`^[ivx]{1,7}$`)
	reAlnum = regexp.MustCompile(`[a-z0-9]+`)
	reRuns  = regexp.MustCompile(`[a-z]+|\d+`)
)

func titleTokens(title string) []ttok {
	t := strings.NewReplacer("™", "", "®", "").Replace(strings.ToLower(title))
	var toks []ttok
	for _, w := range reAlnum.FindAllString(t, -1) {
		switch {
		case reVNum.MatchString(w): // "v2" → 2
			n, _ := strconv.Atoi(reVNum.FindStringSubmatch(w)[1])
			toks = append(toks, ttok{isNum: true, num: n})
		case reRoman.MatchString(w): // "iv" → 4
			toks = append(toks, ttok{isNum: true, num: romanVal(w)})
		default: // split embedded digits: "sample2" → sample, 2
			for _, run := range reRuns.FindAllString(w, -1) {
				if n, err := strconv.Atoi(run); err == nil {
					toks = append(toks, ttok{isNum: true, num: n})
				} else {
					toks = append(toks, ttok{word: run})
				}
			}
		}
	}
	return toks
}

func romanVal(s string) int {
	vals := map[byte]int{'i': 1, 'v': 5, 'x': 10}
	total := 0
	for i := 0; i < len(s); i++ {
		v := vals[s[i]]
		if i+1 < len(s) && vals[s[i+1]] > v {
			total += vals[s[i+1]] - v
			i++
		} else {
			total += v
		}
	}
	return total
}

// sortKey lowercases and drops trademark noise so "FIXTURE STORM®" and
// "Fixture Storm™" collate together.
func sortKey(title string) string {
	t := strings.ToLower(title)
	t = strings.NewReplacer("™", "", "®", "").Replace(t)
	return strings.TrimSpace(t)
}
