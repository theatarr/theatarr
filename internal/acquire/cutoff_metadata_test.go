package acquire

import (
	"database/sql/driver"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"modernc.org/sqlite"
)

var metadataCounterID atomic.Int64

// Count metadata rows read, not elapsed time: adding files must not multiply full-series loads.
func TestCutoffLoadsMetadataOnce(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprint(full), func(t *testing.T) {
			var titles, episodes atomic.Int64
			suffix := metadataCounterID.Add(1)
			titleFunc, episodeFunc := fmt.Sprintf("shadowed_title_%d", suffix), fmt.Sprintf("shadowed_episode_%d", suffix)
			for name, count := range map[string]*atomic.Int64{titleFunc: &titles, episodeFunc: &episodes} {
				if err := sqlite.RegisterScalarFunction(name, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
					count.Add(1)
					return args[0], nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			e := newEnv(t, true, nil)
			cutoffFixture(t, e)
			const count = 24
			for i := 0; i < count; i++ {
				id := 10000 + i
				e.exec(`INSERT INTO episode(id,title_id,season,number,name,runtime_minutes,created_at,updated_at) VALUES(?,10,3,?,'ep',45,0,0)`, id, i+1)
				e.exec(`INSERT INTO version_file(id,version_id,title_id,instance,arr_file_id,path,quality,scene_name) VALUES(?,201,10,'sonarr',?,?,'WEBDL-2160p','Show.S03E01.2160p.WEB-DL-GRP')`, id, id, fmt.Sprintf("/s/%d.mkv", id))
				e.exec(`INSERT INTO version_file_episode(version_file_id,episode_id) VALUES(?,?)`, id, id)
			}
			// Views wrap only the loaded metadata columns; all foreign keys keep pointing at the backing tables.
			for _, item := range []struct{ table, column, fn string }{{"title", "name", titleFunc}, {"episode", "runtime_minutes", episodeFunc}} {
				rows, err := e.st.Reader().Query(`SELECT name FROM pragma_table_info(?)`, item.table)
				if err != nil {
					t.Fatal(err)
				}
				var columns []string
				for rows.Next() {
					var name string
					if err := rows.Scan(&name); err != nil {
						t.Fatal(err)
					}
					col := `"` + name + `"`
					if name == item.column {
						col = item.fn + "(" + col + ") AS " + col
					}
					columns = append(columns, col)
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				rows.Close()
				e.exec(`ALTER TABLE ` + item.table + ` RENAME TO orig_` + item.table)
				e.exec(`CREATE VIEW ` + item.table + ` AS SELECT ` + strings.Join(columns, ",") + ` FROM orig_` + item.table)
			}
			e.pol.Versions["series"].PreferSeaDex = true
			if _, err := e.a.episodesBelowCutoff(e.ctx, "series", 201, 10, full); err != nil {
				t.Fatal(err)
			}
			if got := titles.Load(); got != 1 {
				t.Fatalf("LoadTitle metadata reads=%d, want 1", got)
			}
			if got := episodes.Load(); got > count+10 || got < count {
				t.Fatalf("episode runtime reads=%d for %d files; series must be loaded once", got, count)
			}
		})
	}
}
