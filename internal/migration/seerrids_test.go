package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
)

const seerrIDsKey = "seerr-ids-key-0123456789"

// fakeSeerrMedia answers Seerr's GET /api/v1/settings/{radarr,sonarr} and the paged GET /api/v1/media (routes/media.ts:
// take, skip, pageInfo.results) with media shaped like entity/Media.ts, null where Seerr stores null. It fails the
// test on anything but a GET of those.
func fakeSeerrMedia(t *testing.T, radarr, sonarr string, media []map[string]any) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		if r.Method != http.MethodGet || r.Header.Get("X-Api-Key") != seerrIDsKey {
			t.Errorf("seerr got %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/settings/radarr":
			w.Write([]byte(radarr))
		case "/api/v1/settings/sonarr":
			w.Write([]byte(sonarr))
		case "/api/v1/media":
			take, _ := strconv.Atoi(r.URL.Query().Get("take"))
			skip, _ := strconv.Atoi(r.URL.Query().Get("skip"))
			end := min(skip+take, len(media))
			page := media[min(skip, len(media)):end]
			json.NewEncoder(w).Encode(map[string]any{"pageInfo": map[string]any{"pages": (len(media) + take - 1) / take,
				"pageSize": take, "results": len(media), "page": skip/take + 1}, "results": page})
		default:
			t.Errorf("seerr got %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func seerrItem(id int64, typ string, tmdb int64, tvdb, sid, ext, sid4k, ext4k any) map[string]any {
	return map[string]any{"id": id, "mediaType": typ, "tmdbId": tmdb, "tvdbId": tvdb, "status": 5, "status4k": 1,
		"serviceId": sid, "serviceId4k": sid4k, "externalServiceId": ext, "externalServiceId4k": ext4k,
		"externalServiceSlug": nil, "ratingKey": nil, "downloadStatus": []any{}, "downloadStatus4k": []any{}}
}

// TestCheckSeerrIDs: every stored id goes to the view of its flavour (the 4K twin too) and is classed by the view's
// answer, compared as Seerr's availability sync compares (movies by TMDb, series by TVDB); Seerr's media is read in
// pages until pageInfo.results.
func TestCheckSeerrIDs(t *testing.T) {
	radarr := `[{"id":0,"name":"Radarr 4K","hostname":"radarr","port":7878,"apiKey":"arr-secret-in-seerr","is4k":false,"isDefault":true},
		{"id":2,"name":"Radarr UHD","is4k":true,"isDefault":true}]`
	sonarr := `[{"id":0,"name":"Sonarr 4K","apiKey":"arr-secret-in-seerr","is4k":false,"isDefault":true}]`
	media := []map[string]any{
		seerrItem(1, "movie", 101, nil, 0, 7, nil, nil),     // alias 7 → Alpha: ok
		seerrItem(2, "movie", 102, nil, 0, 1000002, 2, 9),   // ours → Beta: ok; 4K alias 9 → Alpha: wrong
		seerrItem(3, "movie", 105, nil, 1, 99, nil, nil),    // server 1 is gone; 404
		seerrItem(4, "tv", 501, 366924, 0, 35, nil, nil),    // Reacher's sonarr_4k id 35 → Andor: wrong
		seerrItem(5, "tv", 502, 201, 0, 1000010, nil, nil),  // ok by TVDB
		seerrItem(6, "tv", 503, nil, 0, 36, nil, nil),       // no TVDB id; the view has no TMDb id either
		seerrItem(7, "tv", 504, nil, 0, 37, nil, nil),       // no TVDB id; compared by TMDb
		seerrItem(8, "movie", 106, nil, nil, nil, nil, nil), // never sent to an arr
		seerrItem(9, "movie", 107, nil, nil, 0, nil, nil),   // id 0 is no id
		seerrItem(10, "music", 108, nil, 0, 7, nil, nil),    // not a movie or series
	}
	for i := int64(11); i <= 130; i++ { // a second and third page
		media = append(media, seerrItem(i, "movie", 1000+i, nil, nil, nil, nil, nil))
	}
	media = append(media, seerrItem(131, "movie", 101, nil, 0, 1000001, nil, nil)) // on the last page: ok
	srv, seen := fakeSeerrMedia(t, radarr, sonarr, media)
	views := map[string]string{
		"radarr/7": `{"title":"Alpha","tmdbId":101}`, "radarr/1000002": `{"title":"Beta","tmdbId":102}`,
		"radarr/9": `{"title":"Alpha","tmdbId":101}`, "radarr/1000001": `{"title":"Alpha","tmdbId":101}`,
		"sonarr/35": `{"title":"Andor","tvdbId":393189}`, "sonarr/1000010": `{"title":"The Show","tvdbId":201}`,
		"sonarr/36": `{"title":"Show 36","tvdbId":36}`, "sonarr/37": `{"title":"Show 37","tvdbId":37,"tmdbId":504}`,
	}
	var asked []string
	resolve := func(_ context.Context, fl, path string, id int64) (*ViewTitle, error) {
		asked = append(asked, fmt.Sprintf("%s %d", path, id))
		b, ok := views[fl+"/"+strconv.FormatInt(id, 10)]
		if !ok {
			return nil, nil
		}
		var vt ViewTitle
		return &vt, json.Unmarshal([]byte(b), &vt)
	}
	g, err := NewDialer(arr.Options{Timeout: 5 * time.Second, MinInterval: time.Millisecond, Retries: 0})(ServiceSeerr,
		srv.URL, seerrIDsKey)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := CheckSeerrIDs(context.Background(), g, map[string]string{"radarr": "/radarr", "sonarr": "/sonarr"}, resolve)
	if err != nil {
		t.Fatal(err)
	}
	want := SeerrIDSummary{Media: 131, Checked: 9, OK: 5, WrongTitle: 2, Unresolved: 1, Unverifiable: 1, NoServer: 1}
	if rep.Summary != want {
		t.Errorf("summary %+v, want %+v", rep.Summary, want)
	}
	got := map[string]string{}
	for _, it := range rep.Items {
		got[fmt.Sprintf("%d %s", it.MediaID, it.Field)] = it.Result
		if it.MediaID == 2 && it.Is4K && (it.Server != "Radarr UHD" || !it.IsDefault || it.View != "/radarr") {
			t.Errorf("4K item %+v", it)
		}
		if it.MediaID == 3 && (!it.NoServer || it.Server != "") {
			t.Errorf("gone server %+v", it)
		}
		if it.MediaID == 4 && (it.ViewTitle == nil || it.ViewTitle.Title != "Andor" || it.TvdbID != 366924) {
			t.Errorf("wrong title %+v", it)
		}
	}
	wantItems := map[string]string{
		"1 externalServiceId": SeerrIDOK, "2 externalServiceId": SeerrIDOK, "2 externalServiceId4k": SeerrIDWrongTitle,
		"3 externalServiceId": SeerrIDUnresolved, "4 externalServiceId": SeerrIDWrongTitle,
		"5 externalServiceId": SeerrIDOK, "6 externalServiceId": SeerrIDUnverifiable, "7 externalServiceId": SeerrIDOK,
		"131 externalServiceId": SeerrIDOK,
	}
	if fmt.Sprint(got) != fmt.Sprint(wantItems) {
		t.Errorf("items %v\nwant %v", got, wantItems)
	}
	if strings.Join(asked, ",") != "/radarr 7,/radarr 1000002,/radarr 9,/radarr 99,/sonarr 35,/sonarr 1000010,/sonarr 36,/sonarr 37,/radarr 1000001" {
		t.Errorf("asked the views %v", asked)
	}
	wantSeen := []string{"GET /api/v1/settings/radarr?", "GET /api/v1/settings/sonarr?", "GET /api/v1/media?skip=0&take=100",
		"GET /api/v1/media?skip=100&take=100"}
	if fmt.Sprint(*seen) != fmt.Sprint(wantSeen) {
		t.Errorf("seerr saw %v", *seen)
	}
	b, _ := json.Marshal(rep)
	if strings.Contains(string(b), "arr-secret-in-seerr") {
		t.Error("the report holds the arr key Seerr keeps")
	}
}
