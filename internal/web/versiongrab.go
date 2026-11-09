package web

import (
	"slices"

	"github.com/theatarr/theatarr/internal/catalog"
	"github.com/theatarr/theatarr/internal/library"
	"github.com/theatarr/theatarr/internal/policy"
)

// VersionGrabState is the runtime gates for a version, independent of search and monitoring permissions.
type VersionGrabState struct {
	WillGrab        bool   `json:"will_grab"`
	Mode            string `json:"mode"`
	Grab            bool   `json:"grab"`
	LibraryEnabled  bool   `json:"library_enabled"`
	RouterEnabled   bool   `json:"router_enabled"`
	RouterForcedOff bool   `json:"router_forced_off"`
	// LibraryWrites is the library's own write gate (library.Gate.Active): the switch on, and the version mode:
	// active, not followed and listed under library.versions with roots.
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
	out := VersionGrabState{Mode: "off"}
	if p == nil {
		return out
	}
	out.LibraryEnabled = p.Library != nil && p.Library.Enabled
	out.LibraryWrites = library.NewGate(p).Active(key)
	out.RouterEnabled = p.Router != nil && p.Router.Enabled
	out.RouterForcedOff = slices.Contains(p.ForcedOff, "router")
	if v := p.Versions[key]; v != nil {
		out.Mode, out.Grab = string(v.Mode), v.Grab
		if out.Mode == "" {
			out.Mode = string(policy.ModeShadow)
		}
	}
	active, _ := catalog.Searchable(p, []string{key})
	out.WillGrab = len(active) > 0
	return out
}
