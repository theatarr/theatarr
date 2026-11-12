package importarr

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

// fetchBlocklist reads every page on every import: unlike history, old entries can change or disappear.
func fetchBlocklist(ctx context.Context, g Getter, s *Snapshot) error {
	for page := 1; ; page++ {
		q := url.Values{"page": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(historyPageSize)},
			"sortKey": {"id"}, "sortDirection": {"ascending"}}
		var p struct {
			TotalRecords int `json:"totalRecords"`
			Records      []struct {
				historyRecord
				Protocol        string `json:"protocol"`
				TorrentInfoHash string `json:"torrentInfoHash"`
			} `json:"records"`
		}
		if err := g.GetJSON(ctx, "blocklist", q, &p); err != nil {
			return err
		}
		for _, r := range p.Records {
			if r.Data == nil {
				r.Data = map[string]any{}
			}
			if r.Protocol != "" {
				r.Data["protocol"] = r.Protocol
			}
			if r.TorrentInfoHash != "" {
				r.Data["torrentInfoHash"] = r.TorrentInfoHash
			}
			e := toEvent(r.historyRecord)
			if e.InfoHash != "" || strings.TrimSpace(e.SourceTitle) != "" {
				s.Blocklist = append(s.Blocklist, BlockedRelease{ArrTitleID: e.ArrTitleID, InfoHash: e.InfoHash, SourceTitle: e.SourceTitle})
			}
		}
		if len(p.Records) == 0 || page*historyPageSize >= p.TotalRecords {
			return nil
		}
	}
}
