package web

import (
	"context"
	"slices"
	"testing"

	"github.com/theatarr/theatarr/internal/auth"
)

// Extra permissions are the users screen's optional ticks only. Admin-level bits are refused, and a
// stored one (or one a direct UpdateUser call passes) grants nothing beyond the role.
func TestExtraPermissionsAreOptionalBitsOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	c := e.login()
	var me Me
	e.jsonCall("GET", APIBase+"/auth/me", nil, c, "", &me)
	uid := e.account("bob", auth.RoleUser, auth.StatusActive, true)
	for _, bits := range [][]string{{"admin"}, {"users"}, {"manage"}, {"own_keys", "see_files"}} {
		if code := e.jsonCall("PATCH", APIBase+"/users/"+itoa(uid), map[string]any{"extra_permissions": bits}, c, me.CSRFToken, nil); code != 400 {
			t.Errorf("extra_permissions %v: %d, want 400", bits, code)
		}
	}
	all := auth.PermAll
	if err := e.auth.UpdateUser(context.Background(), uid, auth.UserPatch{ExtraPerms: &all}); err != nil {
		t.Fatal(err)
	}
	u, err := e.auth.GetUser(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(u.Permissions, "admin") || slices.Contains(u.Permissions, "users") || slices.Contains(u.Permissions, "manage") {
		t.Errorf("a user account holds %v", u.Permissions)
	}
	if _, err := e.st.Writer().Exec(`UPDATE web_user SET permissions = ? WHERE id = ?`, int64(auth.PermAll), uid); err != nil {
		t.Fatal(err)
	}
	if u, err = e.auth.GetUser(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(u.Permissions, "admin") {
		t.Errorf("a stored admin bit counts: %v", u.Permissions)
	}
}
