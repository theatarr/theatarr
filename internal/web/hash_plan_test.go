package web

import (
	"strings"
	"testing"
)

func TestLinkedTargetHashPlan(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	rows, err := e.st.Reader().Query(`EXPLAIN QUERY PLAN `+uniqueLinkedTargetHash+`SELECT * FROM unique_target_hash`, `[1,2]`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"link_active_target (target=?)", "SEARCH vf2 USING COVERING INDEX version_file_path_content (path=?)", "SEARCH vf USING INDEX version_file_version (version_id=?)"} {
		if !strings.Contains(plan.String(), want) {
			t.Errorf("want %s; got %s", want, plan.String())
		}
	}
}
