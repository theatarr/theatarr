package web

import (
	"context"
	"slices"
	"time"

	"github.com/theatarr/theatarr/internal/activity"
)

// RequestDetail adds acquisition progress to GET requests/{id}. This is deliberately a small, safe projection:
// stage words and request times, never download errors, releases, providers, files or paths, even for managers.
type RequestDetail struct {
	Request
	ApprovedAtCreation bool              `json:"approved_at_creation"`
	Progress           []RequestProgress `json:"progress"`
}

type RequestProgress struct {
	Key      string     `json:"key"`
	Approval string     `json:"approval"`
	Words    string     `json:"words"`
	Updated  *time.Time `json:"updated_at,omitempty"`
	// Episode counts cover only the requested seasons; absent before the episodes are known.
	Have  *int `json:"episodes_available,omitempty"`
	Total *int `json:"episodes_total,omitempty"`
}

func (w *Web) requestDetail(ctx context.Context, r *Request) (*RequestDetail, error) {
	out := &RequestDetail{Request: *r, Progress: []RequestProgress{}}
	// Auto-approval records the requester as the decision maker. There is no historical auto-approve flag; use
	// that attribution, not today's account setting or equal timestamps (catalog.Add may take some time).
	out.ApprovedAtCreation = r.decidedBy != 0 && r.decidedBy == r.userID && r.DecidedAt != nil &&
		r.Status != ReqDeclined && r.Status != ReqCanceled
	var downloads []activity.Download
	var episodes *EpisodeList
	open := r.Status != ReqPending && r.Status != ReqDeclined && r.Status != ReqCanceled
	if open && r.TitleID != nil {
		var err error
		downloads, err = activity.Downloads(ctx, w.st.Reader(), activity.Filter{TitleIDs: []int64{*r.TitleID}, Versions: r.keys})
		if err != nil {
			return nil, err
		}
		if r.Kind == "series" {
			episodes, err = w.titleEpisodes(ctx, *r.TitleID, -1, false)
			if err != nil {
				return nil, err
			}
		}
	}
	out.Versions = slices.Clone(r.Versions)
	for i := range out.Versions {
		v := &out.Versions[i]
		p := RequestProgress{Key: v.Key, Words: "Waiting for a release that matches"}
		p.Approval = "Approval turns on this library and searches for a release that matches."
		if !v.GrabState.WillGrab {
			p.Approval = "Approval records this request; automatic downloads are paused"
			p.Words = "Automatic downloads are paused"
		}
		if episodes != nil && len(episodes.Episodes) > 0 {
			have, total := 0, 0
			for _, e := range episodes.Episodes {
				c := e.Versions[v.Key]
				if len(r.Seasons) > 0 {
					if !slices.Contains(r.Seasons, int(e.Season)) {
						continue
					}
				} else if !c.Monitored {
					continue
				}
				total++
				if c.HasFile {
					have++
				}
			}
			p.Have, p.Total = &have, &total
			v.Available = total > 0 && have == total
		}
		// Keep all concurrent acquisitions: a series can be downloading several requested episodes at once.
		var latest *activity.Download
		var current []activity.Download
		for j := range downloads {
			d := &downloads[j]
			if d.Version != v.Key || d.Updated.Before(r.CreatedAt) {
				continue
			}
			if episodes != nil && len(d.Episodes) > 0 && len(r.Seasons) > 0 {
				matches := false
				for _, e := range episodes.Episodes {
					if slices.Contains(r.Seasons, int(e.Season)) && slices.Contains(d.Episodes, e.ID) {
						matches = true
					}
				}
				if !matches {
					continue
				}
			}
			if latest == nil || d.Updated.After(latest.Updated) {
				latest = d
			}
			if slices.Contains(activity.InHand, d.State) {
				current = append(current, *d)
			}
		}
		switch {
		case r.Status == ReqPending:
			p.Words = "Waiting for approval"
		case r.Status == ReqDeclined:
			p.Words = "Request declined"
		case r.Status == ReqCanceled:
			p.Words = "Request canceled"
		case v.Available:
			p.Words = "Available"
		case len(current) > 0:
			for _, d := range current {
				q := p
				q.Words, q.Updated = d.Words(""), &d.Updated
				out.Progress = append(out.Progress, q)
			}
			continue
		case latest != nil && latest.State == "failed":
			p.Words, p.Updated = latest.Words(""), &latest.Updated
		case episodes != nil && (p.Total == nil || *p.Total == 0):
			p.Words = "Waiting for episode information"
		}
		out.Progress = append(out.Progress, p)
	}
	return out, nil
}
