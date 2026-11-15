package web

import (
	"context"
	"database/sql"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

// upgradeRowsPage is a library of titles that all hold a file below their cutoff, some with a better release found.
func upgradeRowsPage(t testing.TB, titles int) *Web {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "theatarr.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	pol := &policy.Policy{Versions: map[string]*policy.VersionPolicy{}, Profiles: map[string]*policy.Profile{}}
	for _, kind := range []string{"movie", "series"} {
		remux := "Remux-2160p"
		if kind == "series" {
			remux = "Bluray-2160p Remux"
		}
		pol.Profiles[kind] = &policy.Profile{Qualities: []policy.QualityItem{{Name: remux}, {Name: "WEBDL-2160p"}, {Name: "WEBDL-1080p"}}, Cutoff: remux}
		pol.Versions[kind+"-a"] = &policy.VersionPolicy{Profile: kind}
		pol.Versions[kind+"-b"] = &policy.VersionPolicy{Profile: kind}
	}
	err = st.WriteTx(ctx, func(tx *sql.Tx) error {
		exec := func(q string, args ...any) {
			t.Helper()
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < titles; i++ {
			tid := int64(i + 1)
			kind := "movie"
			if i%3 == 0 {
				kind = "series"
			}
			exec(`INSERT INTO title(id,kind,name,sort_name,created_at,updated_at) VALUES(?,?,?,?,0,0)`, tid, kind, fmt.Sprintf("Title %03d", i), fmt.Sprintf("title %03d", i))
			for j, source := range []string{"a", "b"} {
				vid := tid*2 + int64(j)
				key := kind + "-" + source
				exec(`INSERT INTO version(id,title_id,policy_key,state,monitored,created_at,updated_at) VALUES(?,?,?,'filled',1,0,0)`, vid, tid, key)
				seasons := []int{-1}
				if kind == "series" {
					seasons = []int{1, 2}
				}
				for _, season := range seasons {
					fid := vid*10 + int64(season+1)
					exec(`INSERT INTO link(id,path,target,title_id,quality,created_at,updated_at) VALUES(?,?,?,?,'WEBDL-2160p',1000,1000)`, fid, fmt.Sprintf("/files/%d", fid), fmt.Sprintf("/zurg/%d", fid), tid)
					exec(`INSERT INTO link_version(link_id,version_id) VALUES(?,?)`, fid, vid)
					if kind == "series" {
						eid := tid*10 + int64(season)
						exec(`INSERT OR IGNORE INTO episode(id,title_id,season,number,name,created_at,updated_at) VALUES(?,?,?,1,'Episode',0,0)`, eid, tid, season)
						exec(`INSERT INTO episode_version_state(episode_id,version_id,state,monitored,updated_at) VALUES(?,?,'filled',1,0)`, eid, vid)
						exec(`INSERT INTO link_episode(link_id,episode_id) VALUES(?,?)`, fid, eid)
					}
				}
				if i%4 == 1 && j == 1 {
					exec(`INSERT INTO journal_decision(source,title_id,version_key,target,results,accepted,candidates,rejections,notes,created_at) VALUES('test',?,?,'movie',1,1,'[{"release":"Better","accepted":true,"upgrade":true}]','{}','',2000)`, tid, key)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Web{st: st, pol: pol, now: func() time.Time { return addNow }}
}

// The grid's upgrade state agrees with the title page's, row by row; the Upgrade found filter, its facet count and
// Home's Upgrade found row list exactly those titles; an old best_known link lists as if unfiltered.
func TestRowUpgradeStateBatch(t *testing.T) {
	t.Parallel()
	w := upgradeRowsPage(t, 60)
	ctx := context.Background()
	page, err := w.listTitles(ctx, TitleFilter{Limit: 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 60 {
		t.Fatalf("got %d rows", len(page.Items))
	}
	states := map[string]int{}
	for _, row := range page.Items {
		var vs []upgradeVersion
		for _, v := range row.Versions {
			vs = append(vs, upgradeVersion{id: v.ID, titleID: row.ID, key: v.Key, kind: row.Kind, state: v.State})
		}
		s, err := w.loadUpgrades(ctx, w.st.Reader(), vs, false)
		if err != nil {
			t.Fatal(err)
		}
		want := ""
		for _, v := range vs {
			if u := s.explain(v, 0); rowUpgradeRank[u.State] > rowUpgradeRank[want] {
				want = u.State
			}
		}
		if row.UpgradeState != want {
			t.Errorf("title %d: grid %s, single-title %s", row.ID, row.UpgradeState, want)
		}
		states[row.UpgradeState]++
		for _, role := range []struct {
			perms Perms
			want  string
		}{{PermsOf(auth.PermBrowse | auth.PermSeeFiles), want}, {PermsOf(auth.PermBrowse), ""}} {
			copy := row
			copy.Versions = append([]VersionBrief(nil), row.Versions...)
			if !role.perms.SeeFiles {
				redactRow(&copy)
			}
			if copy.UpgradeState != role.want {
				t.Errorf("title %d after redaction: %s, want %s", row.ID, copy.UpgradeState, role.want)
			}
		}
	}
	for _, state := range []string{"below_cutoff", "upgrade_available"} {
		if states[state] == 0 {
			t.Errorf("fixture did not exercise %s", state)
		}
	}
	for _, seeFiles := range []bool{true, false} {
		facets, err := w.titleFacets(ctx, TitleFilter{}, seeFiles)
		if err != nil {
			t.Fatal(err)
		}
		// Every title holds its file: available, whatever its quality against the cutoff.
		if facets.Availability["available"] != 60 {
			t.Errorf("see_files=%t facets: %v", seeFiles, facets.Availability)
		}
		if seeFiles && facets.Upgrades["found"] != states["upgrade_available"] {
			t.Errorf("upgrade facets %v, grid %v", facets.Upgrades, states)
		}
	}
	for up, want := range map[string]int{"found": states["upgrade_available"], "best_known": 60} {
		q := TitleGridFilter{Upgrade: up}
		if up == "best_known" {
			// As parsed from an old link: an unknown value (the removed best_known) is dropped.
			var err error
			if q, err = gridFilter(httptest.NewRequest("GET", "/titles?upgrade=best_known", nil)); err != nil || q.Upgrade != "" {
				t.Fatalf("upgrade=best_known: %+v %v", q, err)
			}
		}
		f, err := w.listTitles(ctx, TitleFilter{Limit: 60, TitleGridFilter: q})
		if err != nil || f.Total != want {
			t.Fatalf("upgrade=%s: %d rows, want %d: %v", up, f.Total, want, err)
		}
		for _, row := range f.Items {
			if up == "found" && row.UpgradeState != "upgrade_available" {
				t.Errorf("upgrade=found lists title %d (%s)", row.ID, row.UpgradeState)
			}
		}
	}
	picks, err := w.upgradePicks(ctx, w.st.Reader())
	if err != nil {
		t.Fatal(err)
	}
	// Home's upgrades row: the titles the grid says have an upgrade found, and no others.
	discover, err := w.discoverRow(ctx, w.st.Reader(), picks, 0, 60, "")
	if err != nil || len(discover.Items) == 0 || len(discover.Items) != states["upgrade_available"] {
		t.Fatalf("discover: %d rows, grid %d with an upgrade found: %v", len(discover.Items), states["upgrade_available"], err)
	}
	byID := map[int64]string{}
	for _, row := range page.Items {
		byID[row.ID] = row.UpgradeState
	}
	for _, row := range discover.Items {
		if row.UpgradeState != "upgrade_available" || row.UpgradeState != byID[row.ID] {
			t.Errorf("discover title %d: %s, grid %s", row.ID, row.UpgradeState, byID[row.ID])
		}
	}
}
