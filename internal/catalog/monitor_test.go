package catalog

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
)

// monitorFixture is a series with tv-2160p and episodes S00E01, S01E01-02 (aired, S01E01 with a file), S02E01 (aired
// 30 days ago), S02E02 (airs next week) and S03E01 (no air date).
func monitorFixture(t *testing.T) (st *store.Store, titleID, vid int64) {
	t.Helper()
	s, _ := setup(t)
	res := add(t, s, Input{Kind: policy.KindSeries, TvdbID: 5001, Title: "Fixture", Monitored: true,
		Versions: fixed("tv-2160p")})
	if err := s.Reader().QueryRow(`SELECT id FROM version WHERE title_id = ?`, res.TitleID).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	old := now.AddDate(-1, 0, 0).UnixMilli()
	for _, e := range []struct {
		id, season, number int
		air                any
	}{
		{1, 0, 1, old}, {2, 1, 1, old}, {3, 1, 2, old}, {4, 2, 1, now.AddDate(0, 0, -30).UnixMilli()},
		{5, 2, 2, now.AddDate(0, 0, 7).UnixMilli()}, {6, 3, 1, nil},
	} {
		if _, err := s.Writer().Exec(`INSERT INTO episode (id, title_id, season, number, name, air_date_utc_at,
			monitored, created_at, updated_at) VALUES (?, ?, ?, ?, '', ?, 1, 0, 0)`, e.id, res.TitleID, e.season,
			e.number, e.air); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Writer().Exec(`INSERT INTO episode_version_state (version_id, episode_id, monitored, state,
		updated_at) VALUES (?, 2, 1, 'cutoff_met', 0)`, vid); err != nil {
		t.Fatal(err)
	}
	return s, res.TitleID, vid
}

func monitoredEpisodes(t *testing.T, db *sql.DB, vid int64) string {
	t.Helper()
	rows, err := db.Query(`SELECT e.id FROM episode e JOIN episode_version_state s ON s.episode_id = e.id
		WHERE s.version_id = ? AND s.monitored = 1 ORDER BY e.id`, vid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := ""
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out += fmt.Sprint(id)
	}
	return out
}

// Each of Sonarr's monitor options on a series whose episodes are known (EpisodeMonitoredService.cs), with the
// season choices from its season rule.
func TestApplyMonitorEpisodes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		monitor, status string
		episodes        string // monitored episode ids
		seasons         string // season choices as stored
	}{
		{MonitorTypeAll, "", "23456", `{"0":false,"1":true,"2":true,"3":true}`},
		{MonitorTypeFuture, "continuing", "56", `{"0":false,"1":false,"2":true,"3":true}`},
		{MonitorTypeFuture, "ended", "56", `{"0":false,"1":false,"2":true,"3":true}`},
		{MonitorTypeMissing, "", "3456", `{"0":false,"1":true,"2":true,"3":true}`},
		{MonitorTypeExisting, "", "2", `{"0":false,"1":true,"2":false,"3":false}`},
		{MonitorTypeFirstSeason, "", "23", `{"0":false,"1":true,"2":false,"3":false}`},
		{MonitorTypeLastSeason, "", "6", `{"0":false,"1":false,"2":false,"3":true}`},
		{MonitorTypeLatestSeason, "", "6", `{"0":false,"1":false,"2":false,"3":true}`},
		{MonitorTypePilot, "", "2", `{"0":false,"1":false,"2":false,"3":false}`},
		{MonitorTypeRecent, "", "456", `{"0":false,"1":false,"2":true,"3":true}`},
		{MonitorTypeNone, "", "", `{"0":false,"1":false,"2":false,"3":false}`},
	} {
		t.Run(c.monitor+"/"+c.status, func(t *testing.T) {
			st, tid, vid := monitorFixture(t)
			ctx := context.Background()
			if _, err := st.Writer().Exec(`UPDATE series_monitor SET pending_monitor = 'pilot' WHERE version_id = ?`, vid); err != nil {
				t.Fatal(err)
			}
			if err := st.WriteTx(ctx, func(tx *sql.Tx) error {
				return ApplyMonitor(ctx, tx, tid, []string{"tv-2160p"}, c.monitor, nil, c.status, now)
			}); err != nil {
				t.Fatal(err)
			}
			if got := monitoredEpisodes(t, st.Reader(), vid); got != c.episodes {
				t.Errorf("episodes %q, want %q", got, c.episodes)
			}
			var pending string
			if err := st.Reader().QueryRow(`SELECT pending_monitor FROM series_monitor WHERE version_id = ?`, vid).Scan(&pending); err != nil || pending != "" {
				t.Fatalf("applied mode left pending %q: %v", pending, err)
			}
			var seasons string
			if err := st.Reader().QueryRow(`SELECT seasons FROM series_monitor WHERE version_id = ?`, vid).
				Scan(&seasons); err != nil || seasons != c.seasons {
				t.Errorf("seasons %s, want %s (%v)", seasons, c.seasons, err)
			}
		})
	}
}

// The specials options touch season 0 only; skip and unknown change nothing; an unknown name is an error.
func TestApplyMonitorSpecialsAndSkip(t *testing.T) {
	t.Parallel()
	st, tid, vid := monitorFixture(t)
	ctx := context.Background()
	apply := func(m string) error {
		return st.WriteTx(ctx, func(tx *sql.Tx) error {
			return ApplyMonitor(ctx, tx, tid, []string{"tv-2160p"}, m, nil, "", now)
		})
	}
	for _, m := range []string{MonitorTypeNone, MonitorTypeMonitorSpecials} {
		if err := apply(m); err != nil {
			t.Fatal(err)
		}
	}
	if got := monitoredEpisodes(t, st.Reader(), vid); got != "1" {
		t.Errorf("after monitorSpecials %q", got)
	}
	for _, m := range []string{MonitorTypeSkip, MonitorTypeUnknown, ""} {
		if err := apply(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := apply(MonitorTypeUnmonitorSpecials); err != nil {
		t.Fatal(err)
	}
	if got := monitoredEpisodes(t, st.Reader(), vid); got != "" {
		t.Errorf("after unmonitorSpecials %q", got)
	}
	if apply("everything") == nil {
		t.Error("an unknown monitor type accepted")
	}
}

// Before metadata arrives a new series keeps the request's season choices and persists the mode for the first refresh.
func TestAddMonitorWithoutEpisodes(t *testing.T) {
	t.Parallel()
	seasons := []Season{{0, false}, {1, true}, {2, true}, {3, true}}
	const want = `{"0":false,"1":true,"2":true,"3":true}`
	for _, c := range []struct{ monitor, status string }{
		{MonitorTypeAll, ""},
		{MonitorTypeMissing, ""},
		{MonitorTypeExisting, ""},
		{MonitorTypeFirstSeason, ""},
		{MonitorTypeLastSeason, ""},
		{MonitorTypeFuture, "continuing"},
		{MonitorTypeFuture, "ended"},
		{MonitorTypePilot, ""},
		{MonitorTypeSkip, ""},
		{"", ""},
	} {
		t.Run(c.monitor+"/"+c.status, func(t *testing.T) {
			st, _ := setup(t)
			res := add(t, st, Input{Kind: policy.KindSeries, TvdbID: 5002, Title: "New", Monitored: true,
				Versions: fixed("tv-2160p"), Seasons: seasons, Monitor: c.monitor, Status: c.status})
			var pending string
			if err := st.Reader().QueryRow(`SELECT pending_monitor FROM series_monitor m JOIN version v ON v.id = m.version_id WHERE v.title_id = ?`, res.TitleID).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			wantPending := c.monitor
			if wantPending == MonitorTypeSkip {
				wantPending = ""
			}
			if pending != wantPending {
				t.Errorf("pending %q, want %q", pending, wantPending)
			}
			var got string
			if err := st.Reader().QueryRow(`SELECT m.seasons FROM series_monitor m JOIN version v ON v.id = m.version_id
				WHERE v.title_id = ?`, res.TitleID).Scan(&got); err != nil || got != want {
				t.Errorf("seasons %s, want %s (%v)", got, want, err)
			}
		})
	}
}
