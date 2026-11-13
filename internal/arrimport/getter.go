package arrimport

import (
	"context"
	"net/url"

	"github.com/theatarr/theatarr/internal/arr"
)

// Getter is the read-only arr access the import needs: GET /api/v3/<endpoint>?<query>, decoded into v. The GET-only
// client in internal/arr (*arr.Client) is the one used; every endpoint ReadTitles reads is on its allowlist.
type Getter interface {
	GetJSON(ctx context.Context, endpoint string, query url.Values, v any) error
}

var _ Getter = (*arr.Client)(nil)
