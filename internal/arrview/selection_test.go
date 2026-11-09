package arrview

import (
	"strconv"
	"testing"
)

// DELETE tag/{id} follows TagService.Delete: 404 for an unknown tag, 409 while a title or notification carries it,
// 200 with no body once free. Tags are global, so a title in another view holds it too.
func TestDeleteTag(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	newTag := func(label string) (string, int) {
		var tg map[string]any
		e.call("POST", "/radarr/api/v3/tag", map[string]any{"label": label}, 201, &tg)
		id := int(tg["id"].(float64))
		return strconv.Itoa(id), id
	}
	e.call("DELETE", "/radarr/api/v3/tag/99", nil, 404, nil)

	// Free: deleted.
	free, _ := newTag("free")
	rec := e.call("DELETE", "/radarr/api/v3/tag/"+free, nil, 200, nil)
	if rec.Body.Len() != 0 {
		t.Errorf("DELETE body %q", rec.Body.String())
	}
	e.call("GET", "/radarr/api/v3/tag/"+free, nil, 404, nil)

	// On a series, deleted through the Radarr view: refused with the arr's message, from either flavour.
	onSeries, seriesTag := newTag("on-series")
	e.call("PUT", "/sonarr/api/v3/series/editor", map[string]any{"seriesIds": []int{IDBase + 10},
		"tags": []int{seriesTag}, "applyTags": "add"}, 202, nil)
	var body map[string]any
	e.call("DELETE", "/radarr/api/v3/tag/"+onSeries, nil, 409, &body)
	if want := "Tag with ID " + onSeries + " 'on-series' cannot be deleted since it's still in use"; body["message"] != want {
		t.Errorf("409 message %q", body["message"])
	}
	e.call("PUT", "/sonarr/api/v3/series/editor", map[string]any{"seriesIds": []int{IDBase + 10},
		"tags": []int{seriesTag}, "applyTags": "remove"}, 202, nil)
	e.call("DELETE", "/sonarr/api/v3/tag/"+onSeries, nil, 200, nil)

	// On a notification of another view.
	onNote, _ := newTag("on-note")
	e.exec(`INSERT INTO arrview_notification (view, name, implementation, resource, created_at, updated_at)
		VALUES ('sonarr', 'hook', 'Webhook', ?, 0, 0)`, `{"name":"hook","tags":[`+onNote+`]}`)
	e.call("DELETE", "/radarr/api/v3/tag/"+onNote, nil, 409, nil)
	e.exec(`DELETE FROM arrview_notification`)
	e.call("DELETE", "/radarr/api/v3/tag/"+onNote, nil, 200, nil)

	// On a title no view shows any more (its versions removed through DELETE): the assignment goes with the tag.
	hidden, hiddenTag := newTag("hidden")
	e.call("PUT", "/radarr/api/v3/movie/editor", map[string]any{"movieIds": []int{IDBase + 1},
		"tags": []int{hiddenTag}, "applyTags": "add"}, 202, nil)
	e.call("DELETE", "/radarr/api/v3/tag/"+hidden, nil, 409, nil)
	e.cutover()
	e.call("DELETE", "/radarr/api/v3/movie/"+strconv.Itoa(IDBase+1), nil, 200, nil)
	e.call("DELETE", "/radarr/api/v3/tag/"+hidden, nil, 200, nil)
	if n := e.one(`SELECT COUNT(*) FROM arrview_title_tag WHERE tag_id = ?`, hidden); n != 0 {
		t.Errorf("%d assignments left", n)
	}
}

// Delay and release profiles are not supported: the views read one delay profile with no delay and no release
// profiles, and refuse writes with 405.
func TestSelectionProfiles(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, base := range []string{"/radarr", "/sonarr", "/v/movie-web/radarr"} {
		var dps []map[string]any
		e.call("GET", base+"/api/v3/delayprofile", nil, 200, &dps)
		if len(dps) != 1 || dps[0]["id"].(float64) != 1 || dps[0]["torrentDelay"].(float64) != 0 ||
			dps[0]["enableTorrent"] != true || dps[0]["enableUsenet"] != false || dps[0]["preferredProtocol"] != "torrent" ||
			len(dps[0]["tags"].([]any)) != 0 {
			t.Errorf("%s delayprofile %v", base, dps)
		}
		wantKeys(t, "delay profile", dps[0], fields(`id enableUsenet enableTorrent preferredProtocol usenetDelay
			torrentDelay bypassIfHighestQuality bypassIfAboveCustomFormatScore minimumCustomFormatScore order tags`))
		e.call("GET", base+"/api/v3/delayprofile/1", nil, 200, nil)
		e.call("GET", base+"/api/v3/delayprofile/2", nil, 404, nil)
		var rps []map[string]any
		rec := e.call("GET", base+"/api/v3/releaseprofile", nil, 200, &rps)
		if len(rps) != 0 || rec.Body.String() != "[]\n" {
			t.Errorf("%s releaseprofile %q", base, rec.Body.String())
		}
		e.call("GET", base+"/api/v3/releaseprofile/1", nil, 404, nil)
	}
	for _, c := range []struct{ method, path string }{
		{"POST", "/radarr/api/v3/delayprofile"},
		{"PUT", "/radarr/api/v3/delayprofile/1"},
		{"DELETE", "/sonarr/api/v3/delayprofile/1"},
		{"PUT", "/sonarr/api/v3/delayprofile/reorder/1"},
		{"POST", "/radarr/api/v3/releaseprofile"},
		{"PUT", "/sonarr/api/v3/releaseprofile/1"},
		{"DELETE", "/sonarr/api/v3/releaseprofile/1"},
	} {
		var body map[string]any
		rec := e.call(c.method, c.path, map[string]any{"required": []string{"x"}}, 405, &body)
		if rec.Header().Get("Allow") != "GET" || body["message"] == "" {
			t.Errorf("%s %s: %v %v", c.method, c.path, rec.Header(), body)
		}
	}
}
