package cardigann

import (
	"context"
	"testing"
)

// Cookies saved before the login ran (a Cloudflare check passed first) are not a login: a restart must sign in
// again rather than search with a session that never logged in.
func TestSessionSavedBeforeLoginIsNotLoggedIn(t *testing.T) {
	t.Parallel()
	store := &memSessions{saved: map[string]SavedSession{}}
	m := &Manager{Sessions: store}
	s, err := newSession(m, &Definition{Login: &LoginBlock{}}, Instance{Key: "ix9", BaseURL: "https://site.example/"})
	if err != nil {
		t.Fatal(err)
	}
	s.persist(context.Background())
	if store.saved["ix9"].LoggedIn {
		t.Fatalf("saved as logged in before any login: %+v", store.saved["ix9"])
	}
	s.loggedIn = true
	s.persist(context.Background())
	if !store.saved["ix9"].LoggedIn {
		t.Fatal("a real login was not saved as one")
	}
}
