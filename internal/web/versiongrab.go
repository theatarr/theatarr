package web

import (
	"slices"

	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
)

// VersionGrabState is the runtime gates for a version, independent of search and monitoring permissions.
type VersionGrabState struct {
	WillGrab    bool   `json:"will_grab"`
	Mode        string `json:"mode"`
	Paused      bool   `json:"paused"`
	PauseForced bool   `json:"pause_forced"`
	// LibraryWrites is the library's own write gate (library.Gate.Active): Pause off, and the version mode:
	// active and listed under library.versions with roots.
	LibraryWrites bool `json:"library_writes"`
}

// VersionGrabSummary is a configured version's runtime gates on Overview and Setup.
type VersionGrabSummary struct {
	Key       string           `json:"key"`
	GrabState VersionGrabState `json:"grab_state"`
}

func versionGrabSummaries(p *policy.Policy) []VersionGrabSummary {
	out := []VersionGrabSummary{}
	if p != nil {
		for _, key := range sortedKeys(p.Versions) {
			out = append(out, VersionGrabSummary{Key: key, GrabState: versionGrabState(p, key)})
		}
	}
	return out
}

func versionGrabState(p *policy.Policy, key string) VersionGrabState {
	out := VersionGrabState{Mode: "off", Paused: true}
	if p == nil {
		return out
	}
	out.LibraryWrites = library.NewGate(p).Active(key)
	out.Paused = p.Router == nil || !p.Router.Enabled
	out.PauseForced = slices.Contains(p.ForcedOff, "pause")
	if v := p.Versions[key]; v != nil {
		out.Mode = string(v.Mode)
		if out.Mode == "" {
			out.Mode = string(policy.ModeShadow)
		}
	}
	active, _ := catalog.Searchable(p, []string{key})
	out.WillGrab = len(active) > 0
	return out
}
