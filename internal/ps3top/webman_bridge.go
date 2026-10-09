package ps3top

// The webMAN client is the webman package, public since 2026-10-09 so ps3sync
// and ps3run can import it at a pinned version. What the TUI uses all over
// keeps its old name here — the types, the sentinels, the fan commands, the
// client and the discovery entry points — so the move cost the rest of this
// package no rewrite. What one or two places use names the package instead
// (webman.SortGames, webman.PatchLookup, webman.LocalNets): the two test
// seams in particular are read at call time, never copied.

import "github.com/lucasdaddiego/ps3top/webman"

type (
	Status = webman.Status
	Game   = webman.Game
	Client = webman.Client
)

const (
	unknown      = webman.Unknown
	statusFields = webman.StatusFields
	fanUp        = webman.FanUp
	fanDown      = webman.FanDown
	fanMode      = webman.FanMode
)

var consoleOrder = webman.ConsoleOrder

func NewClient(host string) *Client { return webman.NewClient(host) }

func discoverHost(cachePath string) (string, error)          { return webman.DiscoverHost(cachePath) }
func findConsole(announce func(nets string)) (string, error) { return webman.FindConsole(announce) }
func rememberHost(cachePath, host string)                    { webman.RememberHost(cachePath, host) }
func normalizeHost(h string) (string, error)                 { return webman.NormalizeHost(h) }
