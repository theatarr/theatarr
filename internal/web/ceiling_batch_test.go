package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/ceiling"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func ceilingPage(t testing.TB, titles int) *Web {
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
		pol.Profiles[kind] = &policy.Profile{Qualities: []policy.QualityItem{{Name: remux}, {Name: "WEBDL-2160p"}}, Cutoff: remux}
		all := []policy.QualityItem{{Name: remux}, {Name: "WEBDL-2160p"}, {Name: "WEBDL-1080p"}}
		pol.Profiles[kind+"-shared"] = &policy.Profile{Qualities: all, Cutoff: remux}
		pol.Profiles[kind+"-owner"] = &policy.Profile{Qualities: all, Cutoff: "WEBDL-2160p"}
		pol.Versions[kind+"-local"] = &policy.VersionPolicy{Profile: kind}
		pol.Versions[kind+"-imported"] = &policy.VersionPolicy{Profile: kind + "-shared"}
		pol.Versions[kind+"-owner"] = &policy.VersionPolicy{Profile: kind + "-owner"}
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
			for j, source := range []string{"local", "imported"} {
				vid := tid*2 + int64(j)
				if source == "imported" && i%2 == 0 {
					source = "owner"
				}
				key := kind + "-" + source
				exec(`INSERT INTO version(id,title_id,policy_key,state,monitored,created_at,updated_at) VALUES(?,?,?,'filled',1,0,0)`, vid, tid, key)
				quality := "WEBDL-2160p"
				if i%8 == 4 {
					quality = "WEBDL-1080p"
				}
				seasons := []int{-1}
				if kind == "series" {
					seasons = []int{1, 2}
				}
				for _, season := range seasons {
					fid := vid*10 + int64(season+1)
					exec(`INSERT INTO version_file(id,version_id,title_id,instance,arr_file_id,path,quality,added_at) VALUES(?,?,?,?,?,?,?,1000)`, fid, vid, tid, kind, fid, fmt.Sprintf("/files/%d", fid), quality)
					if kind == "series" {
						eid := tid*10 + int64(season)
						exec(`INSERT OR IGNORE INTO episode(id,title_id,season,number,name,created_at,updated_at) VALUES(?,?,?,1,'Episode',0,0)`, eid, tid, season)
						exec(`INSERT INTO episode_version_state(episode_id,version_id,state,monitored,updated_at) VALUES(?,?,'filled',1,0)`, eid, vid)
						exec(`INSERT INTO version_file_episode(version_file_id,episode_id) VALUES(?,?)`, fid, eid)
					}
					if i%8 == 0 {
						continue // No ceiling yet.
					}
					info := ceiling.Info{Tier: "web_2160", Status: "known", Evidence: []ceiling.Evidence{{Tier: "web_2160", Kind: "arr_history"}}}
					switch i % 8 {
					case 1:
						info.Tier, info.Status = "unknown", "unknown"
					case 2:
						info.Status = "likely"
					case 3:
						info.Status = "override"
					case 6:
						if j == 1 && season != 2 {
							info.Tier = "unknown"
						}
					}
					raw, err := json.Marshal(info)
					if err != nil {
						t.Fatal(err)
					}
					exec(`INSERT INTO version_ceiling(version_id,season,effective_cutoff,ceiling,computed_at) VALUES(?,?,'WEB 2160p',?,0)`, vid, season, string(raw))
				}
				if i%8 == 5 && j == 1 {
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

func TestRowUpgradeStateBatch(t *testing.T) {
	t.Parallel()
	w := ceilingPage(t, 60)
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
		s.ceilings = map[ceilingKey]ceilingView{}
		s.profiles = map[int64]*profile.Profile{}
		want := ""
		for _, v := range vs {
			// The reference uses the original per-version reads, independently of the batch loader.
			err := scanAll(ctx, w.st.Reader(), `SELECT season,effective_cutoff,ceiling FROM version_ceiling WHERE version_id=?`, []any{v.id}, func(r *sql.Rows) error {
				var season int64
				var cutoff, raw string
				if err := r.Scan(&season, &cutoff, &raw); err != nil {
					return err
				}
				var info ceiling.Info
				if err := json.Unmarshal([]byte(raw), &info); err != nil {
					return err
				}
				s.ceilings[ceilingKey{v.id, season}] = ceilingView{&info, cutoff}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			p, err := ceiling.Profile(w.pol, v.key, v.kind)
			if err != nil {
				t.Fatal(err)
			}
			s.profiles[v.id] = p
			u := s.explain(v, 0)
			if rowUpgradeRank[u.State] > rowUpgradeRank[want] {
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
		}{{PermsOf(auth.PermBrowse | auth.PermSeeFiles | auth.PermManage), want}, {PermsOf(auth.PermBrowse | auth.PermSeeFiles), "below_cutoff"}, {PermsOf(auth.PermBrowse), ""}} {
			copy := row
			copy.Versions = append([]VersionBrief(nil), row.Versions...)
			redactRowFor(role.perms, &copy)
			expect := role.want
			if role.perms.SeeFiles && !canManage(role.perms) && want == "upgrade_available" {
				expect = want
			}
			if copy.UpgradeState != expect {
				t.Errorf("title %d after redaction: %s, want %s", row.ID, copy.UpgradeState, expect)
			}
		}
	}
	for _, state := range []string{"below_cutoff", "best_source_seen", "best_available", "higher_source_exists", "upgrade_available"} {
		if states[state] == 0 {
			t.Errorf("fixture did not exercise %s", state)
		}
	}
	for _, seeFiles := range []bool{true, false} {
		facets, err := w.titleFacets(ctx, TitleFilter{}, seeFiles)
		if err != nil {
			t.Fatal(err)
		}
		availability := "upgrade"
		if !seeFiles {
			availability = "available"
		}
		if facets.Availability[availability] != 60 {
			t.Errorf("see_files=%t facets: %v", seeFiles, facets.Availability)
		}
	}
	filtered, err := w.listTitles(ctx, TitleFilter{Limit: 60, TitleGridFilter: TitleGridFilter{Availability: "upgrade"}})
	if err != nil || len(filtered.Items) != 60 {
		t.Fatalf("upgrade filter: %d rows: %v", len(filtered.Items), err)
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

func BenchmarkRowUpgradeStateBatch(b *testing.B) {
	w := ceilingPage(b, 60)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := w.listTitles(context.Background(), TitleFilter{Limit: 60}); err != nil {
			b.Fatal(err)
		}
	}
}

func TestLoadCeilingsChunks(t *testing.T) {
	t.Parallel()
	w := ceilingPage(t, 451) // 902 versions cross both ceiling and profile chunk boundaries.
	ctx := context.Background()
	var vs []upgradeVersion
	for i := int64(1); i <= 451; i++ {
		kind := "movie"
		if (i-1)%3 == 0 {
			kind = "series"
		}
		for j, source := range []string{"local", "imported"} {
			vs = append(vs, upgradeVersion{id: i*2 + int64(j), titleID: i, key: kind + "-" + source, kind: kind, state: "filled"})
		}
	}
	s := &upgradeSet{}
	if err := w.loadCeilings(ctx, w.st.Reader(), vs, s); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := w.st.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM version_ceiling`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if len(s.ceilings) != count {
		t.Fatalf("loaded %d ceilings, want %d", len(s.ceilings), count)
	}
	for _, v := range vs {
		if (v.titleID-1)%8 != 0 && s.profiles[v.id] == nil {
			t.Errorf("version %d: profile missing across chunks", v.id)
		}
	}
}
