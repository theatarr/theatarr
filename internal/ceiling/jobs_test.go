package ceiling

import (
	"context"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/jobs"
)

func TestComputeJobNewAndDueTitles(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	future := time.Now().Add(48 * time.Hour).UnixMilli()
	if _, err := st.Writer().Exec(`INSERT INTO title(id,kind,name,sort_name,created_at,updated_at) VALUES
 (3,'movie','New','New',0,0),(4,'movie','Later','Later',0,0);
 INSERT INTO version(id,title_id,policy_key,state,monitored,created_at,updated_at) VALUES
 (3,3,'remux','wanted',1,0,0),(4,4,'remux','wanted',1,0,0);
 INSERT INTO source_ceiling(title_id,season,tier,status,override_tier,override_at,override_until,next_check_at) VALUES
 (1,-1,'bd','override','bd',1,2,0);
 INSERT INTO source_ceiling(title_id,season,checked_at,next_check_at) VALUES(4,-1,123,?)`, future); err != nil {
		t.Fatal(err)
	}
	q := jobs.NewQueue(st)
	w := jobs.NewWorker(q, jobs.WorkerOptions{})
	RegisterJobs(w, st, testPolicy())
	id, _, err := q.Enqueue(ctx, JobKind, Payload{}, jobs.EnqueueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ran, err := w.RunOne(ctx); !ran || err != nil {
		t.Fatalf("ran %v: %v", ran, err)
	}
	if j, err := q.Get(ctx, id); err != nil || j.State != jobs.StateDone {
		t.Fatalf("compute job: %+v %v", j, err)
	}
	for _, vid := range []int{1, 2, 3} {
		var tier, status string
		if err := st.Reader().QueryRow(`SELECT json_extract(ceiling,'$.tier'),json_extract(ceiling,'$.status')
 FROM version_ceiling WHERE version_id=?`, vid).Scan(&tier, &status); err != nil || tier != "unknown" || status != "unknown" {
			t.Errorf("version %d: %s/%s %v", vid, tier, status, err)
		}
	}
	var checked int64
	if err := st.Reader().QueryRow(`SELECT checked_at FROM source_ceiling WHERE title_id=4`).Scan(&checked); err != nil || checked != 123 {
		t.Fatalf("title not due was recomputed: %d %v", checked, err)
	}
}
