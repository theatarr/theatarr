package tmdb

import "time"

// ReleaseSchedule selects US releases, falling back to worldwide dates when a type has no US date.
// Premieres and TV broadcasts are not cinema or home releases; limited theatrical is a fallback for wide.
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
	return dates
}
