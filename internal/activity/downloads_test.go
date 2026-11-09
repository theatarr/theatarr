package activity

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/theatarr/theatarr/internal/store"
	"github.com/theatarr/theatarr/internal/store/storetest"
)

func TestDownloadsChangedBranches(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, storetest.Seed(t, filepath.Join(t.TempDir(), "activity.db")), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO title (id,kind,name,created_at,updated_at) VALUES (1,'movie','Fixture',1,1)`,
		`INSERT INTO version (id,title_id,policy_key,created_at,updated_at) VALUES (1,1,'movie',1,1)`,
		`INSERT INTO download (id,title_id,version_id,version_key,info_hash,release_title,indexer,state,created_at,updated_at)
		 VALUES (1,1,1,'movie','a','A','fixture','imported',1,99), (2,1,1,'movie','b','B','fixture','importing',1,99),
		 (3,1,1,'movie','c','C','fixture','importing',1,100), (4,1,1,'movie','d','D','fixture','queued',1,100)`,
		`INSERT INTO library_import (download_id,state,created_at,updated_at) VALUES (1,'linked',1,99),
		 (2,'partial',1,100),(3,'partial',1,101)`,
	} {
		if _, err := s.Writer().ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		filter Filter
		want   []int64
	}{
		{Filter{UpdatedSince: store.FromMillis(100)}, []int64{2, 3, 4}},
		{Filter{UpdatedSince: store.FromMillis(100), Limit: 2}, []int64{2, 3}},
		{Filter{UpdatedSince: store.FromMillis(100), IDs: []int64{1, 3, 4}, States: []string{"importing"}}, []int64{3}},
		{Filter{UpdatedSince: store.FromMillis(100), IDs: []int64{}}, nil},
	} {
		ds, err := Downloads(ctx, s.Reader(), tc.filter)
		if err != nil {
			t.Fatal(err)
		}
		var got []int64
		for _, d := range ds {
			got = append(got, d.ID)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%+v: got %v, want %v", tc.filter, got, tc.want)
		}
	}
	latest, err := LatestUpdate(ctx, s.Reader())
	if err != nil || latest.UnixMilli() != 101 {
		t.Fatalf("latest = %v, %v", latest, err)
	}
}
