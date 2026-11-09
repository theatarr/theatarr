package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/policy"
)

func recordRank(s SeaDexSnapshot, sc *policy.SeaDexScoring, hash string) int {
	for _, t := range ConfigureSeaDex(s, sc).Torrents {
		if t.InfoHash == hash {
			return t.Preference
		}
	}
	return -1
}

func recordMember(s SeaDexSnapshot, hash string) (SeaDexStanding, bool) {
	for _, t := range s.Torrents {
		if t.InfoHash == hash {
			return t, true
		}
	}
	return SeaDexStanding{}, false
}

func recordAnswer(partial bool, ts ...SeaDexStanding) SeaDexSnapshot {
	return SeaDexSnapshot{AniList: 1, Torrents: ts, Partial: partial}
}

// The retirement transitions, row by row: a complete omission starts the retirement with the rank held; a second complete omission retires
// only at 12 h or later; a partial answer clears a pending retirement and nothing else; reappearance clears it;
// time alone retires nothing.
func TestRetirementTransitionTable(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	ta := SeaDexStanding{InfoHash: a, Facts: &sdTorrent{}}
	tb := SeaDexStanding{InfoHash: b, Facts: &sdTorrent{}}
	at := time.Unix(100000, 0)
	listed, ok := ApplySeaDexObservation(nil, recordAnswer(false, ta, tb), at)
	if !ok || listed.Version != SeaDexRecordVersion {
		t.Fatalf("first complete answer: ok=%v version=%d", ok, listed.Version)
	}
	if _, ok := ApplySeaDexObservation(nil, recordAnswer(true, ta), at); ok {
		t.Fatal("a partial answer without a record started one")
	}
	held := recordRank(listed, nil, a)
	pending, _ := ApplySeaDexObservation(&listed, recordAnswer(false, tb), at)
	if m, _ := recordMember(pending, a); m.MissingSince != at.UnixMilli() || recordRank(pending, nil, a) != held {
		t.Fatalf("first omission: %+v rank %d, want pending with rank %d", m, recordRank(pending, nil, a), held)
	}
	// Time alone: the record ranks the pending torrent the same however late it is read.
	if got := recordRank(pending, nil, a); got != held {
		t.Fatalf("pending rank %d without a new observation, want %d", got, held)
	}
	early, _ := ApplySeaDexObservation(&pending, recordAnswer(false, tb), at.Add(SeaDexRetirementDelay-time.Millisecond))
	if m, _ := recordMember(early, a); !m.Pending() || recordRank(early, nil, a) != held {
		t.Fatalf("second omission before 12 h retired: %+v", m)
	}
	retired, _ := ApplySeaDexObservation(&early, recordAnswer(false, tb), at.Add(SeaDexRetirementDelay))
	if m, _ := recordMember(retired, a); m.Preference != 0 || m.IsBest || m.MissingSince == 0 {
		t.Fatalf("second omission at 12 h: %+v, want a zero-rank tombstone", m)
	}
	if len(retired.Sets) != 0 {
		t.Fatalf("a retired torrent's comparison set was kept: %v", retired.Sets)
	}
	// A partial answer lists the pending torrent: it is listed again; omitting another adds or retires nothing.
	cleared, _ := ApplySeaDexObservation(&early, recordAnswer(true, ta), at.Add(13*time.Hour))
	if m, _ := recordMember(cleared, a); m.MissingSince != 0 || m.Preference == 0 {
		t.Fatalf("partial presence did not clear the retirement: %+v", m)
	}
	if m, _ := recordMember(cleared, b); m.MissingSince != 0 || m.Preference == 0 {
		t.Fatalf("partial answer omitting %s changed it: %+v", b[:4], m)
	}
	c := strings.Repeat("c", 40)
	if added, _ := ApplySeaDexObservation(&early, recordAnswer(true, SeaDexStanding{InfoHash: c, Facts: &sdTorrent{}}), at.Add(13*time.Hour)); len(added.Torrents) != 2 {
		t.Fatalf("a partial answer added a torrent: %+v", added.Torrents)
	}
	// Reappearance in a complete answer clears it, and a tombstone listed again is revived.
	back, _ := ApplySeaDexObservation(&early, recordAnswer(false, ta, tb), at.Add(13*time.Hour))
	revived, _ := ApplySeaDexObservation(&retired, recordAnswer(false, ta, tb), at.Add(14*time.Hour))
	for name, s := range map[string]SeaDexSnapshot{"reappeared": back, "revived": revived} {
		if m, _ := recordMember(s, a); m.MissingSince != 0 || m.Preference == 0 || len(s.Sets) != 0 {
			t.Fatalf("%s: %+v sets %v", name, m, s.Sets)
		}
	}
	// An identical complete answer encodes exactly like the stored record: the caller writes nothing.
	same, _ := ApplySeaDexObservation(&listed, recordAnswer(false, tb, ta), at.Add(time.Hour))
	if x, y := recordJSON(t, listed), recordJSON(t, same); x != y {
		t.Fatalf("unchanged answer changed the record:\n%s\n%s", x, y)
	}
}

func recordJSON(t *testing.T, s SeaDexSnapshot) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Torrents first omitted by the same answer share one comparison set, stored once.
func TestOmittedTogetherShareOneSet(t *testing.T) {
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	at := time.Unix(100000, 0)
	listed, _ := ApplySeaDexObservation(nil, recordAnswer(false, SeaDexStanding{InfoHash: a, Facts: &sdTorrent{}},
		SeaDexStanding{InfoHash: b, Facts: &sdTorrent{}}, SeaDexStanding{InfoHash: c, IsBest: true, Facts: &sdTorrent{IsBest: true}}), at)
	next, _ := ApplySeaDexObservation(&listed, recordAnswer(false, SeaDexStanding{InfoHash: c, IsBest: true, Facts: &sdTorrent{IsBest: true}}), at.Add(time.Hour))
	if len(next.Sets) != 1 || len(next.Sets[at.Add(time.Hour).UnixMilli()]) != 3 {
		t.Fatalf("sets %+v, want one set of the three listed torrents", next.Sets)
	}
	if body := recordJSON(t, next); strings.Contains(body, "cohort") {
		t.Fatalf("version 2 record still writes per-torrent cohorts: %s", body)
	}
}

// Property: over random sequences of complete and partial answers, a torrent is retired only by a complete
// omission at least 12 h after the complete omission that started its retirement, with no presence between; until
// then it keeps the rank it had when first omitted (fixed settings); records survive encoding unchanged.
func TestTransitionProperty(t *testing.T) {
	hashes := []string{strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), strings.Repeat("d", 40)}
	torrent := func(i int, tag string) SeaDexStanding {
		return SeaDexStanding{InfoHash: hashes[i], Size: int64(i + 1), IsBest: i == 0, Facts: &sdTorrent{IsBest: i == 0, Tags: []string{tag}}}
	}
	sc := &policy.SeaDexScoring{Tags: map[string]int{"good": 5}}
	for seed := int64(1); seed <= 300; seed++ {
		r := rand.New(rand.NewSource(seed))
		now := time.Unix(100000, 0)
		var rec *SeaDexSnapshot
		type track struct {
			since time.Time
			rank  int
		}
		started := map[string]track{}
		for step := 0; step < 12; step++ {
			// Observations are at least a minute apart: a set is keyed by the millisecond of its first omission.
			now = now.Add(time.Duration(r.Intn(10))*time.Hour + time.Minute)
			var ts []SeaDexStanding
			for i := range hashes {
				if r.Intn(2) == 0 {
					tag := "plain"
					if r.Intn(3) == 0 {
						tag = "good"
					}
					ts = append(ts, torrent(i, tag))
				}
			}
			partial := r.Intn(4) == 0
			next, ok := ApplySeaDexObservation(rec, recordAnswer(partial, ts...), now)
			if !ok {
				if rec != nil || !partial {
					t.Fatalf("seed %d step %d: not applied", seed, step)
				}
				continue
			}
			listed := map[string]bool{}
			for _, x := range ts {
				listed[x.InfoHash] = true
			}
			for _, h := range hashes {
				m, held := recordMember(next, h)
				var before SeaDexStanding
				if rec != nil {
					before, _ = recordMember(*rec, h)
				}
				tr, pending := started[h]
				switch {
				case listed[h] && (!partial || before.Pending()):
					if m.MissingSince != 0 || m.Preference == 0 {
						t.Fatalf("seed %d step %d: listed torrent not listed: %+v", seed, step, m)
					}
					delete(started, h)
				case !held || partial || rec == nil:
				case before.MissingSince == 0 && before.Preference > 0:
					if !m.Pending() {
						t.Fatalf("seed %d step %d: complete omission did not start a retirement: %+v", seed, step, m)
					}
					started[h] = track{now, recordRank(*rec, sc, h)}
				case pending && m.Preference == 0:
					if now.Sub(tr.since) < SeaDexRetirementDelay {
						t.Fatalf("seed %d step %d: retired %v after the first omission", seed, step, now.Sub(tr.since))
					}
					delete(started, h)
				case pending && now.Sub(tr.since) >= SeaDexRetirementDelay:
					t.Fatalf("seed %d step %d: still pending %v after the first omission: %+v", seed, step, now.Sub(tr.since), m)
				}
				if tr, ok := started[h]; ok {
					if got := recordRank(next, sc, h); got != tr.rank {
						t.Fatalf("seed %d step %d: pending rank %d, want %d held from the first omission", seed, step, got, tr.rank)
					}
				}
			}
			var decoded SeaDexSnapshot
			if err := json.Unmarshal([]byte(recordJSON(t, next)), &decoded); err != nil {
				t.Fatal(err)
			}
			if recordJSON(t, decoded) != recordJSON(t, next) {
				t.Fatalf("seed %d step %d: record changed through encoding", seed, step)
			}
			rec = &next
		}
	}
}

// A settings change while a retirement is pending ranks the pending torrent under the new settings, within the set
// it was last listed with: the torrent listed since does not take part.
func TestSettingsChangeDuringPendingRetirement(t *testing.T) {
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	at := time.Unix(100000, 0)
	listed, _ := ApplySeaDexObservation(nil, recordAnswer(false,
		SeaDexStanding{InfoHash: a, Facts: &sdTorrent{Tags: []string{"x"}}},
		SeaDexStanding{InfoHash: b, Facts: &sdTorrent{Tags: []string{"y"}}}), at)
	pending, _ := ApplySeaDexObservation(&listed, recordAnswer(false,
		SeaDexStanding{InfoHash: b, Facts: &sdTorrent{Tags: []string{"y"}}},
		SeaDexStanding{InfoHash: c, Facts: &sdTorrent{Tags: []string{"x", "z"}}}), at.Add(time.Hour))
	preferX := &policy.SeaDexScoring{Tags: map[string]int{"x": 10}}
	preferY := &policy.SeaDexScoring{Tags: map[string]int{"y": 10}}
	if got := recordRank(pending, preferX, a); got != 3 {
		t.Fatalf("pending torrent under settings preferring its tag ranked %d, want 3 (the newcomer is not in its set)", got)
	}
	if got := recordRank(pending, preferY, a); got != 1 {
		t.Fatalf("pending torrent under settings preferring another tag ranked %d, want 1", got)
	}
	if got := recordRank(pending, preferX, c); got != 3 {
		t.Fatalf("listed newcomer ranked %d among the listed torrents, want 3", got)
	}
	excluded := &policy.SeaDexScoring{ExcludeTags: []string{"x"}}
	if got := recordRank(pending, excluded, a); got != 0 {
		t.Fatalf("pending torrent with a now-excluded tag ranked %d, want 0", got)
	}
}

// Version 1 rows (cohort facts, cohort hashes, or neither) convert to version 2 without changing any rank, and
// conversion is idempotent.
func TestConvertsVersion1Rows(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	since := time.Unix(100000, 0).UnixMilli()
	rows := map[string]string{
		"cohort facts":  fmt.Sprintf(`{"anilist":1,"torrents":[{"info_hash":"%s","preference":1,"missing_since":%d,"facts":{},"cohort":["%s","%s"],"cohort_facts":[{"info_hash":"%s","facts":{}},{"info_hash":"%s","facts":{}}]},{"info_hash":"%s","preference":2,"is_best":true,"facts":{"isBest":true}}]}`, a, since, a, b, a, b, b),
		"cohort hashes": fmt.Sprintf(`{"anilist":1,"torrents":[{"info_hash":"%s","preference":1,"missing_since":%d,"facts":{},"cohort":["%s","%s"]},{"info_hash":"%s","preference":1,"facts":{}}]}`, a, since, a, b, b),
		"no cohort":     fmt.Sprintf(`{"anilist":1,"torrents":[{"info_hash":"%s","preference":1,"missing_since":%d,"facts":{}},{"info_hash":"%s","preference":1,"facts":{}}]}`, a, since, b),
		"legacy":        fmt.Sprintf(`{"anilist":1,"torrents":[{"info_hash":"%s","preference":3,"missing_since":%d},{"info_hash":"%s","preference":2}]}`, a, since, b),
	}
	for name, body := range rows {
		t.Run(name, func(t *testing.T) {
			var v1 SeaDexSnapshot
			if err := json.Unmarshal([]byte(body), &v1); err != nil {
				t.Fatal(err)
			}
			v2 := upgradeRecord(v1)
			if v2.Version != SeaDexRecordVersion || strings.Contains(recordJSON(t, v2), "cohort") {
				t.Fatalf("not converted: %s", recordJSON(t, v2))
			}
			if !reflect.DeepEqual(upgradeRecord(v2), v2) {
				t.Fatal("conversion is not idempotent")
			}
			for _, h := range []string{a, b} {
				if x, y := recordRank(v1, nil, h), recordRank(v2, nil, h); x != y {
					t.Fatalf("%s: rank %d before conversion, %d after", h[:4], x, y)
				}
			}
			if name == "legacy" {
				if m, _ := recordMember(v2, b); m.Preference == 0 || m.Facts != nil || len(v2.Sets) != 0 {
					t.Fatalf("legacy member changed: %+v sets %v", m, v2.Sets)
				}
			} else if len(v2.Sets[since]) == 0 {
				t.Fatalf("pending torrent has no comparison set: %+v", v2.Sets)
			}
		})
	}
}

// Stored conversion: the first observation writes a version 1 row back as version 2 with the omission time kept;
// an interrupted write leaves the version 1 row, which the rerun converts the same way; a converted row is not
// rewritten by an unchanged answer.
func TestStoredConversionInterruptedAndRerun(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	st := openStore(t)
	ctx := context.Background()
	if _, err := st.Writer().Exec(`INSERT INTO title(id,kind,name,created_at,updated_at) VALUES(1,'movie','Film',0,0); INSERT INTO title_external_id VALUES(1,'anilist','1')`); err != nil {
		t.Fatal(err)
	}
	since := time.Unix(100000, 0).UnixMilli()
	v1 := fmt.Sprintf(`{"anilist":1,"torrents":[{"info_hash":"%s","preference":1,"missing_since":%d,"facts":{},"cohort":["%s","%s"]},{"info_hash":"%s","preference":1,"facts":{}}]}`, a, since, a, b, b)
	if _, err := st.Writer().Exec(`INSERT INTO metadata_cache(source,key,status,body,fetched_at,expires_at) VALUES('seadex-standing','1',200,?,7,9999999999999)`, v1); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100000, 0).Add(time.Hour)
	ev := NewEvidence(EvidenceConfig{Store: st, Policy: testPolicy(), Now: func() time.Time { return now }})
	answer := []SeaDexSnapshot{{AniList: 1, Torrents: []SeaDexStanding{{InfoHash: b, Facts: &sdTorrent{}}}}}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := ev.observeSeaDex(cancelled, answer); err == nil {
		t.Fatal("cancelled observation reported success")
	}
	var body string
	var fetched int64
	if err := st.Reader().QueryRow(`SELECT body, fetched_at FROM metadata_cache WHERE source='seadex-standing' AND key='1'`).Scan(&body, &fetched); err != nil || body != v1 {
		t.Fatalf("interrupted conversion changed the row: %v %s", err, body)
	}
	for run := 0; run < 2; run++ {
		if err := ev.observeSeaDex(ctx, answer); err != nil {
			t.Fatal(err)
		}
		got := storedStanding(t, st.Reader(), "1")
		m, _ := recordMember(got, a)
		if got.Version != SeaDexRecordVersion || m.MissingSince != since || len(got.Sets[since]) != 2 || m.Cohort != nil {
			t.Fatalf("run %d: %+v", run, got)
		}
		if err := st.Reader().QueryRow(`SELECT fetched_at FROM metadata_cache WHERE source='seadex-standing' AND key='1'`).Scan(&fetched); err != nil {
			t.Fatal(err)
		}
		if run == 0 {
			now = now.Add(time.Hour) // the rerun's write time would show in fetched_at
			continue
		}
		if fetched != time.Unix(100000, 0).Add(time.Hour).UnixMilli() {
			t.Fatalf("an unchanged answer rewrote the converted row (fetched_at %d)", fetched)
		}
	}
}
