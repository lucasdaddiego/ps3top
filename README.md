# ps3top

htop for a HEN'd PS3 — talks to webMAN MOD over HTTP. Status, temps, running
game, and mount/launch/eject of the game library, in one dashboard. Built
2026-08-09 against webMAN MOD 1.47.48 (sMAN skin) on FW 4.93 PS3HEN.

```
╭─ ps3top · 192.168.0.100 · online ─────────────────────────────────────── 4.93 HEN · wM 1.47.48q ─╮
│ CPU 58° ▁▁▂▃▄▅▅▆↗   RSX 61° ▃▃▃▃▄▄▄▄   FAN 26% ▄▄▄▄▄▄▄▄   HDD 123G   MEM 1.1M  ∞ 100d · up 1h24m │
│ ▶ Sample Game™ 2 v01.15 · MOCK30982                                                   play 1h23m │
╰──────────────────────────────────────────────────────────────────────────────────────────────────╯

──  PSX · 1 │ PS3 · 21  ───────────────────────────────────────────────────────────────────── 1/3 ──
▌ 16  Sample Game™ 2             [MOCK30982] ● mounted                                        38h12m
  17  Sample Quest: The Beginni… [MOCK00103]                                                   6h04m
  18  Fixture Storm™             [MOCK98137]
── ⏎ mount · p play · u eject · / filter · s sort · t thermals · r refresh · g rescan · q quit ─────
```

(A real terminal also carries the cover art panel on the right, at ≥84 cols —
and with the panel up, the per-row play total goes: it sits under the cover
instead, and the column's width is worth more to the cover.)
The status frame turns red while the temp alarm is active. Healthy metric
values render green; the active console tab is highlighted in the brand blue.

## Install / run

```sh
go install github.com/lucasdaddiego/ps3top@latest              # → ~/go/bin/ps3top
go install github.com/lucasdaddiego/ps3top/cmd/binmerge@latest # → ~/go/bin/binmerge
ps3top              # auto-discovers the console
```

Two binaries: `ps3top`, the TUI, and [`binmerge`](#binmerge), a CLI that preps
PS1 rips for the PSX tab. From a clone: `make install` puts stripped release
builds of both in `~/.bin`, `make build` stamped ones in `bin/` (gitignored);
`make` lists the other targets (run, test, lint, tidy, clean).

Flags: `--host` (default: auto-discover; env `PS3TOP_HOST`) · `--interval`
(15s, min 5s) · `--alarm` (80°C) · `--no-art` · `--version`. It's a TUI and
nothing else — no one-shot or scripting mode. `--help` prints the key
reference; `?` inside the app shows the same table as a screen (the footer
used to carry a keybar, which never fit every key at any width and so always
hid the ones worth discovering).

With no `--host`, ps3top finds the console itself: it sweeps the machine's
private IPv4 /24s (TCP :80, then a `GET /cpursx.ps3` that must answer with the
webMAN banner — anything else on port 80 is rejected), takes the first hit,
and caches it (`~/Library/Caches/ps3top/host` on macOS, XDG cache on Linux).
Later launches take the cached address on trust and go straight to the
dashboard — the first status poll *is* the verification, so there's no
probe-then-poll double request, and a console that's still off gets an
offline dashboard that comes alive when it boots rather than a refusal to
start. If that first poll fails, one background sweep runs per outage (the
header says `scanning 192.168.0.0/24 for webMAN…`); a console DHCP moved is
adopted and re-cached, a console that's simply off is retried on its old
address every 15s, and `r` asks for another sweep. A cold sweep takes a few
seconds; two consoles on one LAN → first answer wins, use `--host` to pick.

(Wake-on-LAN existed in 0.1.0 and was removed: the PS3 only listens for magic
packets with Remote Start on, that toggle is gated behind registering a
PSP/Vita, and on a Super Slim a standby wake is a cold boot where HEN — and
therefore webMAN and this tool — is dead until two controller presses anyway.
The DS3's PS button is the wake button.) Note:
after a cold boot, webMAN only appears once HEN is active — with HEN auto-boot
the dashboard recovers by itself, otherwise enable HEN from the XMB first.

Two things reach you when the window isn't in front, as a desktop
notification (`terminal-notifier` if installed, else `osascript`; `notify-send`
on Linux) on top of the bell and the red frame: a sensor crossing the alarm,
and the console going quiet in the middle of a game — a crash, a hard-off or
a lost network, all worth knowing from another window. A console going quiet
on the XMB is just switched off and says nothing. Same triggers as before,
one more sink, nothing periodic.

Metric scales (researched, values plain when healthy — color means attention):
- **Temps** (PSX-Place/GBAtemp consensus; webMAN's own fan target is 68°):
  60s–low 70s is normal PS3 gaming → plain; 70–77 yellow; 78+ red; alarm
  (flash + bell) at 80 by default — console overheat-warns/shuts down ~85.
- **FAN** (webMAN wiki: 40% manual recommended on HEN, SYSCON fine "if under
  70% in games"): plain <50, yellow 50–69, red 70+. In **manual** mode the
  speed is fixed and nothing ramps it for you, so a low percentage is fine on
  XMB and is the one way to cook a console mid-game; in **dynamic** mode
  webMAN ramps toward a target temperature you can raise past anything sane.
  The mode is rendered large on the thermal screen for exactly that reason.
- **HDD free** (dual-layer PS3 ISO ≤ ~45GB): plain ≥60G, yellow <60G, red <20G.
- **MEM** = free available memory (`meminfo.avail` in webMAN source) — ~1MB
  in-game is normal (the game owns the RAM): yellow <512K, red <256K.

A field the status page doesn't carry renders as a dim `—`, never as a number.
This matters more than it looks: `0` is a value webMAN legitimately reports
(`FAN SPEED: 0%`), so it can't double as "absent" — and a temperature of 0°
colors as perfectly healthy, meaning a webMAN markup change would otherwise
make a broken parser look like a cool console. Losing fields also raises a
flash: `status page: N of 6 fields not found`. It fires when the count *gets
worse*, not once per run — a steady break stays quiet after the first warning
instead of nagging every 15s, an outage and recovery into the same state don't
re-warn, and a page that later breaks harder still says so. The em dashes are
the standing signal; the flash is the explanation.

Header extras: game version (`· v01.15`) next to the running title — with
`→ 01.17 available` in yellow when Sony's title-update index lists a newer
patch (one HTTPS GET to `a0.ww.np.dl.playstation.net` per running title per
session, never to the console; its certificate is Sony's own SHA-1 CA, which
Go refuses, so the 2004–2037 root is pinned in `patch.go` and the leaf is
verified by hand) — and the syscon lifetime counters right of the metrics — `∞ 218d · 2709 boots ·
65 hard-off` (hard-off = power-ons minus clean power-offs).

## Sparklines

CPU, RSX and FAN carry an inline sparkline over the last ~240 polls (an hour at
the default cadence), bucketed so a full ring shows the whole hour rather than
the last two minutes. It costs nothing on the wire — the samples come from the
poll that was already happening.

The scale is the window's own min/max widened to a floor (10° for temps, 20
points for fan) and centered, so idle sensor jitter stays shallow and a steady
reading sits mid-height instead of pinned to the floor. A `↗` appears when the
last ~10 minutes moved by 3° or more (warn-colored, since a climb is the thing
you might have to act on); cooling gets a dim `↘`; anything smaller is noise
and gets nothing. Offline stretches are recorded as gaps, so the x axis stays
honest instead of silently compressing across an outage.

Fan-vs-temp correlation ("is SYSCON actually ramping") is shown rather than
told — the two plots sit side by side.

## Thermal screen (`t`) and fan control

`t` swaps the game list for a full-width temperature screen: CPU and RSX as
block-font readouts colored by their thermal band (readable across a room), a
multi-row plot of the same history at a much bigger zoom, and fan control.

```
                    CPU                         RSX
           ██████  ██  ██  ██████      ██████  ██████  ██████
               ██  ██  ██  ██  ██      ██      ██  ██  ██  ██
               ██  ██████  ██████      ██████  ██████  ██████
               ██      ██              ██  ██  ██  ██
               ██      ██              ██████  ██████
                    warm                       normal

 80 ┤· · · · · · · · · · · · · · · · · · · · · · · · · · · · · · · ·
    ┤                                                  ▄▄▄▄▄▄▄▄▄▄▄▄
 70 ┤· · · · · · · · · · · · · · ·▄▄▄▄▄█████ · · · · · · · · · · · ·
    ┤            ▄▄▄▄▄▄▄▄▄▄▄▄█████     ▄▄▄▄▄▄▄▄▄▄▄███████████
 56 ┼████████████                                                   
 └ 1h00m ago                              ── CPU  ── RSX     now ┘

  FAN  42%  SYSCON  ▁▂▂▂▃▃▃▄▄▄▅▅▅▆▆▆            +/− adjust · f mode
```

Two lines share one auto-scaled axis: color identifies the sensor, dotted
gridlines mark the 70 / 78 / alarm thresholds, and position carries the value.
(Recoloring the lines by band would have made the two sensors indistinguishable
exactly when it matters.) The axis pulls in a threshold sitting just above the
data so you can see the headroom rather than a full-height trace with nothing
to read it against. Under ~21 terminal rows the big readouts drop and the plot
takes their space; the fan row never drops, since it's where the controls are
documented.

`↑`/`↓` (or `+`/`−`) step the speed, `f` toggles the mode. Fan control uses
`/cpursx.ps3?up` · `?dn` · `?mode` — the links webMAN hangs on its own
temperature and fan readings. Those endpoints answer with the entire status
page, so **the response is the new state**: ps3top adopts it directly (one
request, not an action plus a re-poll) and flashes the delta that actually
happened — `fan 26% → 31%`, or `fan unchanged at 100%` when the console
declines.

That self-verifying report earned its keep immediately. The page renders one
slot three different ways depending on who is driving the fan:

| slot | mode | what `↑↓` moves |
|---|---|---|
| `<small>[Fan control: SYSCON]</small>` | console's own control | — |
| `(MAX: 86°C)` | webMAN ramps to hold a target temp | **the target** |
| *nothing* | manual fixed % | **the percentage** |

**`f` cannot get you back to SYSCON.** `?mode` only cycles webMAN's own
strategies (Lowest / Manual / Auto on `/setup.ps3`), and each of those sets
`fc.checked = 1` — SYSCON is that checkbox *off* ("Enable dynamic fan
control"), reachable only from the setup form. So leaving SYSCON is a one-way
door from in here, and `f` asks first when you're on it, labelled `f take over`
rather than `f mode`. Getting back: open `http://<ps3>/setup.ps3`, untick
**Enable dynamic fan control**, save. ps3top deliberately won't submit that
form itself — it's 143 fields and 103 checkboxes, and a partial GET submit
would silently clear every setting that isn't currently ticked.

Two consequences worth knowing. First, an absent `[Fan control: …]` marker is
not a parse failure — it's manual mode, so the mode is read from *which* marker
is present including neither (guarded on the fan percentage having parsed, so a
genuinely broken page still reports nothing rather than claiming manual).
Second, `?up` means different things in different modes: pressing it in dynamic
mode moved the target 86°→98° while the fan sat at 31%. A report written around
the percentage would have said "unchanged" while the console's thermal ceiling
quietly rose 12°.

So `fanReport` diffs **every** field a fan command can move — mode, percentage,
target — and names whatever actually changed. The keybar hint follows the mode
too (`↑↓ target` vs `↑↓ speed`), and the Fahrenheit copy of that line carries
its own `(MAX: 186°F)`, so the pattern requires `°C`.

Presses are **queued and sent one at a time**: webMAN has ~4 session slots, and
one GET per keypress turns a quick 24%→40% adjustment into a pile-up that reads
as an unresponsive UI. The queue is capped, cleared on error, and shown live in
the fan row (`⋯+3`) so a press registers before the console has answered.

The queue is **ordered, not netted**, and that matters precisely because `?up`
means different things in different modes. A step queued before a `?mode` was
aimed at the old mode: press `↑↑` in manual to spin the fan up, then `f`, and
sending the mode change first would leave those two presses raising the
*temperature ceiling* on a console you were trying to cool. Press order is
preserved exactly; only an immediate `↑`/`↓` reversal cancels, and only against
the tail, so it can never reach across a `?mode`.

For the same reason the fan keys **do nothing until a status has established
the mode**. With the mode unknown, `↑↓` can't say what they'd move and `f`
can't warn that it's a one-way door out of SYSCON — so they flash and wait
rather than guess.

No confirm on fan changes, deliberately: the in-game confirm elsewhere exists
because `/mount_ps3` can pull a running game's disc, which loses progress. A
fan step has no such failure mode — it's small, immediately visible, and undone
by the opposite key. The mode is rendered large because leaving the console in
manual at a low percentage is the one way to get this wrong.

When the header can't hold everything, it gives up the least live thing first:
full lifetime counters → `∞ Nd` only → sparklines → metric readings last.

## Thermal history (`h` inside `t`)

The one-hour rings can't answer "is it running hotter than in May", so every
minute of polling appends one line to `~/Library/Application Support/ps3top/
thermal.ndjson` (XDG data dir on Linux): the minute's peak CPU, RSX and fan.
Downsampled on the way in — ~60 bytes a minute, no compactor — and read only
when the page is opened. The page lays the log out by week, newest first:
hours logged, CPU and RSX peak / average, and how the fan split its minutes
across the header's colour bands (<50 · 50–69 · 70+), with the all-time
peaks dated above. The open minute is written on quit.

## Play history

The PS3 keeps no record of how long you've played anything. ps3top appends one
NDJSON line per session to `~/Library/Application Support/ps3top/history.ndjson`
(XDG data dir on Linux) — deliberately the data dir and not the cache dir,
since covers are disposable and this file isn't.

Session length comes from webMAN's own `PlayTime` field rather than a clock
ps3top starts, so a record is right even when ps3top joins a game already in
progress. That's also why there's no open-session sidecar file to recover after
a crash. A session that both starts and ends while ps3top isn't running is
simply never seen.

The catch is that `PlayTime` is *cumulative* for the whole game process: quit
ps3top mid-game and it writes the total so far; the next run writes the total
again when the game finally ends. Both lines describe one session. They're
recognised as one by their **start** time (`end − secs`, stable across
restarts) and merged on load, keeping the longest reading and counting it once.
The start time is derived rather than stored, so a log written by an older
ps3top is repaired the same way. Two genuinely separate plays still count
twice — only readings starting within five minutes of each other merge.

What it buys you: `s` toggles the list between alphabetical and
recently-played (so the handful of games you actually play float above the
alphabetical wall), rows carry a dim play total when the art panel is
hidden, the art panel gains
`38h12m · 9 sessions · last 2h ago`, and quitting a game flashes
`Borderlands™ 2 — 2h14m (peak 71°/74°)`.

## Keys

`↑↓`/`jk` move (smooth scroll, 2-row margin) · `pgup/pgdn`/`ctrl+u/d` page ·
`home`/`end` jump · `tab`/`←→`/`hl` switch console tab · `⏎` mount (already
mounted → launch) · `p` play = mount+launch · `u` eject · `/` fuzzy filter
(`esc` clears, scoped to the active tab) · `s` sort alphabetical → recently
played → largest ISO first (the size sort is the one that puts the size on
the row; the others give that width to the cover) · `?` help screen · `x`
quit the running game to the XMB · `X` restart it (both only
while a game is known to be running, both behind the red confirm) · `t`
thermal screen · `m` popup message on the TV · `r` refresh now
(status **and** game list) · `g` rescan the library · `S`/`R` shutdown/restart
(confirmed) · `q` quit.

`r` vs `g` matters when you've just FTP'd an ISO over: `mygames.xml` is static
until webMAN rebuilds it, so `r` shows you what webMAN already knows, while `g`
fires `/refresh.ps3?xmb` (the sMAN skin's own Refresh action) to make webMAN
re-scan the ISO folders and regenerate the XML, then reloads the list once
webMAN has had a beat to finish writing it. The rescan holds the same one-at-a-
time action gate as mount/eject — the console is genuinely busy while it scans —
and gets a 30s timeout instead of the usual 6s, since it walks every ISO
directory. Neither key loses your place: a reload re-finds the selected game by
identity instead of dumping the cursor back to row 1.

Inside the thermal screen: `+`/`−` (or `↑↓`) fan speed in manual mode, target
temperature in dynamic — the footer names whichever it currently is · `f` fan
mode · `h` the long-term history page (and back) · `r` refresh · `esc` (or
`t`, or `q`) back. `q` closes the screen rather than quitting ps3top
— quitting out of a subscreen on the key that everywhere else means "back" is
a nasty surprise mid-session.

The separator line is a tab strip, one tab per console present, in release
order — `⟨ PSX · 1 ⟩⟨ PS3 · 21 ⟩` — with cursor position (and filter state) on
the right, plus `recent` when that sort is active. Games are numbered per
console and sorted alphabetically; starts on the PS3 tab. `s` keeps the cursor
on whatever game it was on, so you can flip sorts without losing your place.

While a game is running, mount/play/launch/eject/power ask for a red confirm
first — deliberately, because the `/mount_ps3` silent variant bypasses webMAN's
own in-game mount protection.

They ask **equally hard when ps3top can't tell**: before the first status has
landed, and after any failed poll, the last reading is stale and "no game is
running" would be a guess. The prompt says the state is unknown rather than
naming a title it doesn't trust. Only a console known to be sitting on the XMB
skips the confirm.

## How it talks to the PS3 (and why it's gentle)

- One `GET /cpursx.ps3` per 15s on the XMB (webMAN's own web-UI refresh
  cadence; the page is ~6.6KB and carries every field server-rendered) and
  **every 30s while a game is running** — that's when the console is busiest,
  so that's where ps3top asks least — dropping back to 15s the moment a
  sensor reaches 70°, since alarm latency is the one reason to poll a running
  game fast. At the slow cadence each reading fills two sparkline slots, so
  the x axis stays one slot per 15s and a full ring stays an hour. Immediate
  extra poll after an action. Launch is that one poll, alone: the game list is fetched
  once the first status has answered (and again whenever the console comes
  back from an outage), so startup never has more than two connections open
  and a host that turns out not to be webMAN is never asked for a list. **One console-state request in flight at a time** — status
  polls, actions and fan commands share one gate, so a scheduled poll can't
  race a fan reply and overwrite the newer reading with the older one; a poll
  that comes due while busy is deferred, not dropped. Fresh connection each
  time (webMAN's server has ~4 session slots — never hold one). Timeouts: 2s
  dial, 6s per request, 10s client backstop (8s for games and covers).
  Offline → retry every 15s. Game-list and cover fetches are outside that gate
  by design — they're idempotent reads that don't carry console state.
- `--host` is parsed as a bare `host[:port]` authority and rejected otherwise:
  a value with a path silently prefixed every endpoint. Redirects to a
  different host are refused, so an action can't be aimed somewhere other than
  the address on screen.
- Every string webMAN sends — titles, firmware, paths — is stripped of terminal
  control sequences at the parser, before it can reach the screen. Covers are
  checked for a real PNG header and sane dimensions before being cached or
  handed to the terminal's image decoder.
- Game list from the **static** `mygames.xml` (`/dev_hdd0/xmlhost/game_plugin/`),
  fetched at startup and on `r`. It's XMB pseudo-XML (`<>value</>`), hence the
  regex parser. Its shape follows the **Add game-ID to game-title** box on
  `/setup.ps3`: ticked, the title ID is a `[BLES00455]` suffix on the title;
  unticked, it's in the `info` field as `hdd0/PS3ISO | BLES00455 | v01.90`,
  version included. Both parse, and the version shows in the art panel. `g` first asks webMAN to rebuild it
  (`/refresh.ps3?xmb`, 30s timeout — a rescan walks every ISO directory) and
  reloads 1.5s after the reply, in case the XML is still being written when
  webMAN answers.
- Library sizes: after every games load, one `GET /dev_hdd0/<folder>/` per
  ISO folder the library draws from (two for a PS3ISO + PSXISO library).
  webMAN's listing hangs the exact byte size on each row's mount link, keyed
  by the path it mounts by — `Game.Path` — so the join is by identity. The
  art panel gains `· 9.3G`, the header's HDD figure gains `~27 more` (free
  space over the median PS3 ISO), and two files of identical size are flashed
  once as probably the same ISO twice.
- Covers follow the covers source on `/setup.ps3`: **ICON0.PNG** is the ISO's
  own 320×176 icon (`/dev_hdd0/tmp/wmtmp/`), **MM COVERS** is multiMAN's
  folder of 260×300 JPEGs (`/dev_hdd0/game/BLES80608/USRDIR/covers/`, filled
  by whatever cover pack you installed). Either is fetched once per game and
  cached as PNG in `~/Library/Caches/ps3top/covers/` (the kitty transmission
  only carries PNG, so a JPEG is converted on the way in). **ONLINE COVERS**
  hasn't been captured; an icon that isn't a console path is refused rather
  than guessed at. Each cover is placed
  at its own aspect inside the art box rather than stretched to it, since
  kitty scales an image to exactly the cells it's given — a portrait cover
  stays portrait. The box takes every column the list doesn't need for
  its widest row (24–96 columns) and as many rows as the three text lines
  under it leave and the placement
  is shaped at the terminal's *measured* cell aspect (`TIOCGWINSZ` pixel
  fields; kitty and Ghostty fill them in, 1:2 assumed otherwise) — the
  terminal letterboxes inside a placement of the wrong shape, and that strip
  can't be centered from outside. A resize re-places the covers the terminal
  already holds without touching the console. The panel is centered in the
  list's height. A cached cover shows at once; an uncached
  one is requested only after the cursor has rested on its row for 150ms, and
  never more than one at a time — holding `j` through a fresh library used to
  open one connection per row it passed, at a server with ~4 slots. A cover
  webMAN can't serve is remembered for the session (`no cover` in the panel)
  instead of being re-requested on every visit; `r`/`g` retry it.
- Actions: `/mount_ps3/<path>` · `/mount_ps3/unmount` · `/play.ps3` (launch
  mounted) · `/play.ps3/<path>` (mount+launch) · `/popup.ps3/<text>` ·
  `/refresh.ps3?xmb` (library rescan) · `/xmb.ps3$exit` (quit game to XMB)
  · `/xmb.ps3$reloadgame` (restart game) · `/shutdown.ps3` · `/restart.ps3` ·
  `/cpursx.ps3?up|dn|mode` (fan; the reply is the new status page, so these
  cost one request and no follow-up poll).
- webMAN also runs a PS3MAPI text protocol on **port 7887** (temps, IDPS,
  process list, memory peek/poke) — unused here, but it's the door for any
  future tool that needs live memory access.

## Footprint

Meant to sit open all day: the renderer (bubbletea v2's cell-diffing one) is
capped at 30fps, a frame is only built on a keypress or a poll, and an idle
flush tick is a pointer-equal string compare and nothing else. The only
periodic work is the one 15s status GET plus its ~15 regex matches over
6.6KB. Cover PNGs are handed to the terminal and not retained in the model;
the *terminal* keeps each browsed cover (~225KB decoded) until ps3top exits
and deletes them all. Idle: ~12–14MB RSS (Go runtime floor), CPU rounds to
zero.

Sparklines and play history don't move any of that: the samples come from the
poll that already runs (three fixed 240-int rings, ~6KB total; the window each
frame reads is materialized once per poll, not once per read), and the history
file is appended once per finished session — a few hundred bytes a day at
worst, read once at startup.

## Cover art

Kitty graphics protocol in Unicode-placeholder mode (kitty & Ghostty; detected
via `TERM`/`TERM_PROGRAM`). Images are transmitted once with `q=2` and rendered
as placeholder cells, which survive bubbletea redraws. The transmission goes
out through `tea.Raw`, which bubbletea flushes from the same ticker as the
frame and ahead of it, and the panel only references an image once that write
has been sequenced — so the bytes are always in the terminal before the cells
that point at them. `art_test.go` drives a headless renderer and checks the
placeholder, its diacritics and the `38;5;id` foreground come out the other
side intact, since the cell renderer re-encodes everything it draws.
Unsupported terminal or `--no-art` → text-only, no errors.

## binmerge

A separate binary in this repo, because ps3top is a TUI and nothing else and
this is a batch job. It merges a cue sheet's bin files into a single bin and
writes a new cue with corrected offsets; `--split` reverses it.

That's the prep step the PSX tab needs. Redump dumps a multi-track PS1 disc as
one bin per track, and the PS3's PS1 emulator mounts one bin per disc — so a
freshly downloaded rip is a set webMAN will list and fail to boot. Merge it,
copy the pair to `/dev_hdd0/PSXISO/`, mount it from the PSX tab:

```sh
# a Redump rip: Game.cue + "Game (Track 1..N).bin"
binmerge Game.cue Game --psx -o ~/psx/out
# → out/Game.bin + out/Game.cue, ready for /dev_hdd0/PSXISO/
```

`--psx` is checks, not magic — the merge is byte-identical without it. It
refuses `--split` (a split set is a multi-file cue, the one thing the emulator
can't mount), warns when the sector size isn't 2352 (a cooked rip won't boot)
or when the basename carries `#`, `?` or `%` (webMAN mounts by URL path), and
prints the filenames to copy. Everything else about the tool is
console-agnostic and works on any cue.

Flags: `-s/--split` · `-o/--outdir` · `-f/--force` · `-n/--dry-run` · `--psx` ·
`-v/--verbose` · `-V/--version`. Positionals and flags interleave in either
order. `-n` validates and prints the cue it *would* write to stdout, touching
nothing — not even the output directory.

What it protects, since a merge reads the only copy of someone's disc:

- **inputs are never overwritten, `--force` included.** Paths aren't identity —
  a hardlink, or a case-only difference on the case-insensitive filesystem
  macOS ships by default, names the same inode under a name that compares
  unequal — so inodes are compared too, and the comparison happens against the
  resolved path (a `../NAME` basename only aliases an input once `..` is
  collapsed, and the output directory it escapes may not exist yet).
- **two outputs never land on one path.** Track filenames come from track
  numbers, so a cue repeating one would write the same file twice, lose the
  first track's sectors, and report success.
- **nothing partial survives a failure.** A merge or split that dies halfway —
  Ctrl-C included, which the copy loops watch for rather than leaving to the
  process — removes what it had written. A half-merged bin is the right length
  to look plausible and the wrong bytes to boot.
- **metadata is carried through.** `FLAGS`, `PREGAP`, `POSTGAP`, `ISRC`,
  `CATALOG`, disc and track `TITLE`/`PERFORMER`, `REM` — every line the parser
  doesn't act on comes out where it went in, on the right side of the indexes.
  The regexes are anchored, so a keyword inside a `REM` stays a comment.
- **non-UTF-8 cues decode as cp1252, not latin-1.** The rewritten cue goes out
  as UTF-8, so a wrong decode is permanent: 0x92 is a right single quote in a
  Windows-made cue, and latin-1 would bake a C1 control character into the
  title.

It descends from a Python tool of the same name (2.1.0, the version `-V` still
reports) that lived in its own repo until 2026-08-24; the Go port passes that
tool's full black-box suite unchanged.

## Source layout

`main.go` at the root is ten lines — the build stamp handed into
`ps3top.Run` — so `go install …/ps3top@latest` keeps working. `cmd/binmerge`
is the same shape for the other binary: flags and exit codes, with the work in
`internal/binmerge` (`cue.go` parse + generation, `validate.go` the sector and
output guards, `merge.go` the copying and its cleanup, `run.go` the
orchestration). The TUI is
`internal/ps3top`, one flat package deliberately: the tests live in-package
and exercise internals directly, and a split by concern would churn five
thousand lines of tests for nothing. The files carve it by concern instead
(the same map lives in `doc.go`):

| file | owns |
|---|---|
| `run.go` | flags, discovery hand-off, program start |
| `model.go` | the bubbletea model, its messages, list mechanics |
| `update.go` | `Init`/`Update`/`handleKey`, network commands, the action gate |
| `view.go` | every frame of the main screen, styles, shared formatters |
| `session.go` | what each poll feeds: metric samples, play sessions |
| `fan.go` | the serialized fan-command queue and its delta reports |
| `thermal.go` | the thermal screen (`t`) that fan.go's queue drives |
| `webman.go` | HTTP client + parsers for webMAN's pages |
| `discover.go` | the LAN sweep behind auto-discovery |
| `patch.go` | Sony's title-update index, behind a pinned root |
| `help.go` / `notify.go` | the key table behind `?` and `--help` / desktop notifications |
| `history.go` | the NDJSON play log behind the play totals |
| `spark.go` | metric rings + sparkline renderer |
| `thermallog.go` | the per-minute thermal log and its weekly fold |
| `art.go` / `cover.go` | kitty-graphics plumbing / cover fetch + cache |

And beside it, the annexed tool:

| file | owns |
|---|---|
| `cmd/binmerge/main.go` | flags, the positional/flag interleave, exit codes |
| `internal/binmerge/cue.go` | decode, parse, timestamps, both cue generators |
| `internal/binmerge/validate.go` | sector size, bin sizes, index bounds, output guards |
| `internal/binmerge/merge.go` | the copying, and the cleanup after a failed one |
| `internal/binmerge/run.go` | `Options`/`Run`, the logger, the `--psx` checks |

Each `*_test.go` matches its file, plus `state_test.go`/`edges_test.go` for
model transitions and end-to-end key→wire paths, and `ui_test.go` for whole
frames.

## Tests

`go test ./...` covers both binaries. binmerge's suite needs no fixtures on
disk — it builds a three-track Redump set in a temp dir per test — and pins the
round trip (merge then split gives back byte-identical bins and the original
cue), the metadata passthrough, every fatal path and the message it prints,
and the three output guards, including the hardlink and case-only aliases that
`--force` must still refuse. The port was landed against the Python original's
black-box suite driving the Go binary, which passed unchanged before a line of
Go test was written; that suite lives on in `internal/binmerge/*_test.go`.

The rest of this section is ps3top's own. `go test` runs the parsers against
the fixtures in `internal/ps3top/testdata/` (in-game
`cpursx.ps3`, `mygames.xml` with the game-ID-in-title setting on, and
`mygames_info_ids.xml` with it off plus MM COVERS). The fixtures are **synthetic**: byte-faithful to
real webMAN (sMAN skin) output in structure, but every game title, title ID,
and counter is invented (`MOCK…`/`DEMO…` IDs), chosen to exercise the sort
rules — word-before-number, roman numerals, `V2`-style numbers, embedded
digits, ™/® noise, newline titles, and the franchise-ID hint.

Beyond the parsers: `spark_test.go` pins the sparkline's scaling floor and
trend noise band, `history_test.go` covers session recording (including that
length comes from webMAN's counter, not ps3top's uptime) and the recency sort,
and `ui_test.go` renders whole frames in every state ps3top sits in all day —
asserting each line lands on exactly the terminal width, since a wrapped line
pushes everything below it down a row and shears the frame. It also pins the
header's degradation order, which is a judgment call worth not regressing.

`thermal_test.go` covers the glyph font and plot geometry, that the fan keys
bind only inside the thermal screen, that `q` there means "back" and not
"quit", and — via `httptest` — that the fan endpoints are addressed correctly
and their response is parsed as the new status. **Nothing in the suite talks to
a real console** — every fan test runs against `httptest`. Fan commands *have*
been fired at hardware by hand, which is where the three marker shapes and the
dynamic-mode `?up` behaviour were observed; it's the automated suite that never
touches the console.

`testdata/cpursx_xmb.html` is **derived** from the in-game fixture (game block
and PlayTime removed), not captured. It proves the parser handles an absent
game block — which feeds the mount/eject guard — but not that this is
byte-for-byte what a console emits on the XMB. Replace it with a scrubbed real
capture when one is to hand.

If webMAN gets updated and parsing breaks: capture the fresh pages to
somewhere **outside the repo** (`curl -s http://$PS3/cpursx.ps3` and
`…/dev_hdd0/xmlhost/game_plugin/mygames.xml`), diff the structure, and port
the structural changes into the synthetic fixtures. Never commit a raw
capture — `cpursx.ps3` embeds the console's PSID, IDPS, and MAC address (in
the hidden `id='ht'` span), and `mygames.xml` is your actual library.

Known gap: no XMB-state fixture yet (`InGame` detection keys off the `pid=`
marker; a capture with the console on XMB would pin the negative case).
