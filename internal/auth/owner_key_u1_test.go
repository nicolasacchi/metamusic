package auth

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

var (
	testUsers = map[string]Role{"alice": RoleOwner, "bob": RoleEditor, "carol": RoleListener}
	bobH      = Principal{Kind: KindHuman, ID: "bob", Name: "bob", User: "bob", Role: RoleEditor, Scopes: ScopesFor(RoleEditor), Via: "proxy"}
	carolH    = Principal{Kind: KindHuman, ID: "carol", Name: "carol", User: "carol", Role: RoleListener, Scopes: ScopesFor(RoleListener), Via: "proxy"}
)

func TestOwnerKeyOf(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		p    Principal
		key  string
		ok   bool
	}{
		{"owner", human, "owner", true},
		{"editor", bobH, "bob", true},
		{"listener", carolH, "carol", true},
		{"role without user", Principal{Kind: KindHuman, Role: RoleListener}, "", false},
		{"human without role", Principal{Kind: KindHuman, User: "dave"}, "", false},
		{"owner device", Principal{Kind: KindDevice, ID: "d1"}, "", false},
		{"listener device", Principal{Kind: KindDevice, ID: "d1", Role: RoleListener, User: "carol"}, "", false},
		{"agent", Principal{Kind: KindAgent, ID: "t"}, "", false},
		{"anonymous", Principal{}, "", false},
	} {
		if key, ok := OwnerKeyOf(c.p); key != c.key || ok != c.ok {
			t.Errorf("%s: OwnerKeyOf = %q, %v; want %q, %v", c.name, key, ok, c.key, c.ok)
		}
	}
	if !SeesAllDevices(human) || SeesAllDevices(bobH) || SeesAllDevices(Principal{Kind: KindDevice}) {
		t.Error("only the Owner's login sees every device")
	}
}

// TestOwnerIdentityClamp pins C301.3 and C314.1: stored read,write (and read only, write only) give the Owner's key the stored
// scopes unchanged and an Editor or Listener exactly { x in ScopesFor(role) : stored.Has(x) }; a key missing from the user map,
// a key naming the Owner and an unknown key are refused; a device never gains generate or admin.
func TestOwnerIdentityClamp(t *testing.T) {
	t.Parallel()
	rw, r, w := []Scope{ScopeRead, ScopeWrite}, []Scope{ScopeRead}, []Scope{ScopeWrite}
	for _, c := range []struct {
		key    string
		stored []Scope
		role   Role
		user   string
		want   []Scope
	}{
		{"owner", rw, "", "", rw},
		{"owner", r, "", "", r},
		{"bob", rw, RoleEditor, "bob", []Scope{ScopeListen, ScopeRead, ScopeRate, ScopeWrite}},
		{"bob", r, RoleEditor, "bob", []Scope{ScopeListen, ScopeRead}},
		{"bob", w, RoleEditor, "bob", []Scope{ScopeRate, ScopeWrite}},
		{"carol", rw, RoleListener, "carol", []Scope{ScopeListen, ScopeRate}},
		{"carol", r, RoleListener, "carol", []Scope{ScopeListen}},
		{"carol", w, RoleListener, "carol", []Scope{ScopeRate}},
	} {
		role, user, got, ok := ownerIdentity(testUsers, c.key, c.stored)
		if !ok || role != c.role || user != c.user || !slices.Equal(got, c.want) {
			t.Errorf("%s %v: got %q %q %v %v, want %q %q %v", c.key, c.stored, role, user, got, ok, c.role, c.user, c.want)
		}
		for _, banned := range []Scope{ScopeGenerate, ScopeAdmin} {
			if slices.Contains(got, banned) && c.key != "owner" {
				t.Errorf("%s: device holds %s", c.key, banned)
			}
		}
	}
	for _, key := range []string{"dave", "alice", "", "OWNER", "critic"} { // removed user, the Owner's username, empty
		if _, _, _, ok := ownerIdentity(testUsers, key, []Scope{ScopeRead}); ok {
			t.Errorf("key %q resolved", key)
		}
	}
	if _, _, _, ok := ownerIdentity(nil, "bob", []Scope{ScopeRead}); ok {
		t.Error("a key resolved against no user map")
	}
}

func TestDeviceOwnership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ds, _ := newDevices(t)
	ds.UseUsers(testUsers)
	pair := func(by Principal, name string) (string, DeviceInfo) {
		t.Helper()
		code, _, err := ds.CreatePairingCode(ctx, by, name)
		if err != nil {
			t.Fatalf("code for %s: %v", by.ID, err)
		}
		cookie, dev, err := ds.Redeem(ctx, code, "ua")
		if err != nil {
			t.Fatalf("redeem for %s: %v", by.ID, err)
		}
		return cookie, dev
	}
	ownC, ownD := pair(human, "Own phone")
	carolC, carolD := pair(carolH, "Carol phone")
	_, bobD := pair(bobH, "Bob phone")
	if ownD.OwnerKey != "owner" || carolD.OwnerKey != "carol" || bobD.OwnerKey != "bob" {
		t.Fatalf("owner keys: %q %q %q", ownD.OwnerKey, carolD.OwnerKey, bobD.OwnerKey)
	}

	// principals: the Owner's device is unchanged (no role, no user, read and write); a Listener's carries role, user and the clamp
	op, err := ds.VerifyDevice(ctx, ownC)
	if err != nil || op.Role != "" || op.User != "" || !slices.Equal(op.Scopes, []Scope{ScopeRead, ScopeWrite}) || !op.IsOwnerSide() || RaterKey(op) != "owner" {
		t.Errorf("owner device: %+v %v", op, err)
	}
	cp, err := ds.VerifyDevice(ctx, carolC)
	if err != nil || cp.Role != RoleListener || cp.User != "carol" || !slices.Equal(cp.Scopes, []Scope{ScopeListen, ScopeRate}) ||
		cp.IsOwnerSide() || cp.CanSeePrivate() || RaterKey(cp) != "carol" || cp.Key() != "device:"+carolD.ID {
		t.Errorf("listener device: %+v %v", cp, err)
	}

	// listing and revoking: own only, the Owner sees and revokes all with the owner column filled
	if l, _ := ds.List(ctx, carolH); len(l) != 1 || l[0].ID != carolD.ID {
		t.Errorf("listener list: %+v", l)
	}
	if l, _ := ds.List(ctx, human); len(l) != 3 {
		t.Errorf("owner list: %d", len(l))
	}
	if l, err := ds.List(ctx, cp); err != nil || len(l) != 0 { // a device never lists
		t.Errorf("device list: %+v %v", l, err)
	}
	if _, err := ds.Revoke(ctx, carolH, bobD.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign revoke: %v", err)
	}
	if _, err := ds.Revoke(ctx, carolH, ownD.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoke of the Owner's device by a Listener: %v", err)
	}
	if _, err := ds.Revoke(ctx, cp, carolD.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a device revoking through the user path: %v", err)
	}
	if _, err := ds.Revoke(ctx, carolH, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown revoke: %v", err)
	}
	if di, err := ds.Revoke(ctx, human, bobD.ID); err != nil || di.RevokedAt == "" {
		t.Errorf("owner revoke of bob's device: %+v %v", di, err)
	}
	if di, err := ds.Revoke(ctx, carolH, carolD.ID); err != nil || di.RevokedAt == "" {
		t.Errorf("own revoke: %+v %v", di, err)
	}
	if _, err := ds.VerifyDevice(ctx, carolC); !errors.Is(err, ErrInvalidDevice) {
		t.Errorf("revoked device verifies: %v", err)
	}

	// a device or agent never pairs
	for _, by := range []Principal{cp, op, {Kind: KindAgent, ID: "t"}, {Kind: KindHuman, User: "x"}} {
		if _, _, err := ds.CreatePairingCode(ctx, by, "x"); !errors.Is(err, ErrNotHuman) {
			t.Errorf("%v paired: %v", by.Kind, err)
		}
	}
}

// TestDeviceRoleResolvedPerRequest: the user map is read on every request, so a device whose owner left the map answers invalid
// (never the Owner), and a code redeemed after its creator left (or filled the limit) is the generic invalid code.
func TestDeviceRoleResolvedPerRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ds, _ := newDevices(t)
	ds.UseUsers(testUsers)
	code, _, err := ds.CreatePairingCode(ctx, carolH, "Phone")
	if err != nil {
		t.Fatal(err)
	}
	cookie, _, err := ds.Redeem(ctx, code, "ua")
	if err != nil {
		t.Fatal(err)
	}
	ds.UseUsers(map[string]Role{"alice": RoleOwner, "carol": RoleEditor}) // a restart with carol promoted: clamped to the new role
	p, err := ds.VerifyDevice(ctx, cookie)
	if err != nil || p.Role != RoleEditor || !slices.Equal(p.Scopes, []Scope{ScopeListen, ScopeRead, ScopeRate, ScopeWrite}) {
		t.Errorf("promoted: %+v %v", p, err)
	}
	ds.UseUsers(map[string]Role{"alice": RoleOwner}) // a restart with carol removed
	if _, err := ds.VerifyDevice(ctx, cookie); !errors.Is(err, ErrInvalidDevice) {
		t.Errorf("removed owner's device verifies: %v", err)
	}
	code, _, err = ds.CreatePairingCode(ctx, carolH, "Phone 2")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ds.Redeem(ctx, code, "ua"); !errors.Is(err, ErrInvalidCode) {
		t.Errorf("redeem for a removed user: %v", err)
	}
	ds2, _ := newDevices(t) // no user map at all: only the Owner's devices work
	code, _, _ = ds2.CreatePairingCode(ctx, carolH, "Phone")
	if _, _, err := ds2.Redeem(ctx, code, "ua"); !errors.Is(err, ErrInvalidCode) {
		t.Errorf("redeem with no user map: %v", err)
	}
}

func TestDeviceLimitAndCodeIsolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ds, _ := newDevices(t)
	ds.UseUsers(testUsers)
	var last DeviceInfo
	for i := range MaxUserDevices {
		code, _, err := ds.CreatePairingCode(ctx, carolH, "Phone")
		if err != nil {
			t.Fatalf("code %d: %v", i, err)
		}
		if _, last, err = ds.Redeem(ctx, code, "ua"); err != nil {
			t.Fatalf("redeem %d: %v", i, err)
		}
	}
	if _, _, err := ds.CreatePairingCode(ctx, carolH, "Sixth"); !errors.Is(err, ErrDeviceLimit) {
		t.Errorf("6th code: %v", err)
	}
	if _, _, err := ds.CreatePairingCode(ctx, bobH, "Bob"); err != nil { // another user is unaffected
		t.Errorf("bob: %v", err)
	}
	for range MaxUserDevices + 2 { // the Owner is unlimited
		code, _, err := ds.CreatePairingCode(ctx, human, "Own")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := ds.Redeem(ctx, code, "ua"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ds.Revoke(ctx, carolH, last.ID); err != nil {
		t.Fatal(err)
	}
	code, _, err := ds.CreatePairingCode(ctx, carolH, "After revoke")
	if err != nil {
		t.Fatalf("revoking frees a slot: %v", err)
	}
	// a code made while a slot is free but redeemed after the slots filled by another path is the generic invalid code
	code2, _, err := ds.CreatePairingCode(ctx, carolH, "Racing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ds.st.W.ExecContext(ctx, `INSERT INTO devices(id,name,prefix,hmac_hash,scopes,user_agent,paired_by,created_at,expires_at,owner_key)
		VALUES('x1','P','a','hx','read,write','','human:carol','2026-10-01T10:00:00Z','2027-01-01T10:00:00Z','carol')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ds.Redeem(ctx, code2, "ua"); !errors.Is(err, ErrInvalidCode) {
		t.Errorf("redeem over the limit: %v", err)
	}
	if _, _, err := ds.Redeem(ctx, code, "ua"); !errors.Is(err, ErrInvalidCode) { // code2's creation superseded code
		t.Errorf("the superseded code redeems: %v", err)
	}

	// a Listener's new code does not supersede the Owner's live code, and the reverse
	ownCode, _, _ := ds.CreatePairingCode(ctx, human, "Own")
	if _, _, err := ds.CreatePairingCode(ctx, bobH, "Bob again"); err != nil {
		t.Fatal(err)
	}
	if _, dev, err := ds.Redeem(ctx, ownCode, "ua"); err != nil || dev.OwnerKey != "owner" {
		t.Errorf("the Owner's code after bob's: %+v %v", dev, err)
	}
	if !strings.Contains(ErrDeviceLimit.Error(), "limit") {
		t.Error("limit error text")
	}
}
