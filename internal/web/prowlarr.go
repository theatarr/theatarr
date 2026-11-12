package web

// Link Prowlarr: Prowlarr's indexers as Torznab feeds. The trackers and their logins stay in Prowlarr; Theatarr
// reads its indexer list (GET /api/v1/indexer, on the LAN) and adds the chosen ones as feeds. No catalog needed.

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/theatarr/theatarr/internal/auth"
	"github.com/theatarr/theatarr/internal/indexer/cardigann"
	"github.com/theatarr/theatarr/internal/policy"
	"github.com/theatarr/theatarr/internal/settings"
)

// prowlarrBudget is the request budget the linked feeds share, as theatarr migrate makes it.
const prowlarrBudget = "prowlarr"

// ProwlarrFeeds answers GET /settings/prowlarr/feeds. Without a Prowlarr address and key, Configured is false and
// nothing else is set.
type ProwlarrFeeds struct {
	Configured bool                     `json:"configured"`
	URL        string                   `json:"url,omitempty"`
	Feeds      []cardigann.ProwlarrFeed `json:"feeds,omitempty"`
}

type prowlarrLinkBody struct {
	Generation        *int64   `json:"generation"`
	IDs               []int    `json:"ids"`
	Versions          []string `json:"versions"`
	ConfirmationToken string   `json:"confirmation_token"`
}

func (w *Web) mountProwlarrAPI(mux *http.ServeMux, b string) {
	h := func(pattern string, fn http.HandlerFunc) { w.handle(mux, pattern, SignedIn, auth.PermAdmin, fn) }
	h("GET "+b+"/prowlarr/feeds", w.apiProwlarrFeeds)
	h("POST "+b+"/prowlarr/feeds", w.apiProwlarrLink)
}

// prowlarrAt is the head's Prowlarr address and key; ok is false when either is missing.
func prowlarrAt(snap *settings.Snapshot) (url, key string, ok bool) {
	if snap.Policy.Cardigann == nil || snap.Policy.Cardigann.Prowlarr == nil {
		return "", "", false
	}
	pr := snap.Policy.Cardigann.Prowlarr
	key = snap.Policy.Getenv(pr.APIKeyEnv)
	return pr.URL, key, pr.URL != "" && key != ""
}

// prowlarrKeyLeft reports whether a cardigann section save moves Prowlarr to another address while the stored key
// stays: the key is never sent to a new address, so it must be entered again (as for Radarr and Sonarr lists).
func prowlarrKeyLeft(snap *settings.Snapshot, config json.RawMessage, writes map[string]settings.Secret) bool {
	before, key, ok := prowlarrAt(snap)
	if !ok || key == "" {
		return false
	}
	if _, sent := writes["prowlarr.api_key"]; sent {
		return false
	}
	var next struct {
		Prowlarr *struct {
			URL string `json:"url"`
		} `json:"prowlarr"`
	}
	if json.Unmarshal(config, &next) != nil || next.Prowlarr == nil || next.Prowlarr.URL == "" {
		return false
	}
	return cardigann.NormalizeFeedURL(next.Prowlarr.URL) != cardigann.NormalizeFeedURL(before)
}

// readProwlarrFeeds reads Prowlarr and marks its indexers against the snapshot's; a read error is answered (502).
func (w *Web) readProwlarrFeeds(rw http.ResponseWriter, r *http.Request, snap *settings.Snapshot, url, key string) ([]cardigann.ProwlarrFeed, bool) {
	list, err := cardigann.ReadProwlarr(r.Context(), url, key)
	if err != nil {
		msg := scrubText(err.Error(), []string{key})
		w.log.Warn("prowlarr read failed", "err", msg)
		writeJSON(rw, http.StatusBadGateway, apiError{Error: "could not read Prowlarr's indexers: " + msg})
		return nil, false
	}
	existing := map[string]string{}
	taken := make([]string, 0, len(snap.Records.Indexers))
	for _, rec := range snap.Records.Indexers {
		taken = append(taken, rec.Key)
		if rec.Config.URL != "" {
			existing[cardigann.NormalizeFeedURL(rec.Config.URL)] = rec.Key
		}
	}
	return cardigann.ProwlarrFeeds(list, url, existing, taken), true
}

func (w *Web) apiProwlarrFeeds(rw http.ResponseWriter, r *http.Request) {
	if w.refuseUnavailable(rw) {
		return
	}
	snap, ok := w.settingsHead(rw, r)
	if !ok {
		return
	}
	url, key, ok := prowlarrAt(snap)
	if !ok {
		writeJSON(rw, http.StatusOK, ProwlarrFeeds{})
		return
	}
	feeds, ok := w.readProwlarrFeeds(rw, r, snap, url, key)
	if !ok {
		return
	}
	writeJSON(rw, http.StatusOK, ProwlarrFeeds{Configured: true, URL: url, Feeds: feeds})
}

// apiProwlarrLink adds the chosen linkable Prowlarr indexers as enabled Torznab feeds in one save: each gets
// Prowlarr's key as its credential and the shared prowlarr budget (made when absent), and joins the named libraries.
func (w *Web) apiProwlarrLink(rw http.ResponseWriter, r *http.Request) {
	if w.refuseUnavailable(rw) {
		return
	}
	var body prowlarrLinkBody
	if !decodeSettingsJSON(rw, r, &body) {
		return
	}
	if len(body.IDs) == 0 {
		badRequest(rw, "ids is required: the Prowlarr indexers to link")
		return
	}
	snap, ok := w.settingsAt(rw, r, body.Generation)
	if !ok {
		return
	}
	url, key, ok := prowlarrAt(snap)
	if !ok {
		badRequest(rw, "Prowlarr is not connected: set its address and API key first")
		return
	}
	feeds, ok := w.readProwlarrFeeds(rw, r, snap, url, key)
	if !ok {
		return
	}
	req := settings.SaveRequest{ExpectedGeneration: snap.Generation, Records: cloneRecords(snap.Records),
		ConfirmationToken: body.ConfirmationToken, SecretWrites: map[string]settings.Secret{}}
	if !slices.ContainsFunc(req.Records.Budgets, func(b settings.Budget) bool { return b.Key == prowlarrBudget }) {
		req.Records.Budgets = append(req.Records.Budgets, settings.Budget{Record: settings.Record{Key: prowlarrBudget},
			Config: policy.Budget{Requests: 60, Per: policy.Duration(time.Minute)}})
	}
	priority := 0
	for _, rec := range req.Records.Indexers {
		priority = max(priority, rec.Priority)
	}
	var keys []string
	for _, id := range body.IDs {
		i := slices.IndexFunc(feeds, func(f cardigann.ProwlarrFeed) bool { return f.ID == id })
		if i < 0 || feeds[i].Status != cardigann.FeedLinkable {
			badRequest(rw, "Prowlarr indexer "+strconv.Itoa(id)+" cannot be linked: not found, already linked, off in Prowlarr or Usenet")
			return
		}
		f := feeds[i]
		if slices.Contains(keys, f.Key) {
			continue // listed twice
		}
		priority++
		req.Records.Indexers = append(req.Records.Indexers, settings.Indexer{Record: settings.Record{Key: f.Key},
			Enabled: true, Priority: priority, Config: policy.Indexer{Kind: "torznab", URL: f.URL,
				Privacy: policy.Privacy(f.Privacy), Budget: prowlarrBudget}})
		req.SecretWrites[entityIndexers+"/"+f.Key] = settings.NewSecret(key)
		keys = append(keys, f.Key)
	}
	if err := addToVersions(&req, body.Versions, keys...); err != nil {
		writeBodyError(rw, err, []string{key})
		return
	}
	w.saveSettings(rw, r, req, []string{key}, entityIndexers, "")
}
