package importarr

import (
	"context"
	"net/url"

	"github.com/theatarr/theatarr/internal/arr"
)

// Getter is the read-only arr access the importer needs: GET /api/v3/<endpoint>?<query>, decoded into v. The
// GET-only client in internal/arr (*arr.Client) is the one used; every endpoint in [Endpoints] is on its allowlist.
type Getter interface {
	GetJSON(ctx context.Context, endpoint string, query url.Values, v any) error
}

var _ Getter = (*arr.Client)(nil)

// Endpoints are every arr endpoint the importer reads (relative to /api/v3/). All are GETs that change nothing.
var Endpoints = []string{"system/status", "rootfolder", "tag", "qualityprofile", "delayprofile", "releaseprofile",
	"movie", "moviefile", "series", "episode", "episodefile", "history", "blocklist", "collection",
	"customformat", "qualitydefinition", "config/naming", "config/mediamanagement", "config/indexer"}
