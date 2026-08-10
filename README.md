# ps3top

htop for a HEN'd PS3 — talks to webMAN MOD over HTTP. Status, temps, running
game, and mount/launch/eject of the game library, in one dashboard. Built
2026-08-09 against webMAN MOD 1.47.48 (sMAN skin) on FW 4.93 PS3HEN.

```
╭─ ps3top · 192.168.0.100 · online ─────── 4.93 HEN · wM 1.47.48q ─╮
│ CPU 58°  RSX 61°  FAN 26%  HDD 123G  MEM 1.1M   ∞ 100d · 1234 boots · 34 hard-off · up 1h24m │
│ ▶ Sample Game™ 2 v01.15 · MOCK30982                   play 1h23m │
╰──────────────────────────────────────────────────────────────────╯

── PSX · 1 │ PS3 · 21 ──────────────────────────────────── 16/21 ──
▌ 16  Sample Game™ 2            [MOCK30982]  ● mounted   [cover art]
   17  Sample Quest: The Beg…    [MOCK00103]
── ⏎ mount/launch · u eject · p play · ⇥ console · / filter · … ──
```

The status frame turns red while the temp alarm is active. Healthy metric
values render green; the active console tab is highlighted in the brand blue.

## Install / run

```sh
go install github.com/lucasdaddiego/ps3top@latest   # → ~/go/bin/ps3top
ps3top              # TUI (auto-discovers the console)
ps3top --once       # plain one-shot status (scripting/cron friendly)
```

From a clone: `make install` (or `make build` / `make test` / `make vet`).

Flags: `--host` (default: auto-discover; env `PS3TOP_HOST`) · `--interval`
(15s, min 5s) · `--alarm` (80°C) · `--no-art` · `--once` · `--version`.

With no `--host`, ps3top finds the console itself: it sweeps the machine's
private IPv4 /24s (TCP :80, then a `GET /cpursx.ps3` that must answer with the
webMAN banner — anything else on port 80 is rejected), takes the first hit,
and caches it (`~/Library/Caches/ps3top/host` on macOS, XDG cache on Linux).
Later launches probe the cached address only; if DHCP moved the console, the
stale probe fails and a fresh sweep runs. A cold sweep takes a few seconds;
two consoles on one LAN → first answer wins, use `--host` to pick.

(Wake-on-LAN existed in 0.1.0 and was removed: the PS3 only listens for magic
packets with Remote Start on, that toggle is gated behind registering a
PSP/Vita, and on a Super Slim a standby wake is a cold boot where HEN — and
therefore webMAN and this tool — is dead until two controller presses anyway.
The DS3's PS button is the wake button.) Note:
after a cold boot, webMAN only appears once HEN is active — with HEN auto-boot
the dashboard recovers by itself, otherwise enable HEN from the XMB first.

Metric scales (researched, values plain when healthy — color means attention):
- **Temps** (PSX-Place/GBAtemp consensus; webMAN's own fan target is 68°):
  60s–low 70s is normal PS3 gaming → plain; 70–77 yellow; 78+ red; alarm
  (flash + bell) at 80 by default — console overheat-warns/shuts down ~85.
- **FAN** (webMAN wiki: 40% manual recommended on HEN, SYSCON fine "if under
  70% in games"): plain <50, yellow 50–69, red 70+.
- **HDD free** (dual-layer PS3 ISO ≤ ~45GB): plain ≥60G, yellow <60G, red <20G.
- **MEM** = free available memory (`meminfo.avail` in webMAN source) — ~1MB
  in-game is normal (the game owns the RAM): yellow <512K, red <256K.

Header extras: game version (`· v01.15`) next to the running title, and the
syscon lifetime counters right of the metrics — `∞ 218d · 2709 boots ·
65 hard-off` (hard-off = power-ons minus clean power-offs).

## Keys

`↑↓`/`jk` move (smooth scroll, 2-row margin) · `pgup/pgdn`/`ctrl+u/d` page ·
`home`/`end` jump · `tab`/`←→`/`hl` switch console tab · `⏎` mount (already
mounted → launch) · `p` play = mount+launch · `u` eject · `/` fuzzy filter
(`esc` clears, scoped to the active tab) · `m` popup message on the TV ·
`g` reload games · `r` refresh now · `S`/`R` shutdown/restart (confirmed) ·
`q` quit.

The separator line is a tab strip, one tab per console present, in release
order — `⟨ PSX · 1 ⟩⟨ PS3 · 21 ⟩` — with cursor position (and filter state) on
the right. Games are numbered per console and sorted alphabetically; starts on
the PS3 tab.

While a game is running, mount/play/eject/power ask for a red confirm first —
deliberately, because the `/mount_ps3` silent variant bypasses webMAN's own
in-game mount protection.

## How it talks to the PS3 (and why it's gentle)

- One `GET /cpursx.ps3` per 15s (webMAN's own web-UI refresh cadence; the page
  is ~6.6KB and carries every field server-rendered). Immediate extra poll
  after an action. One request in flight max, fresh connection each time
  (webMAN's server has ~4 session slots — never hold one), 2s/4s timeouts.
  Offline → retry every 15s.
- Game list from the **static** `mygames.xml` (`/dev_hdd0/xmlhost/game_plugin/`),
  fetched at startup and on `g` only. It's XMB pseudo-XML (`<>value</>`), hence
  the regex parser.
- Covers (`/dev_hdd0/tmp/wmtmp/*.PNG`, 320×176) fetched once per game, cached in
  `~/Library/Caches/ps3top/covers/`.
- Actions: `/mount_ps3/<path>` · `/mount_ps3/unmount` · `/play.ps3` (launch
  mounted) · `/play.ps3/<path>` (mount+launch) · `/popup.ps3/<text>` ·
  `/shutdown.ps3` · `/restart.ps3`.
- webMAN also runs a PS3MAPI text protocol on **port 7887** (temps, IDPS,
  process list, memory peek/poke) — unused here, but it's the door for any
  future tool that needs live memory access.

## Footprint

Meant to sit open all day: the renderer is capped at 30fps, repaints only
happen on a keypress or a poll, and the only periodic work is the one 15s
status GET plus its ~15 regex matches over 6.6KB. `View` appends an
alternating invisible SGR reset to every frame — bubbletea v1's `flush()`
never resets its buffer after an unchanged frame, which would otherwise leave
the fps ticker re-allocating the whole frame 30×/s all day just to compare it
(details in the `View` comment). Cover PNGs are handed to the terminal and
not retained in the model; the *terminal* keeps each browsed cover (~225KB
decoded) until ps3top exits and deletes them all. Idle: ~12–14MB RSS (Go
runtime floor), CPU rounds to zero.

## Cover art

Kitty graphics protocol in Unicode-placeholder mode (kitty & Ghostty; detected
via `TERM`/`TERM_PROGRAM`). Images are transmitted once with `q=2` and rendered
as placeholder cells, which survive bubbletea redraws. Unsupported terminal or
`--no-art` → text-only, no errors.

## Tests

`go test` runs the parsers against the fixtures in `testdata/` (in-game
`cpursx.ps3`, `mygames.xml`). The fixtures are **synthetic**: byte-faithful to
real webMAN (sMAN skin) output in structure, but every game title, title ID,
and counter is invented (`MOCK…`/`DEMO…` IDs), chosen to exercise the sort
rules — word-before-number, roman numerals, `V2`-style numbers, embedded
digits, ™/® noise, newline titles, and the franchise-ID hint.

If webMAN gets updated and parsing breaks: capture the fresh pages to
somewhere **outside the repo** (`curl -s http://$PS3/cpursx.ps3` and
`…/dev_hdd0/xmlhost/game_plugin/mygames.xml`), diff the structure, and port
the structural changes into the synthetic fixtures. Never commit a raw
capture — `cpursx.ps3` embeds the console's PSID, IDPS, and MAC address (in
the hidden `id='ht'` span), and `mygames.xml` is your actual library.

Known gap: no XMB-state fixture yet (`InGame` detection keys off the `pid=`
marker; a capture with the console on XMB would pin the negative case).
