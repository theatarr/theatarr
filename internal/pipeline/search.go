// Package pipeline holds service wiring shared by theatarr serve and the in-process end-to-end tests.
package pipeline

import (
	"github.com/theatarr/theatarr/internal/acquire"
	"github.com/theatarr/theatarr/internal/metadata/tvrefresh"
)

// NewAcquirer connects acquisition to the series refresh: searches wait for the first episode list, and a
// completed refresh releases them. The caller registers both services' jobs before starting workers.
func NewAcquirer(cfg acquire.Config, metadata *tvrefresh.Service) (*acquire.Acquirer, error) {
	ac, err := acquire.New(cfg)
	if err != nil {
		return nil, err
	}
	if metadata.Enabled() {
		metadata.SetSearches(ac)
		ac.SetMetadata(metadata)
	}
	return ac, nil
}
