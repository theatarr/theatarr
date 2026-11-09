package importarr

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/theatarr/theatarr/internal/library/naming"
)

func fetchPolicySnapshot(ctx context.Context, g Getter, s *Snapshot) error {
	for _, ep := range []string{"customformat", "qualitydefinition"} {
		var body json.RawMessage
		if err := g.GetJSON(ctx, ep, nil, &body); err != nil {
			return fmt.Errorf("policy snapshot %s: %w", ep, err)
		}
		s.PolicySnapshot[ep] = body
	}
	var body json.RawMessage
	if err := g.GetJSON(ctx, "config/naming", nil, &body); err != nil {
		return fmt.Errorf("naming snapshot: %w", err)
	}
	if _, err := naming.ParseArrConfig(body); err != nil {
		return err
	}
	s.PolicySnapshot["config/naming"] = body
	// Keep only settings the decision engine reads, not unrelated arr configuration.
	var mm struct {
		Propers string `json:"downloadPropersAndRepacks"`
	}
	if err := g.GetJSON(ctx, "config/mediamanagement", nil, &mm); err != nil {
		return err
	}
	var ic struct {
		MaximumSize        int  `json:"maximumSize"`
		PreferIndexerFlags bool `json:"preferIndexerFlags"`
	}
	if err := g.GetJSON(ctx, "config/indexer", nil, &ic); err != nil {
		return err
	}
	s.PolicySnapshot["config/mediamanagement"], _ = json.Marshal(mm)
	s.PolicySnapshot["config/indexer"], _ = json.Marshal(ic)
	return nil
}
