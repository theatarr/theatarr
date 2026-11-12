package web

import (
	"context"
	"testing"
)

func TestUpgradeEvidenceDecodesOnlySelectedDecisions(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	for _, q := range []string{
		`INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates) VALUES
		 ('old',1,'movie-remux','movie',2000,'[{"release":"superseded","accepted":true,"upgrade":true}]'),
		 ('new',1,'movie-remux','movie',6000,'[]'),
		 ('unrelated',1,'movie-web','movie',7000,'[{"release":"other version","accepted":true,"upgrade":true}]'),
		 ('early-rss',1,'movie-remux','rss',5500,'[{"release":"old RSS","accepted":true,"upgrade":true}]'),
		 ('late-rss',1,'movie-remux','rss',6500,'[{"release":"late RSS","accepted":true,"upgrade":true,"score":42}]')`,
	} {
		e.exec(q)
	}
	vs := []upgradeVersion{{id: 1, titleID: 1, key: "movie-remux", kind: "movie", state: "filled"}}
	s, err := e.web.loadUpgrades(context.Background(), e.st.Reader(), vs, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.evidence) != 1 {
		t.Fatalf("loaded evidence for unrelated versions: %+v", s.evidence)
	}
	for _, r := range s.evidence[titleKey{1, "movie-remux"}] {
		if r.at < 6000 && len(r.cands) != 0 {
			t.Errorf("decoded superseded candidates at %d: %+v", r.at, r.cands)
		}
	}
	u := s.explain(vs[0], 0)
	if u.Next == nil || u.Next.Release != "late RSS" || u.LastSearch == nil || u.LastSearch.UnixMilli() != 6000 {
		t.Fatalf("selected evidence: %+v", u)
	}
	// LastSearch is independent of the file-time floor, even when no blob needs decoding.
	e.exec(`UPDATE version_file SET added_at = 9000 WHERE version_id = 1`)
	s, err = e.web.loadUpgrades(context.Background(), e.st.Reader(), vs, false)
	if err != nil {
		t.Fatal(err)
	}
	u = s.explain(vs[0], 0)
	if u.Next != nil || u.LastSearch == nil || u.LastSearch.UnixMilli() != 6000 {
		t.Fatalf("last search before current file: %+v", u)
	}
	for _, recs := range s.evidence {
		for _, r := range recs {
			if len(r.cands) != 0 {
				t.Errorf("decoded evidence older than the file: %+v", r)
			}
		}
	}
}

func TestUpgradeEvidenceSelectsEachEpisodesCoveringHistory(t *testing.T) {
	t.Parallel()
	e := newManageEnv(t, nil)
	e.exec(`INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates) VALUES
		('season',10,'tv-2160p','s01',6000,'[]'),
		('ep2',10,'tv-2160p','s01e02',6500,'[]'),
		('ep1',10,'tv-2160p','s01e01',7000,'[{"release":"Episode 1","accepted":true,"upgrade":true,"score":50,"episodes":["S01E01"]}]')`)
	// Many newer decisions for another season cannot hide the covering searches. There is no history limit.
	e.exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x < 1200)
		INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates)
		SELECT 'other-season',10,'tv-2160p','s02',8000+x,'[{"release":"Other season","accepted":true,"upgrade":true}]' FROM n`)
	e.exec(`INSERT INTO journal_decision (source,title_id,version_key,target,created_at,candidates) VALUES
		('rss',10,'tv-2160p','rss',6600,'[{"release":"Episode 2 fallback","accepted":true,"upgrade":true,"score":60,"episodes":["S01E02"]}]')`)
	v := upgradeVersion{id: 10, titleID: 10, key: "tv-2160p", kind: "series", state: "filled"}
	s, err := e.web.loadUpgrades(context.Background(), e.st.Reader(), []upgradeVersion{v}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, last int64
		release  string
	}{{101, 7000, "Episode 1"}, {102, 6500, "Episode 2 fallback"}} {
		u := s.explain(v, tc.id)
		if u.Next == nil || u.Next.Release != tc.release || u.LastSearch == nil || u.LastSearch.UnixMilli() != tc.last {
			t.Fatalf("episode %d: %+v", tc.id, u)
		}
	}
	for _, r := range s.evidence[titleKey{10, "tv-2160p"}] {
		if r.target == "s02" && len(r.cands) != 0 {
			t.Fatalf("decoded another season's candidates: %+v", r)
		}
	}
}
