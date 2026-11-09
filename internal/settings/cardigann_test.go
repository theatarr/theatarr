package settings

import (
	"context"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/indexer/cardigann"
)

func TestCardigannSessionsRoundTrip(t *testing.T) {
	t.Parallel()
	_, db, _ := setup(t)
	ctx := context.Background()
	c, err := NewCardigannSessions(db)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := c.LoadSession(ctx, "site"); err != nil || s != nil {
		t.Fatalf("empty: %+v %v", s, err)
	}
	saved := cardigann.SavedSession{Key: "k1", Cookies: []cardigann.SavedCookie{{Host: "site.test", Name: "sid", Value: inventedSecret}},
		LoggedIn: true, SavedAt: time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)}
	for i := 0; i < 2; i++ {
		if err := c.SaveSession(ctx, "site", saved); err != nil {
			t.Fatal(err)
		}
		saved.Key = "k2"
	}
	got, err := c.LoadSession(ctx, "site")
	if err != nil || got == nil || got.Key != "k2" || len(got.Cookies) != 1 || got.Cookies[0].Value != inventedSecret || !got.LoggedIn {
		t.Fatalf("loaded %+v %v", got, err)
	}
	if err := c.DeleteSession(ctx, "site"); err != nil {
		t.Fatal(err)
	}
	if s, _ := c.LoadSession(ctx, "site"); s != nil {
		t.Fatal("deleted session still there")
	}
}
