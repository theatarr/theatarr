package torrents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/provider"
)

// The report reads the record and calls no provider; an account never read is not an empty one; Refresh runs the
// provider sync, and a failed refresh keeps the account's record with its error.
func TestReportFromRecord(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	old := e.now.Add(-72 * time.Hour)
	e.rd.list = []provider.Torrent{
		tor("1", hA, "Movie.A.2020.1080p.WEB-DL", 100, old, provider.StateDownloaded),
		tor("2", hA, "Movie.A.2020.1080p.WEB-DL", 100, old, provider.StateDownloaded), // the same hash again
	}
	e.tb.list = []provider.Torrent{}
	byKey := func(r *Report) map[string]ProviderReport {
		out := map[string]ProviderReport{}
		for _, p := range r.Providers {
			out[p.Provider] = p
		}
		return out
	}

	r, err := e.m.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p := byKey(r)["rd"]; p.OK || !p.NeverRead || p.AsOf != nil || len(p.Torrents) != 0 {
		t.Fatalf("never read: %+v", p)
	}
	if err := e.m.Refresh(ctx); !errors.Is(err, ErrNoSync) {
		t.Fatalf("no sync, no refresh: %v", err)
	}

	e.m.Syncer = &provider.Syncer{Store: e.st, Providers: e.m.Providers, Configured: []string{"rd", "tb"},
		Now: func() time.Time { return e.now }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := e.m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	e.rd.lists, e.tb.lists = 0, 0
	e.rd.list = append(e.rd.list, tor("3", hB, "Movie.B.2021.1080p.WEB-DL", 50, old, provider.StateDownloaded))
	r, err = e.m.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if e.rd.lists != 0 || e.tb.lists != 0 || e.rd.reads != 0 || e.tb.reads != 0 {
		t.Fatalf("the report calls no provider: rd %d/%d tb %d/%d", e.rd.lists, e.rd.reads, e.tb.lists, e.tb.reads)
	}
	rd, tb := byKey(r)["rd"], byKey(r)["tb"]
	if !rd.OK || rd.NeverRead || rd.AsOf == nil || !rd.AsOf.Equal(e.now) || rd.AccountTorrents != 2 ||
		rd.UniqueHashes != 1 || len(rd.Torrents) != 2 || len(r.DuplicateHash) != 1 {
		t.Fatalf("rd from the record (both torrents of the hash): %+v", rd)
	}
	if !tb.OK || tb.NeverRead || tb.AsOf == nil || tb.AccountTorrents != 0 || tb.Error != "" {
		t.Fatalf("an empty account is read: %+v", tb)
	}

	first := e.now
	e.now = e.now.Add(time.Hour)
	e.tb.listErr = errors.New("tb: HTTP 503")
	if err := e.m.Refresh(ctx); err == nil {
		t.Fatal("tb failed")
	}
	r, err = e.m.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rd, tb = byKey(r)["rd"], byKey(r)["tb"]
	if rd.AccountTorrents != 3 || !rd.AsOf.Equal(e.now) || rd.Error != "" {
		t.Fatalf("rd read again: %+v", rd)
	}
	if !tb.OK || !tb.AsOf.Equal(first) || !strings.Contains(tb.Error, "HTTP 503") || tb.ErrorAt == nil {
		t.Fatalf("tb keeps its record and says why: %+v", tb)
	}
}

// A URL-encoded TorBox name is shown and parsed decoded; the raw name is kept beside it, and the usage matches the
// provider's own name. Sources and Saved inventory get the same stored usage as the report.
func TestRecordNamesAndUsage(t *testing.T) {
	t.Parallel()
	e, _ := usageFixture(t)
	ctx := context.Background()
	const raw = "[SoM]%20Dragon%20Ball%20Z%20-%20COMPLETE%20(001-291)"
	e.tb.list = append(e.tb.list, tor("91", hH, raw, 10, e.now.Add(-72*time.Hour), provider.StateDownloaded))
	e.synced("tb")
	e.content(hH, 1)
	r, err := e.report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := find(r, "tb", "91")
	if got == nil || got.Name != "[SoM] Dragon Ball Z - COMPLETE (001-291)" || got.RawName != raw ||
		strings.Contains(got.Parsed, "%20") {
		t.Fatalf("decoded for display: %+v", got)
	}
	if DisplayName("100%25 Real.mkv") != "100%25 Real.mkv" || DisplayName("Plain.Name") != "Plain.Name" {
		t.Fatal("only an encoded name without spaces is decoded")
	}
	db := e.st.Reader()
	for _, p := range r.Providers {
		rec, err := ReadProviderRecord(ctx, db, p.Provider)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Usage != p.Usage || rec.AccountTorrents != p.AccountTorrents || rec.UniqueHashes != p.UniqueHashes ||
			rec.Bytes != p.Bytes {
			t.Errorf("%s: sources %+v, report %+v", p.Provider, rec, p)
		}
		var hashes []string
		for _, t0 := range p.Torrents {
			hashes = append(hashes, t0.Hash)
		}
		us, err := HashUsages(ctx, db, hashes)
		if err != nil {
			t.Fatal(err)
		}
		for _, t0 := range p.Torrents {
			if hu := us[t0.Hash]; hu.Usage == Unused && t0.Usage == Used {
				t.Errorf("%s %s: inventory unused, report used", p.Provider, t0.ID)
			}
		}
	}
	us, err := HashUsages(ctx, db, []string{hA, "0000000000000000000000000000000000000000"})
	if err != nil {
		t.Fatal(err)
	}
	if a := us[hA]; a.Usage != Used || a.LinkedFiles != 1 {
		t.Errorf("hA used by its link: %+v", a)
	}
	if _, ok := us["0000000000000000000000000000000000000000"]; ok {
		t.Error("a hash no account holds has no usage")
	}
}

// A hash's usage unites the references of all its account torrents, whatever the provider order: rd's name leads to
// one link, tb's (another folder) to two more; the hash has the three, each once.
func TestHashUsageUnitesReferences(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := context.Background()
	old := e.now.Add(-72 * time.Hour)
	e.rd.list = []provider.Torrent{tor("1", hA, "Film.A.2020.1080p", 100, old, provider.StateDownloaded)}
	e.tb.list = []provider.Torrent{tor("9", hA, "Film A (2020)", 100, old, provider.StateDownloaded)}
	e.synced("rd")
	e.synced("tb")
	e.content(hA, 1)
	for i, folder := range []string{"Film.A.2020.1080p", "Film A (2020)", "Film A (2020)"} {
		e.exec(`INSERT INTO link (path, target, title_id, created_at, updated_at) VALUES (?, ?, 1, 0, 0)`,
			fmt.Sprintf("/lib/A/%d.mkv", i), mountPath+"/__all__/"+folder+fmt.Sprintf("/%d.mkv", i))
	}
	r, err := e.report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rd, tb := find(r, "rd", "1"), find(r, "tb", "9"); rd.LinkedFiles != 1 || tb.LinkedFiles != 2 {
		t.Fatalf("per torrent: rd %d tb %d", rd.LinkedFiles, tb.LinkedFiles)
	}
	us, err := HashUsages(ctx, e.st.Reader(), []string{hA})
	if err != nil {
		t.Fatal(err)
	}
	if a := us[hA]; a.Usage != Used || a.LinkedFiles != 3 || a.Refs[RefLink] != 3 || len(a.RefList) != 3 {
		t.Fatalf("united: %+v", a)
	}
}

// The screens read the stored answer: the report, Sources' record and the inventory's hash usage walk no scan root,
// read no mount listing and call no provider. The time of one report on a few thousand rows is logged.
func TestRecordReadsNothingLive(t *testing.T) {
	t.Parallel()
	e, z := zurgFixture(t)
	ctx := context.Background()
	for i := range 2750 {
		e.rd.list = append(e.rd.list, tor(fmt.Sprintf("x%d", i), fmt.Sprintf("%040x", i+1), fmt.Sprintf("Bulk.Film.%d.2020.1080p", i),
			10, e.now.Add(-72*time.Hour), provider.StateDownloaded))
	}
	if _, err := e.report(ctx); err != nil {
		t.Fatal(err)
	}
	z.reads.Store(0)
	e.rd.lists, e.tb.lists, e.rd.reads, e.tb.reads = 0, 0, 0, 0
	// A walk now would fail and add an unknown reason.
	if err := os.RemoveAll(e.pol.Torrents.ScanRoots[0]); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r, err := e.m.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("report of %d torrents: %v", r.Providers[0].AccountTorrents+r.Providers[1].AccountTorrents, time.Since(start))
	start = time.Now()
	if _, err := ReadProviderRecord(ctx, e.st.Reader(), "rd"); err != nil {
		t.Fatal(err)
	}
	if _, err := HashUsages(ctx, e.st.Reader(), []string{hA, hB}); err != nil {
		t.Fatal(err)
	}
	t.Logf("sources record and inventory usage: %v", time.Since(start))
	if len(r.Unknown) != 0 || z.reads.Load() != 0 || e.rd.lists+e.tb.lists+e.rd.reads+e.tb.reads != 0 {
		t.Fatalf("read live: unknown %v, zurg %d, rd %d/%d, tb %d/%d", r.Unknown, z.reads.Load(), e.rd.lists,
			e.rd.reads, e.tb.lists, e.tb.reads)
	}
	if got := usages(t, r)["rd/1"]; got.Usage != Used {
		t.Fatalf("the stored usage: %+v", got)
	}
}
