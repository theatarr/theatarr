package nowplaying

import (
	"path"
	"regexp"
	"strings"
	"time"
)

// zurg's log lines about streaming (its own wording, matched by our patterns). Only lines about a file someone is
// playing are kept (and account cooldowns, which name no file), for eventTTL.
const eventTTL = 10 * time.Minute

// Event kinds.
const (
	kindTransfer = "transfer" // Large transfer: which account served the file
	kindStarved  = "starved"  // a delivery node sent too little; zurg refuses the file for a while
	kindLink     = "link"     // download link broken, re-unrestricting
	kindBroken   = "broken"   // the copy on an account turned broken, or the file is broken (404)
	kindLimited  = "limited"  // provider rate limit while unrestricting
	kindCooldown = "cooldown" // an account is cooling down
)

var zurgPatterns = []struct {
	kind string
	re   *regexp.Regexp // file and account groups, by name
}{
	{kindTransfer, regexp.MustCompile(`Large transfer: \S+ \S+ of (?P<file>.+?) from (?P<account>\S+) for `)},
	{kindStarved, regexp.MustCompile(`Cut a read of (?P<file>.+?) on (?P<account>\S+) for `)},
	{kindBroken, regexp.MustCompile(`Source state changed: (?P<file>.+?) on (?P<account>\S+) \[ok_file → broken_file\]`)},
	{kindBroken, regexp.MustCompile(`is broken; read retry window elapsed`)},
	{kindLink, regexp.MustCompile(`Download link broken \(attempt \d+/\d+\), re-unrestricting(?: file (?P<file>.+?))?\s*$`)},
	{kindLimited, regexp.MustCompile(`Provider rate limited while unrestricting (?P<file>.+?);`)},
	{kindCooldown, regexp.MustCompile(`Account (?P<account>\S+) is cooling down`)},
}

// zurgEvent is one matched line.
type zurgEvent struct {
	At      time.Time
	Kind    string
	File    string // base name; "" for an account event
	Account string
}

// parseZurgLine matches a log line. A line whose pattern names no file is attributed to the first of files (base
// names being played) it contains; one about no played file is dropped, except an account cooldown.
func parseZurgLine(line string, files []string, at time.Time) (zurgEvent, bool) {
	line = strings.TrimRight(line, "\r\n")
	for _, p := range zurgPatterns {
		m := p.re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ev := zurgEvent{At: at, Kind: p.kind}
		if i := p.re.SubexpIndex("file"); i > 0 && m[i] != "" {
			ev.File = path.Base(m[i])
		}
		if i := p.re.SubexpIndex("account"); i > 0 {
			ev.Account = m[i]
		}
		if ev.Kind == kindCooldown {
			return ev, true
		}
		for _, f := range files {
			if f != "" && (ev.File == f || (ev.File == "" && strings.Contains(line, f))) {
				ev.File = f
				return ev, true
			}
		}
		return zurgEvent{}, false
	}
	return zurgEvent{}, false
}

// issue is the most telling provider problem among a file's recent events: what the health verdict names.
func issue(evs []zurgEvent, file, servedBy string) string {
	var out string
	for _, e := range evs {
		var s string
		switch {
		case e.Kind == kindCooldown && e.Account != "" && e.Account == servedBy:
			s = e.Account + " is cooling down"
		case e.File != file:
			continue
		case e.Kind == kindStarved:
			s = "zurg cut a starved read on " + e.Account + " (slow delivery node)"
		case e.Kind == kindBroken && e.Account != "":
			s = "the copy on " + e.Account + " turned broken"
		case e.Kind == kindBroken:
			s = "zurg says the file is broken"
		case e.Kind == kindLink:
			s = "the download link broke; zurg re-unrestricts it"
		case e.Kind == kindLimited:
			s = "the provider rate-limits unrestricting"
		}
		if s != "" {
			out = s // the newest wins
		}
	}
	return out
}
