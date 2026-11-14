package torrents

import (
	"context"
	"errors"
	"io"
	"log/slog"
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
// provider's own name. Sources and Saved inventory get the same usage as the report (RecordUsage).
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
	u, err := e.m.RecordUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range r.Providers {
		rec := u.Providers[p.Provider]
		if rec.Usage != p.Usage || rec.AccountTorrents != p.AccountTorrents || rec.UniqueHashes != p.UniqueHashes {
			t.Errorf("%s: sources %+v, report %+v", p.Provider, rec, p.Usage)
		}
		for _, t0 := range p.Torrents {
			if hu := u.Hashes[t0.Hash]; hu.Usage == Unused && t0.Usage == Used {
				t.Errorf("%s %s: inventory unused, report used", p.Provider, t0.ID)
			}
		}
	}
	if a := u.Hashes[hA]; a.Usage != Used || a.LinkedFiles != 1 {
		t.Errorf("hA used by its link: %+v", a)
	}
	if _, ok := u.Hashes["0000000000000000000000000000000000000000"]; ok {
		t.Error("a hash no account holds has no usage")
	}
}
