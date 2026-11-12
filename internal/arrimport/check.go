package arrimport

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/theatarr/theatarr/internal/arr"
)

// checkStatusWait bounds Check's first request, so an address where nothing answers fails within seconds instead of
// after the arr client's retries.
const checkStatusWait = 5 * time.Second

// CheckResult is what Check read from an instance: enough for the choices before a Preview.
type CheckResult struct {
	App         string      `json:"app"` // Radarr or Sonarr
	Version     string      `json:"version"`
	Titles      int         `json:"titles"`
	WithFiles   int         `json:"with_files"`
	Profiles    []CheckProf `json:"profiles"`
	RootFolders []CheckRoot `json:"root_folders"`
}

// CheckProf is one of the instance's quality profiles and how many titles use it.
type CheckProf struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Titles int    `json:"titles"`
}

// CheckRoot is one of the instance's root folders, its titles, and whether Theatarr can read it at that path.
type CheckRoot struct {
	Path     string `json:"path"`
	Titles   int    `json:"titles"`
	Readable bool   `json:"readable"`
}

// CheckError is an instance that did not answer, or answered as something else (answered 502).
type CheckError struct{ Msg string }

func (e *CheckError) Error() string { return e.Msg }

// Check reads an instance's version, quality profiles, root folders and title list (a few GETs, nothing else).
func Check(ctx context.Context, d Deps, flavour, url, key string) (*CheckResult, error) {
	if flavour != "radarr" && flavour != "sonarr" {
		return nil, &InputError{Msg: "Choose Radarr or Sonarr."}
	}
	if strings.TrimSpace(url) == "" || key == "" {
		return nil, &InputError{Msg: "Enter the instance's address and API key."}
	}
	g, err := d.Arr(url, key)
	if err != nil {
		if strings.Contains(err.Error(), "bad base URL") {
			return nil, &InputError{Msg: "Enter the address as http://host:port."}
		}
		return nil, &InputError{Msg: scrub(err.Error(), key)}
	}
	app := appName(flavour)
	unreachable := func(err error) error { return &CheckError{Msg: checkFailed(app, scrub(err.Error(), key), err)} }
	var status struct {
		AppName string `json:"appName"`
		Version string `json:"version"`
	}
	sctx, cancel := context.WithTimeout(ctx, checkStatusWait)
	err = g.GetJSON(sctx, "system/status", nil, &status)
	cancel()
	if err != nil {
		return nil, unreachable(err)
	}
	if status.AppName != "" && status.AppName != app {
		return nil, &CheckError{Msg: fmt.Sprintf("This address is %s, not %s.", status.AppName, app)}
	}
	var profiles []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := g.GetJSON(ctx, "qualityprofile", nil, &profiles); err != nil {
		return nil, unreachable(err)
	}
	var roots []struct {
		Path string `json:"path"`
	}
	if err := g.GetJSON(ctx, "rootfolder", nil, &roots); err != nil {
		return nil, unreachable(err)
	}
	var titles []struct {
		QualityProfileID int    `json:"qualityProfileId"`
		RootFolderPath   string `json:"rootFolderPath"`
		Path             string `json:"path"`
		HasFile          bool   `json:"hasFile"`
		Statistics       struct {
			EpisodeFileCount int `json:"episodeFileCount"`
		} `json:"statistics"`
	}
	list := "movie"
	if flavour == "sonarr" {
		list = "series"
	}
	if err := g.GetJSON(ctx, list, nil, &titles); err != nil {
		return nil, unreachable(err)
	}

	res := &CheckResult{App: app, Version: status.Version, Titles: len(titles), Profiles: []CheckProf{},
		RootFolders: []CheckRoot{}}
	byProfile := map[int]int{}
	byRoot := map[string]int{}
	var rootPaths []string
	for _, r := range roots {
		rootPaths = append(rootPaths, r.Path)
	}
	for _, t := range titles {
		if t.HasFile || t.Statistics.EpisodeFileCount > 0 {
			res.WithFiles++
		}
		byProfile[t.QualityProfileID]++
		root := t.RootFolderPath
		if root == "" {
			// The longest root folder that holds the title's folder.
			for _, r := range rootPaths {
				if under(t.Path, r) && len(r) > len(root) {
					root = r
				}
			}
		}
		byRoot[path.Clean(root)]++
	}
	for _, p := range profiles {
		res.Profiles = append(res.Profiles, CheckProf{ID: p.ID, Name: p.Name, Titles: byProfile[p.ID]})
	}
	sort.SliceStable(res.Profiles, func(i, j int) bool { return res.Profiles[i].Name < res.Profiles[j].Name })
	for _, r := range roots {
		cr := CheckRoot{Path: r.Path, Titles: byRoot[path.Clean(r.Path)]}
		if d.Library != nil {
			if ok, err := d.Library.DirExists(ctx, r.Path); err == nil {
				cr.Readable = ok
			}
		}
		res.RootFolders = append(res.RootFolders, cr)
	}
	return res, nil
}

// checkFailed is a failed Check request in plain words, with the technical cause after it.
func checkFailed(app, msg string, err error) string {
	var se *arr.StatusError
	if errors.As(err, &se) && se.Status == 401 || strings.Contains(msg, "HTTP 401") {
		return fmt.Sprintf("%s refused the API key; check the key under Settings › General in %s.", app, app)
	}
	detail := msg
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "context deadline exceeded") {
		detail = "no answer within a few seconds"
	} else if i := strings.LastIndex(msg, ": "); i >= 0 {
		detail = msg[i+2:]
	}
	return fmt.Sprintf("%s did not answer at this address; check the address and port. (%s)", app, detail)
}
