package tvrefresh

import (
	"strings"
	"time"
	_ "time/tzdata" // zones in images without zoneinfo
)

// zones maps a network's country to the zone its airs time is in. TVDB names countries in ISO 3166-1 alpha-3
// ("usa"), TMDb in alpha-2 ("US"); both are here. A country with several zones takes the one most of its networks
// broadcast in (the US: Eastern, as SkyHook's air times are). Unknown countries are UTC.
var zones = map[string]string{
	"usa": "America/New_York", "us": "America/New_York",
	"can": "America/Toronto", "ca": "America/Toronto",
	"mex": "America/Mexico_City", "mx": "America/Mexico_City",
	"bra": "America/Sao_Paulo", "br": "America/Sao_Paulo",
	"arg": "America/Argentina/Buenos_Aires", "ar": "America/Argentina/Buenos_Aires",
	"gbr": "Europe/London", "gb": "Europe/London", "uk": "Europe/London",
	"irl": "Europe/Dublin", "ie": "Europe/Dublin",
	"fra": "Europe/Paris", "fr": "Europe/Paris",
	"deu": "Europe/Berlin", "de": "Europe/Berlin",
	"aut": "Europe/Vienna", "at": "Europe/Vienna",
	"che": "Europe/Zurich", "ch": "Europe/Zurich",
	"ita": "Europe/Rome", "it": "Europe/Rome",
	"esp": "Europe/Madrid", "es": "Europe/Madrid",
	"prt": "Europe/Lisbon", "pt": "Europe/Lisbon",
	"nld": "Europe/Amsterdam", "nl": "Europe/Amsterdam",
	"bel": "Europe/Brussels", "be": "Europe/Brussels",
	"dnk": "Europe/Copenhagen", "dk": "Europe/Copenhagen",
	"swe": "Europe/Stockholm", "se": "Europe/Stockholm",
	"nor": "Europe/Oslo", "no": "Europe/Oslo",
	"fin": "Europe/Helsinki", "fi": "Europe/Helsinki",
	"isl": "Atlantic/Reykjavik", "is": "Atlantic/Reykjavik",
	"pol": "Europe/Warsaw", "pl": "Europe/Warsaw",
	"cze": "Europe/Prague", "cz": "Europe/Prague",
	"tur": "Europe/Istanbul", "tr": "Europe/Istanbul",
	"rus": "Europe/Moscow", "ru": "Europe/Moscow",
	"ukr": "Europe/Kyiv", "ua": "Europe/Kyiv",
	"isr": "Asia/Jerusalem", "il": "Asia/Jerusalem",
	"ind": "Asia/Kolkata", "in": "Asia/Kolkata",
	"chn": "Asia/Shanghai", "cn": "Asia/Shanghai",
	"hkg": "Asia/Hong_Kong", "hk": "Asia/Hong_Kong",
	"twn": "Asia/Taipei", "tw": "Asia/Taipei",
	"jpn": "Asia/Tokyo", "jp": "Asia/Tokyo",
	"kor": "Asia/Seoul", "kr": "Asia/Seoul",
	"tha": "Asia/Bangkok", "th": "Asia/Bangkok",
	"phl": "Asia/Manila", "ph": "Asia/Manila",
	"aus": "Australia/Sydney", "au": "Australia/Sydney",
	"nzl": "Pacific/Auckland", "nz": "Pacific/Auckland",
	"zaf": "Africa/Johannesburg", "za": "Africa/Johannesburg",
}

// zoneFor returns the zone of the first known country (and its name), else UTC.
func zoneFor(countries ...string) (*time.Location, string) {
	for _, c := range countries {
		if name, ok := zones[strings.ToLower(strings.TrimSpace(c))]; ok {
			if loc, err := time.LoadLocation(name); err == nil {
				return loc, name
			}
		}
	}
	return time.UTC, "UTC"
}

// airUTC is an episode's air time in UTC: its air date (YYYY-MM-DD) at the series' airs time (HH:MM, midnight when
// unknown) in loc. nil for a missing or bad date.
func airUTC(date, airsTime string, loc *time.Location) *time.Time {
	d, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(date), loc)
	if err != nil {
		return nil
	}
	if t, err := time.Parse("15:04", strings.TrimSpace(airsTime)); err == nil {
		d = time.Date(d.Year(), d.Month(), d.Day(), t.Hour(), t.Minute(), 0, 0, loc)
	}
	u := d.UTC()
	return &u
}
