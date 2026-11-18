package config

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func usersEnv(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func TestParseUsers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		env   func(string) string
		want  map[string]Role
		alias bool
		err   string
	}{
		{"unset", usersEnv(), nil, false, ""},
		{"three roles", usersEnv("MM_USERS", "alice:owner,bob:editor,carol:listener"), map[string]Role{"alice": RoleOwner, "bob": RoleEditor, "carol": RoleListener}, false, ""},
		{"bare name is owner", usersEnv("MM_USERS", "alice"), map[string]Role{"alice": RoleOwner}, false, ""},
		{"folded and trimmed", usersEnv("MM_USERS", " Alice:OWNER , Bob:Editor ,"), map[string]Role{"alice": RoleOwner, "bob": RoleEditor}, false, ""},
		{"alias one name", usersEnv("MM_TRUSTED_USERS", "Alice"), map[string]Role{"alice": RoleOwner}, true, ""},
		{"alias two names", usersEnv("MM_TRUSTED_USERS", "alice,bob"), nil, false, "exactly one name"},
		{"both set", usersEnv("MM_USERS", "alice", "MM_TRUSTED_USERS", "alice"), nil, false, "not both"},
		{"no owner", usersEnv("MM_USERS", "bob:editor"), nil, false, "found none"},
		{"two owners", usersEnv("MM_USERS", "alice:owner,bob:owner"), nil, false, "found 2"},
		{"duplicate after folding", usersEnv("MM_USERS", "alice:owner,ALICE:editor"), nil, false, "twice"},
		{"empty name", usersEnv("MM_USERS", ":owner"), nil, false, "empty name"},
		{"unknown role", usersEnv("MM_USERS", "alice:owner,bob:admin"), nil, false, "unknown role"},
		{"reserved owner", usersEnv("MM_USERS", "alice:owner,owner:editor"), nil, false, "reserved"},
		{"reserved critic", usersEnv("MM_USERS", "critic:owner"), nil, false, "reserved"},
		{"reserved system", usersEnv("MM_USERS", "alice:owner,System:listener"), nil, false, "reserved"},
		{"reserved user", usersEnv("MM_USERS", "alice:owner,user:listener"), nil, false, "reserved"},
		{"reserved human", usersEnv("MM_USERS", "alice:owner,human:listener"), nil, false, "reserved"},
		{"colon twice", usersEnv("MM_USERS", "alice:owner:x"), nil, false, "unknown role"},
		{"whitespace in name", usersEnv("MM_USERS", "al ice:owner"), nil, false, "whitespace"},
		{"only separators", usersEnv("MM_USERS", " , "), nil, false, "names no user"},
	} {
		got, alias, err := parseUsers(tc.env)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if diff := cmp.Diff(tc.want, got); diff != "" || alias != tc.alias {
			t.Errorf("%s: %s alias=%v", tc.name, diff, alias)
		}
	}
}

func TestLocalAuthRefusesNonOwnerUser(t *testing.T) {
	t.Parallel()
	base := func(users string) func(string) string {
		return usersEnv("MM_AUTH", "local", "MM_USERS", users, "MM_DATA_DIR", t.TempDir())
	}
	if _, errs := LoadAll(base("alice:owner")); len(errs) != 0 {
		t.Fatalf("owner only in local mode: %v", errs)
	}
	_, errs := LoadAll(base("alice:owner,bob:editor"))
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "Owner-only") {
		t.Fatalf("want the local-auth refusal, got %v", errs)
	}
}

func TestOwnerHelpers(t *testing.T) {
	t.Parallel()
	u := map[string]Role{"alice": RoleOwner, "bob": RoleEditor}
	if Owner(u) != "alice" || Owner(nil) != "" || !cmp.Equal(UserNames(u), []string{"alice", "bob"}) {
		t.Fatal("helpers")
	}
	if !cmp.Equal(OwnerUsers("Alice"), map[string]Role{"alice": RoleOwner}) {
		t.Fatal("OwnerUsers")
	}
}
