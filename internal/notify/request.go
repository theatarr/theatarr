package notify

import (
	"strconv"
	"strings"
)

// requestPayload uses Seerr's default webhook fields and notification names, with Theatarr's copies and joined users.
func requestPayload(b map[string]any, ev Event) {
	r := ev.Request
	mediaType := "Movie"
	mt := "movie"
	if r.Kind == "series" {
		mediaType, mt = "Series", "tv"
	}
	names := map[Kind]string{RequestPending: "MEDIA_PENDING", RequestApproved: "MEDIA_APPROVED",
		RequestAutoApproved: "MEDIA_AUTO_APPROVED", RequestDeclined: "MEDIA_DECLINED",
		RequestAvailable: "MEDIA_AVAILABLE", RequestPartial: "MEDIA_AVAILABLE", RequestFailed: "MEDIA_FAILED"}
	events := map[Kind]string{RequestPending: "New " + mediaType + " Request", RequestApproved: mediaType + " Request Approved",
		RequestAutoApproved: mediaType + " Request Automatically Approved", RequestDeclined: mediaType + " Request Declined",
		RequestAvailable: mediaType + " Now Available", RequestPartial: mediaType + " Partially Available", RequestFailed: mediaType + " Request Failed"}
	b["notification_type"], b["event"] = names[ev.Kind], events[ev.Kind]
	b["subject"], b["message"], b["image"] = r.Name, r.Reason, r.Image
	status := map[string]string{"pending": "PENDING", "approved": "PROCESSING", "processing": "PROCESSING",
		"available": "AVAILABLE", "failed": "UNKNOWN", "declined": "UNKNOWN"}[r.Status]
	if ev.Kind == RequestPartial {
		status = "PARTIALLY_AVAILABLE"
	}
	b["media"] = map[string]any{"media_type": mt, "tmdbId": r.TMDbID, "tvdbId": r.TVDbID, "status": status}
	username := ""
	if len(r.Users) > 0 {
		username = r.Users[0].DisplayName
		if username == "" {
			username = r.Users[0].Username
		}
	}
	b["request"] = map[string]any{"request_id": r.ID, "requestedBy_username": username, "requesters": r.Users,
		"status": r.Status, "seasons": r.Seasons, "versions": r.Versions, "titleId": r.TitleID}
	var seasons, copies []string
	for _, s := range r.Seasons {
		seasons = append(seasons, strconv.Itoa(s))
	}
	for _, v := range r.Versions {
		// A label that collided with another copy's already ends in its key ("4K · tv-2160p-theatarr").
		switch {
		case v.Label == "":
			copies = append(copies, v.Key)
		case strings.Contains(v.Label, v.Key):
			copies = append(copies, v.Label)
		default:
			copies = append(copies, v.Label+" ("+v.Key+")")
		}
	}
	seasonText := strings.Join(seasons, ", ")
	if seasonText == "" {
		seasonText = "All"
	}
	b["extra"] = []map[string]string{{"name": "Requested Seasons", "value": seasonText},
		{"name": "Requested Copies", "value": strings.Join(copies, ", ")}}
}
