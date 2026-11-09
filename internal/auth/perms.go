package auth

import (
	"fmt"
	"slices"
)

// Roles and permissions (docs/design/ui.md, docs/design/users.md). A role is a preset of permission bits; an admin can
// add bits per user (web_user.permissions), and auto_approve is its own column. A request's permissions are its
// user's, whether it came with a session or an API key.

// Roles.
const (
	RoleAdmin   = "admin"
	RoleManager = "manager"
	RoleUser    = "user"
)

// ValidRole reports whether r is a role.
func ValidRole(r string) bool { return r == RoleAdmin || r == RoleManager || r == RoleUser }

// Account states.
const (
	StatusActive   = "active"
	StatusPending  = "pending"
	StatusDisabled = "disabled"
)

// Perm is a set of permission bits.
type Perm uint32

// Permission bits. The values are stored (web_user.permissions): never renumber them.
const (
	// PermBrowse: the library, title pages, the calendar, search, own activity and own requests.
	PermBrowse Perm = 1 << iota
	// PermRequest: ask for a title or a version.
	PermRequest
	// PermAutoApprove: one's requests apply at once.
	PermAutoApprove
	// PermAdd: add titles and turn versions on or off directly, without a request.
	PermAdd
	// PermManageRequests: see every request, approve and decline.
	PermManageRequests
	// PermSeeFiles: file paths, hashes, release names, sizes, scores, providers, arr records.
	PermSeeFiles
	// PermManage: search now, the releases panel, refresh art.
	PermManage
	// PermOwnKeys: create and revoke one's own API keys.
	PermOwnKeys
	// PermAdmin: the admin panel (system, jobs, shadow, registry, policy, logs, journal, metadata review) and the
	// device routes.
	PermAdmin
	// PermUsers: users, their settings and everyone's API keys.
	PermUsers
)

// PermAll is every bit.
const PermAll = PermBrowse | PermRequest | PermAutoApprove | PermAdd | PermManageRequests | PermSeeFiles | PermManage |
	PermOwnKeys | PermAdmin | PermUsers

var permNames = []struct {
	p    Perm
	name string
}{
	{PermBrowse, "browse"}, {PermRequest, "request"}, {PermAutoApprove, "auto_approve"}, {PermAdd, "add"},
	{PermManageRequests, "manage_requests"}, {PermSeeFiles, "see_files"}, {PermManage, "manage"},
	{PermOwnKeys, "own_keys"}, {PermAdmin, "admin"}, {PermUsers, "users"},
}

// RolePerms is a role's preset. An unknown role has none.
func RolePerms(role string) Perm {
	switch role {
	case RoleAdmin:
		return PermAll
	case RoleManager:
		return PermBrowse | PermRequest | PermAutoApprove | PermAdd | PermManageRequests | PermSeeFiles | PermManage |
			PermOwnKeys
	case RoleUser:
		return PermBrowse | PermRequest
	}
	return 0
}

// OptionalUserPerms are the bits an admin may add to an account beyond its role (the users screen's ticks).
const OptionalUserPerms = PermAutoApprove | PermOwnKeys

// Has reports whether p holds every bit of q.
func (p Perm) Has(q Perm) bool { return p&q == q }

// Names lists p's bits by name, in bit order.
func (p Perm) Names() []string {
	out := []string{}
	for _, n := range permNames {
		if p&n.p != 0 {
			out = append(out, n.name)
		}
	}
	return out
}

// String is the names joined.
func (p Perm) String() string {
	if p == 0 {
		return "none"
	}
	return fmt.Sprint(p.Names())
}

// ParsePerms turns names into bits.
func ParsePerms(names []string) (Perm, error) {
	var p Perm
	for _, s := range names {
		i := slices.IndexFunc(permNames, func(n struct {
			p    Perm
			name string
		}) bool {
			return n.name == s
		})
		if i < 0 {
			return 0, fmt.Errorf("unknown permission %q", s)
		}
		p |= permNames[i].p
	}
	return p, nil
}

// Effective is an account's permissions: its role's preset, its extra bits (only OptionalUserPerms count, so a
// stored admin bit grants nothing), and auto-approve.
func Effective(role string, extra Perm, autoApprove bool) Perm {
	p := RolePerms(role) | extra&OptionalUserPerms
	if autoApprove {
		p |= PermAutoApprove
	}
	return p
}
