package tmdb

import "time"

// ReleaseSchedule selects US releases, falling back to worldwide dates when a type has no US date.
// Premieres and TV broadcasts are not cinema or home releases; limited theatrical is a fallback for wide.
// theatrical_limited is availability's first cinema date (availability.FirstCinema): the earliest wide or limited
// cinema release in any country, present only when earlier than the chosen cinema date. TMDb's general release date
// (often a festival premiere) is never it.
func (m *Movie) ReleaseSchedule() map[string]time.Time {
	dates := map[string]time.Time{}
	us := map[string]time.Time{}
	for _, c := range m.ReleaseDates.Results {
		for _, r := range c.Releases {
			if r.Date.IsZero() {
				continue
			}
			kind := TypeName(r.Type)
			if kind != "theatrical" && kind != "theatrical_limited" && kind != "digital" && kind != "physical" {
				continue
			}
			d := r.Date.UTC().Truncate(24 * time.Hour)
			if old := dates[kind]; old.IsZero() || d.Before(old) {
				dates[kind] = d
			}
			if old := us[kind]; c.Country == "US" && (old.IsZero() || d.Before(old)) {
				us[kind] = d
			}
		}
	}
	first := dates["theatrical"]
	if l := dates["theatrical_limited"]; !l.IsZero() && (first.IsZero() || l.Before(first)) {
		first = l
	}
	for kind, date := range us {
		dates[kind] = date
	}
	if dates["theatrical"].IsZero() {
		dates["theatrical"] = dates["theatrical_limited"]
	}
	if dates["theatrical"].IsZero() {
		if d, err := time.Parse(time.DateOnly, m.ReleaseDate); err == nil {
			dates["theatrical"] = d
		}
	}
	delete(dates, "theatrical_limited")
	if !first.IsZero() && first.Before(dates["theatrical"]) {
		dates["theatrical_limited"] = first
	}
	return dates
}

// PrimaryCountry is the country of the movie's primary release (docs/design/ui.md "Release dates"), the dates the
// display shows: the US when TMDb lists a US cinema, digital or physical date, else the country with the earliest of
// those dates (the original release; the first country by code on a shared day); "" without any.
func (m *Movie) PrimaryCountry() string {
	best, bestDay := "", ""
	for _, c := range m.ReleaseDates.Results {
		for _, r := range c.Releases {
			switch TypeName(r.Type) {
			case "theatrical", "theatrical_limited", "digital", "physical":
			default:
				continue
			}
			if r.Date.IsZero() || c.Country == "" {
				continue
			}
			if c.Country == "US" {
				return "US"
			}
			day := r.Date.UTC().Format(time.DateOnly)
			if best == "" || day < bestDay || (day == bestDay && c.Country < best) {
				best, bestDay = c.Country, day
			}
		}
	}
	return best
}
