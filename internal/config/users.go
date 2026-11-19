package config

import (
	"fmt"
	"slices"
	"strings"
)

// Role is a user's role (U1 D-U1-1). It lives here, not in auth, because auth imports config; auth.Role is an alias.
type Role string

const (
	RoleOwner    Role = "owner"
	RoleEditor   Role = "editor"
	RoleListener Role = "listener"
)

// Valid reports whether r is one of the three roles.
func (r Role) Valid() bool { return r == RoleOwner || r == RoleEditor || r == RoleListener }

// reservedUsers can never name a user: they are rater keys, principal ids or role words (D-U1-3).
var reservedUsers = []string{"owner", "critic", "system", "user", "human"}

// Owner returns the name of the single Owner of users, or "" when there is none.
func Owner(users map[string]Role) string {
	for name, r := range users {
		if r == RoleOwner {
			return name
		}
	}
	return ""
}

// OwnerUsers is the one-owner map that MM_TRUSTED_USERS=<name> stands for.
func OwnerUsers(name string) map[string]Role {
	return map[string]Role{strings.ToLower(name): RoleOwner}
}

// UserNames lists the configured names, sorted.
func UserNames(users map[string]Role) []string {
	out := make([]string, 0, len(users))
	for n := range users {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// parseUsers resolves MM_USERS (name:role pairs, a bare name meaning owner) or the deprecated MM_TRUSTED_USERS alias
// into the user map (C301.2). Names are folded to lowercase. Errors name the offending entry and never panic. An
// unset pair of variables yields a nil map (no user can log in through the proxy), as MM_TRUSTED_USERS unset did.
func parseUsers(getenv func(string) string) (users map[string]Role, aliasUsed bool, err error) {
	raw, trusted := strings.TrimSpace(getenv("MM_USERS")), splitList(getenv("MM_TRUSTED_USERS"))
	switch {
	case raw != "" && len(trusted) > 0:
		return nil, false, fmt.Errorf("config: set MM_USERS or the deprecated MM_TRUSTED_USERS, not both")
	case raw == "" && len(trusted) == 0:
		return nil, false, nil
	case raw == "":
		if len(trusted) != 1 {
			return nil, false, fmt.Errorf("config: MM_TRUSTED_USERS must hold exactly one name (the Owner); use MM_USERS=name:role,... for several users")
		}
		raw, aliasUsed = trusted[0]+":owner", true
	}
	users = map[string]Role{}
	for _, entry := range strings.Split(raw, ",") {
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		name, role, hasRole := strings.Cut(entry, ":")
		name, role = strings.ToLower(strings.TrimSpace(name)), strings.ToLower(strings.TrimSpace(role))
		if !hasRole {
			role = string(RoleOwner)
		}
		switch {
		case name == "":
			return nil, false, fmt.Errorf("config: MM_USERS entry %q has an empty name", entry)
		case strings.ContainsAny(name, ",:") || strings.ContainsFunc(name, func(r rune) bool { return r <= ' ' || r == 0x7f }):
			return nil, false, fmt.Errorf("config: MM_USERS name %q must not contain commas, colons or whitespace", name)
		case slices.Contains(reservedUsers, name):
			return nil, false, fmt.Errorf("config: MM_USERS name %q is reserved", name)
		case !Role(role).Valid():
			return nil, false, fmt.Errorf("config: MM_USERS user %q has unknown role %q (owner, editor or listener)", name, role)
		}
		if _, dup := users[name]; dup {
			return nil, false, fmt.Errorf("config: MM_USERS user %q appears twice (names are case-insensitive)", name)
		}
		users[name] = Role(role)
	}
	owners := 0
	for _, r := range users {
		if r == RoleOwner {
			owners++
		}
	}
	switch {
	case len(users) == 0:
		return nil, false, fmt.Errorf("config: MM_USERS names no user")
	case owners == 0:
		return nil, false, fmt.Errorf("config: MM_USERS needs exactly one owner, found none")
	case owners > 1:
		return nil, false, fmt.Errorf("config: MM_USERS needs exactly one owner, found %d", owners)
	}
	return users, aliasUsed, nil
}

// checkUsersAuth refuses a non-owner user in local-auth mode: local auth is the single-Owner dev mode (C301.5).
func checkUsersAuth(c *Config) error {
	if c.AuthMode != "local" {
		return nil
	}
	for name, r := range c.Users {
		if r != RoleOwner {
			return fmt.Errorf("config: MM_AUTH=local is Owner-only; user %q has role %s", name, r)
		}
	}
	return nil
}
