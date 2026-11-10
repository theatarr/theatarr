package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// The create page's account: an active admin with the chosen password, signed in at once, and only while the table
// is empty. The password never reaches the log.
func TestCreateFirstAdmin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st := openStore(t)
	c := &clock{t: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	s, _, err := New(ctx, st, Options{Logger: log, Now: c.now, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	const pw = "my own secret pass"
	for _, bad := range []struct{ user, pw string }{{"", pw}, {"  ", pw}, {"a b", pw}, {strings.Repeat("x", 65), pw},
		{"admin", "short"}} {
		if _, err := s.CreateFirstAdmin(ctx, bad.user, bad.pw, "10.1.2.3", ""); !errors.As(err, new(InvalidAccount)) {
			t.Errorf("create %q/%q: %v", bad.user, bad.pw, err)
		}
	}
	if need, _ := s.NeedsFirstAccount(ctx); !need {
		t.Fatal("a refused create made an account")
	}
	tok, err := s.CreateFirstAdmin(ctx, " boss ", pw, "10.1.2.3", "ua")
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Authenticate(req(tok))
	if err != nil || id == nil || id.Username != "boss" || id.Role != RoleAdmin || !id.Perms.Has(PermAdmin) || !id.Local {
		t.Fatalf("identity %+v %v", id, err)
	}
	// Setup reads equal created and changed times on admin as a generated password; this one is chosen.
	var generated bool
	if err := st.Reader().QueryRow(`SELECT password_changed_at = created_at FROM web_user WHERE id = ?`, id.UserID).
		Scan(&generated); err != nil || generated {
		t.Errorf("generated %v %v", generated, err)
	}
	if _, err := s.Login(ctx, "boss", pw, "10.1.2.3", ""); err != nil {
		t.Errorf("login with the chosen password: %v", err)
	}
	if _, err := s.CreateFirstAdmin(ctx, "other", "another long password", "10.1.2.3", ""); !errors.Is(err, ErrClaimed) {
		t.Errorf("second create: %v", err)
	}
	if need, _ := s.NeedsFirstAccount(ctx); need {
		t.Error("needs an account after create")
	}
	if strings.Contains(logs.String(), pw) {
		t.Errorf("the log holds the password: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "created the first admin account") {
		t.Errorf("log: %s", logs.String())
	}
}

// Requests at once: one admin, the others ErrClaimed.
func TestCreateFirstAdminConcurrent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openStore(t)
	s, _ := newServiceEmpty(t, st)
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A busy refusal (two hashes at once) is retried, as the page would.
			for {
				_, err := s.CreateFirstAdmin(ctx, "admin"+string(rune('a'+i)), "a long enough password", "10.1.2.3", "")
				var thr *Throttled
				if !errors.As(err, &thr) {
					errs[i] = err
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrClaimed):
			t.Errorf("create: %v", err)
		}
	}
	var users int
	if err := st.Reader().QueryRow(`SELECT COUNT(*) FROM web_user`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if ok != 1 || users != 1 {
		t.Errorf("%d creates succeeded, %d users", ok, users)
	}
}

// Plex sign-in cannot make the first account (it would be a user, and the install would have no admin); once an
// account exists it creates Plex accounts as before.
func TestPlexSignInNeedsAnAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openStore(t)
	s, _ := newServiceEmpty(t, st)
	if _, _, _, err := s.PlexSignIn(ctx, PlexAccount{ID: 7, Username: "viewer"}); !errors.Is(err, ErrNoAdminYet) {
		t.Fatalf("plex on an empty table: %v", err)
	}
	if need, _ := s.NeedsFirstAccount(ctx); !need {
		t.Fatal("plex made an account")
	}
	if _, err := s.CreateFirstAdmin(ctx, "admin", "a long enough password", "10.1.2.3", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := s.PlexSignIn(ctx, PlexAccount{ID: 7, Username: "viewer"}); err != nil || !created {
		t.Fatalf("plex after the admin: %v %v", created, err)
	}
}

// newServiceEmpty starts the service on st and makes no account.
func newServiceEmpty(t *testing.T, st *store.Store) (*Service, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	s, _, err := New(context.Background(), st, Options{Logger: slog.New(slog.DiscardHandler), Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}
