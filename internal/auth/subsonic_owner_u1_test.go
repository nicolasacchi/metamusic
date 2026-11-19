package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestSubsonicOwnership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newSubRig(t)
	r.s.UseUsers(testUsers)
	mk := func(by Principal, name string, write bool) (SubsonicClient, SubsonicSecrets) {
		t.Helper()
		c, sec, err := r.s.Create(ctx, by, NewSubsonicClient{Name: name, Write: write, APIKey: true, Password: true})
		if err != nil {
			t.Fatalf("create for %s: %v", by.ID, err)
		}
		return c, sec
	}
	ownC, ownSec := mk(human, "Own app", true)
	carolC, carolSec := mk(carolH, "Carol app", true)
	_, roSec := mk(carolH, "Carol read-only", false)
	bobC, bobSec := mk(bobH, "Bob app", true)
	if ownC.OwnerKey != "owner" || carolC.OwnerKey != "carol" || bobC.OwnerKey != "bob" {
		t.Fatalf("owner keys %q %q %q", ownC.OwnerKey, carolC.OwnerKey, bobC.OwnerKey)
	}

	// principals by API key and by token: the Owner's client is unchanged, others carry role, user and the clamp
	p, err := r.s.VerifyAPIKey(ctx, ownSec.APIKey, "App")
	if err != nil || p.Role != "" || p.User != "" || !slices.Equal(p.Scopes, []Scope{ScopeRead, ScopeWrite}) || RaterKey(p) != "owner" || !p.IsOwnerSide() {
		t.Errorf("owner client: %+v %v", p, err)
	}
	p, err = r.s.VerifyAPIKey(ctx, carolSec.APIKey, "App")
	if err != nil || p.Role != RoleListener || p.User != "carol" || !slices.Equal(p.Scopes, []Scope{ScopeListen, ScopeRate}) || RaterKey(p) != "carol" || p.IsOwnerSide() {
		t.Errorf("listener client: %+v %v", p, err)
	}
	if p, err = r.s.VerifyAPIKey(ctx, roSec.APIKey, "App"); err != nil || !slices.Equal(p.Scopes, []Scope{ScopeListen}) {
		t.Errorf("read-only listener client: %+v %v", p, err)
	}
	salt := "abcdef"
	p, err = r.s.VerifyToken(ctx, bobC.Username, subToken(bobSec.Password, salt), salt, "App", SaltWindowAPI)
	if err != nil || p.Role != RoleEditor || p.User != "bob" || !slices.Equal(p.Scopes, []Scope{ScopeListen, ScopeRead, ScopeRate, ScopeWrite}) {
		t.Errorf("editor client by token: %+v %v", p, err)
	}

	// listing and revoking: own only; the Owner sees and revokes all
	if l, _ := r.s.List(ctx, carolH); len(l) != 2 {
		t.Errorf("listener list: %d", len(l))
	}
	if l, _ := r.s.List(ctx, human); len(l) != 4 {
		t.Errorf("owner list: %d", len(l))
	}
	if l, err := r.s.List(ctx, p); err != nil || len(l) != 0 {
		t.Errorf("a client lists: %v %v", l, err)
	}
	if _, err := r.s.Revoke(ctx, carolH, bobC.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign revoke: %v", err)
	}
	if _, err := r.s.Revoke(ctx, carolH, ownC.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoke of the Owner's client: %v", err)
	}
	if _, err := r.s.Revoke(ctx, p, bobC.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a client revoking: %v", err)
	}
	if _, err := r.s.Revoke(ctx, carolH, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown revoke: %v", err)
	}
	if c, err := r.s.Revoke(ctx, human, bobC.ID); err != nil || c.RevokedAt == nil {
		t.Errorf("owner revoke: %+v %v", c, err)
	}
	if c, err := r.s.Client(ctx, carolC.ID); err != nil || c.OwnerKey != "carol" {
		t.Errorf("Client: %+v %v", c, err)
	}
	if _, err := r.s.Client(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Client unknown: %v", err)
	}
	// nobody but a human login creates one
	for _, by := range []Principal{p, {Kind: KindAgent, ID: "t"}, {Kind: KindHuman, User: "x"}} {
		if _, _, err := r.s.Create(ctx, by, NewSubsonicClient{Name: "x", APIKey: true}); !errors.Is(err, ErrNotHuman) {
			t.Errorf("%v created: %v", by.Kind, err)
		}
	}

	// a restart that removed carol: her credentials fail as bad credentials, never as the Owner
	r.s.UseUsers(map[string]Role{"alice": RoleOwner, "bob": RoleEditor})
	if _, err := r.s.VerifyAPIKey(ctx, carolSec.APIKey, "App"); !errors.Is(err, ErrSubsonicAPIKey) {
		t.Errorf("removed owner's key: %v", err)
	}
	salt2 := "ghijkl"
	if _, err := r.s.VerifyToken(ctx, carolC.Username, subToken(carolSec.Password, salt2), salt2, "App", SaltWindowAPI); !errors.Is(err, ErrSubsonicCredentials) {
		t.Errorf("removed owner's token: %v", err)
	}
	if _, err := r.s.VerifyAPIKey(ctx, ownSec.APIKey, "App"); err != nil {
		t.Errorf("the Owner's key after the restart: %v", err)
	}
}

func TestSubsonicLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newSubRig(t)
	r.s.UseUsers(testUsers)
	var ids []string
	for i := range MaxUserDevices {
		c, _, err := r.s.Create(ctx, carolH, NewSubsonicClient{Name: "App", Username: fmt.Sprintf("app.%d", i), APIKey: true})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
	}
	if _, _, err := r.s.Create(ctx, carolH, NewSubsonicClient{Name: "Sixth", Username: "sixth", APIKey: true}); !errors.Is(err, ErrDeviceLimit) {
		t.Errorf("6th client: %v", err)
	}
	if _, _, err := r.s.Create(ctx, bobH, NewSubsonicClient{Name: "Bob", Username: "bob.app", APIKey: true}); err != nil {
		t.Errorf("another user: %v", err)
	}
	for i := range MaxUserDevices + 2 {
		if _, _, err := r.s.Create(ctx, human, NewSubsonicClient{Name: "Own", Username: fmt.Sprintf("own.%d", i), APIKey: true}); err != nil {
			t.Fatalf("the Owner is unlimited: %v", err)
		}
	}
	if _, err := r.s.Revoke(ctx, carolH, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.s.Create(ctx, carolH, NewSubsonicClient{Name: "Again", Username: "again", APIKey: true}); err != nil {
		t.Errorf("revoking frees a slot: %v", err)
	}
}
