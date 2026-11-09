package arr

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
)

// EventGrabbed is the history eventType for a grab. Both arrs use 1 (Radarr MovieHistoryEventType.Grabbed,
// Sonarr EpisodeHistoryEventType.Grabbed); the API serialises it as the string "grabbed".
const EventGrabbed = 1

// HistoryPage is one page of GET /api/v3/history. Records stay raw so they can be sanitised field by field.
type HistoryPage struct {
	Page         int               `json:"page"`
	PageSize     int               `json:"pageSize"`
	TotalRecords int               `json:"totalRecords"`
	Records      []json.RawMessage `json:"records"`
}

// GrabbedHistory fetches one page of grabbed events, oldest first. Oldest first keeps earlier pages stable while
// new grabs arrive during a long walk; callers still dedupe by id.
func (c *Client) GrabbedHistory(ctx context.Context, page, pageSize int) (*HistoryPage, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("pageSize", strconv.Itoa(pageSize))
	q.Set("sortKey", "date")
	q.Set("sortDirection", "ascending")
	q.Set("eventType", strconv.Itoa(EventGrabbed))
	var hp HistoryPage
	if err := c.GetJSON(ctx, "history", q, &hp); err != nil {
		return nil, err
	}
	return &hp, nil
}

// Parse asks the arr to parse a release title: GET /api/v3/parse?title=. The body is returned raw with its status.
func (c *Client) Parse(ctx context.Context, title string) (*Response, error) {
	q := url.Values{}
	q.Set("title", title)
	return c.Get(ctx, "parse", q)
}
