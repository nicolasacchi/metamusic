package auth_test

import (
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// TestRaterKey_Devices: a device or Subsonic client of an Editor or Listener rates as that user (so it can never write or
// read the Owner's row), the Owner's own device and the Owner rate as "owner", an agent as the critic.
func TestRaterKey_Devices(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		p    auth.Principal
		want string
	}{
		{"owner", auth.Principal{Kind: auth.KindHuman, Role: auth.RoleOwner, User: "alice"}, "owner"},
		{"editor", auth.Principal{Kind: auth.KindHuman, Role: auth.RoleEditor, User: "bob"}, "bob"},
		{"owner device before D1", auth.Principal{Kind: auth.KindDevice, ID: "d1"}, "owner"},
		{"owner device with owner role", auth.Principal{Kind: auth.KindDevice, Role: auth.RoleOwner, User: "alice"}, "owner"},
		{"listener device", auth.Principal{Kind: auth.KindDevice, Role: auth.RoleListener, User: "carol"}, "carol"},
		{"editor device", auth.Principal{Kind: auth.KindDevice, Role: auth.RoleEditor, User: "bob"}, "bob"},
		{"editor role without user fails closed", auth.Principal{Kind: auth.KindDevice, Role: auth.RoleEditor}, ""},
		{"listener role without user fails closed", auth.Principal{Kind: auth.KindHuman, Role: auth.RoleListener}, ""},
		{"agent", auth.Principal{Kind: auth.KindAgent}, "critic"},
		{"system", auth.Principal{Kind: auth.KindSystem}, ""},
		{"anonymous", auth.Principal{}, ""},
	} {
		if got := auth.RaterKey(c.p); got != c.want {
			t.Errorf("%s: RaterKey = %q, want %q", c.name, got, c.want)
		}
	}
}
