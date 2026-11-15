package web

import (
	"slices"

	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/policy"
)

// VersionGrabState is the runtime gates for a version, independent of search and monitoring permissions.
type VersionGrabState struct {
	WillGrab    bool `json:"will_grab"`
	Paused      bool `json:"paused"`
	PauseForced bool `json:"pause_forced"`
	// LibraryWrites is the library's own write gate (library.Gate.Active): library writes on, and the version listed
	// under library.versions with roots.
	LibraryWrites bool `json:"library_writes"`
}

// VersionGrabSummary is a configured version's runtime gates on Overview and Setup.
type VersionGrabSummary struct {
	Key string `json:"key"`
	// Label is the library's name (libraryNames).
	Label     string           `json:"label"`
	GrabState VersionGrabState `json:"grab_state"`
}

func versionGrabSummaries(p *policy.Policy) []VersionGrabSummary {
	out := []VersionGrabSummary{}
	if p != nil {
		names := libraryNames(p)
		for _, key := range sortedKeys(p.Versions) {
			out = append(out, VersionGrabSummary{Key: key, Label: names[key], GrabState: versionGrabState(p, key)})
		}
	}
	return out
}

func versionGrabState(p *policy.Policy, key string) VersionGrabState {
	out := VersionGrabState{Paused: true}
	if p == nil {
		return out
	}
	// The library gate's Active (library.Gate), read from the policy without building anything per row.
	if l := p.Library; l != nil && l.Enabled && p.Versions[key] != nil {
		lv := l.Versions[key]
		out.LibraryWrites = lv != nil && len(lv.Roots) > 0
	}
	out.Paused = p.Router == nil || !p.Router.Enabled
	out.PauseForced = slices.Contains(p.ForcedOff, "pause")
	active, _ := catalog.Searchable(p, []string{key})
	out.WillGrab = len(active) > 0
	return out
}
