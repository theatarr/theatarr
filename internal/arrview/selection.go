package arrview

import (
	"math"
	"net/http"
)

// Delay profiles and release profiles. A typical arr setup has no
// release profile and only the default delay profile with no delay, so Theatarr has neither: every release is decided as it
// is found, and no must-contain / must-not-contain terms apply. The views read them as that (one delay profile with no
// delay, no release profiles) and refuse writes with 405, so a client cannot believe it configured one. theatarr import-arr
// warns when an arr gains either (internal/importarr selection.go).

// delayProfileResource is DelayProfileResource (Radarr and Sonarr have the same fields).
type delayProfileResource struct {
	ID                             int64   `json:"id"`
	EnableUsenet                   bool    `json:"enableUsenet"`
	EnableTorrent                  bool    `json:"enableTorrent"`
	PreferredProtocol              string  `json:"preferredProtocol"`
	UsenetDelay                    int     `json:"usenetDelay"`
	TorrentDelay                   int     `json:"torrentDelay"`
	BypassIfHighestQuality         bool    `json:"bypassIfHighestQuality"`
	BypassIfAboveCustomFormatScore bool    `json:"bypassIfAboveCustomFormatScore"`
	MinimumCustomFormatScore       int     `json:"minimumCustomFormatScore"`
	Order                          int     `json:"order"`
	Tags                           []int64 `json:"tags"`
}

// releaseProfileResource is ReleaseProfileResource. None exist; the type gives GET releaseprofile its shape.
type releaseProfileResource struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name,omitempty"`
	Enabled  bool     `json:"enabled"`
	Required []string `json:"required"`
	Ignored  []string `json:"ignored"`
	// IndexerID 0 is any indexer.
	IndexerID int     `json:"indexerId"`
	Tags      []int64 `json:"tags"`
}

// theatarrDelayProfile is what Theatarr does, written as the arrs' default delay profile (id 1, order int.MaxValue, no tags):
// torrents only (usenet is never routed: debrid by info hash), no delay, so the bypass switches never matter.
func theatarrDelayProfile() delayProfileResource {
	return delayProfileResource{ID: 1, EnableTorrent: true, PreferredProtocol: "torrent", BypassIfHighestQuality: true,
		Order: math.MaxInt32, Tags: []int64{}}
}

// GET delayprofile.
func (s *Service) delayProfiles(w http.ResponseWriter, _ *request) {
	writeJSON(w, http.StatusOK, []delayProfileResource{theatarrDelayProfile()})
}

// GET delayprofile/{id}: only id 1.
func (s *Service) delayProfile(w http.ResponseWriter, r *request) {
	if id, ok := r.pathID(1); ok && id == 1 {
		writeJSON(w, http.StatusOK, theatarrDelayProfile())
		return
	}
	notFound(w, "DelayProfile with ID "+r.raw[1]+" does not exist")
}

// POST, PUT and DELETE on delayprofile[/…] and releaseprofile[/…]: not supported. 405 with the reason.
func (s *Service) selectionNotSupported(w http.ResponseWriter, r *request) {
	w.Header().Set("Allow", "GET")
	msg := "Delay profiles are not supported by Theatarr: releases are grabbed when found, without delay"
	if r.seg[0] == "releaseprofile" {
		msg = "Release profiles are not supported by Theatarr: use custom formats in the policy for must-contain and " +
			"must-not-contain terms"
	}
	s.log.Info("selection write refused", "view", r.v.Key, "method", r.Method, "path", r.URL.Path)
	writeJSON(w, http.StatusMethodNotAllowed, errorBody{Message: msg})
}

// seg0 is the first path segment, or "".
func seg0(seg []string) string {
	if len(seg) == 0 {
		return ""
	}
	return seg[0]
}
