package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
}
func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newDevices(t *testing.T) (*DeviceStore, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	n := 0
	ds := NewDeviceStore(newStore(t), key, clk.Now, func() string { n++; return fmt.Sprintf("0190%04d-0000-7000-8000-000000000001", n) })
	return ds, clk
}

var human = Principal{Kind: KindHuman, ID: "alice", Name: "alice", Scopes: allScopes, Via: "proxy"}

var (
	codeRE   = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)
	cookieRE = regexp.MustCompile(`^mmd_[a-z2-7]{52}$`)
)

func TestPairing_CodeLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ds, clk := newDevices(t)
	code, pc, err := ds.CreatePairingCode(ctx, human, "Phone")
	if err != nil {
		t.Fatal(err)
	}
	if !codeRE.MatchString(code) || pc.Name != "Phone" || pc.CreatedBy != "human:alice" || pc.ID == "" {
		t.Fatalf("code = %q, pc = %+v", code, pc)
	}
	if want := clk.Now().Add(PairingTTL).Format(time.RFC3339); pc.ExpiresAt != want {
		t.Fatalf("expires = %s, want %s", pc.ExpiresAt, want)
	}

	code, _, err = ds.CreatePairingCode(ctx, human, "Tablet")
	if err != nil {
		t.Fatal(err)
	}
	mangled := strings.ToLower(strings.ReplaceAll(code, "-", ""))
	mangled = strings.NewReplacer("1", "i", "0", "o").Replace(mangled)
	cookie, dev, err := ds.Redeem(ctx, mangled, "UA/1")
	if err != nil {
		t.Fatal(err)
	}
	if !cookieRE.MatchString(cookie) || dev.Name != "Tablet" || dev.PairedBy != "human:alice" || dev.Prefix != cookie[4:12] ||
		len(dev.Scopes) != 2 || dev.Scopes[0] != ScopeRead || dev.Scopes[1] != ScopeWrite || dev.UserAgent != "UA/1" {
		t.Fatalf("cookie %q dev %+v", cookie, dev)
	}
	p, err := ds.VerifyDevice(ctx, cookie)
	if err != nil || p.Kind != KindDevice || p.ID != dev.ID || p.Name != "Tablet" || p.Via != "device" || p.Key() != "device:"+dev.ID ||
		!p.Has(ScopeRead) || !p.Has(ScopeWrite) || p.Has(ScopeGenerate) || p.Has(ScopeAdmin) {
		t.Fatalf("principal %+v, err %v", p, err)
	}

	// expiry: +5 min 01 s
	code, _, _ = ds.CreatePairingCode(ctx, human, "Late")
	clk.Advance(PairingTTL + time.Second)
	if _, _, err := ds.Redeem(ctx, code, "ua"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("expired code: %v", err)
	}

	// a second code revokes the first
	first, _, _ := ds.CreatePairingCode(ctx, human, "One")
	second, _, _ := ds.CreatePairingCode(ctx, human, "Two")
	if _, _, err := ds.Redeem(ctx, first, "ua"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("superseded code: %v", err)
	}
	if _, _, err := ds.Redeem(ctx, second, "ua"); err != nil {
		t.Fatal(err)
	}

	// malformed input
	for _, bad := range []string{"", "ABCD", "ABCD-EFGH-J", "UUUU-UUUU", "AB!D-EFGH", "ABCDEFGHJ"} {
		if _, _, err := ds.Redeem(ctx, bad, "ua"); !errors.Is(err, ErrInvalidCode) {
			t.Errorf("Redeem(%q) = %v", bad, err)
		}
	}

	// a long, multi-byte user agent is cut at a rune boundary, never an error
	code, _, _ = ds.CreatePairingCode(ctx, human, "Long")
	ua := strings.Repeat("é", 300) // 600 bytes
	_, dev, err = ds.Redeem(ctx, code, ua)
	if err != nil {
		t.Fatal(err)
	}
	if len(dev.UserAgent) > 512 || !utf8.ValidString(dev.UserAgent) || len(dev.UserAgent) != 512 {
		t.Fatalf("user agent len %d valid %v", len(dev.UserAgent), utf8.ValidString(dev.UserAgent))
	}
	code, _, _ = ds.CreatePairingCode(ctx, human, "Long2")
	_, dev, err = ds.Redeem(ctx, code, "a"+strings.Repeat("é", 300))
	if err != nil || len(dev.UserAgent) != 511 || !utf8.ValidString(dev.UserAgent) {
		t.Fatalf("boundary cut: len %d err %v", len(dev.UserAgent), err)
	}
}

func TestPairing_CreateRules(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ds, _ := newDevices(t)
	for _, p := range []Principal{{Kind: KindAgent, ID: "t"}, {Kind: KindDevice, ID: "d"}, {Kind: KindAnonymous}} {
		if _, _, err := ds.CreatePairingCode(ctx, p, "x"); err == nil {
			t.Errorf("%v minted a code", p.Kind)
		}
	}
	for _, n := range []string{"", strings.Repeat("n", 81)} {
		if _, _, err := ds.CreatePairingCode(ctx, human, n); !errors.Is(err, ErrInvalidName) {
			t.Errorf("name %q: %v", n, err)
		}
	}
	if _, _, err := ds.CreatePairingCode(ctx, human, strings.Repeat("n", 80)); err != nil {
		t.Fatal(err)
	}
}

func TestPairing_SingleUse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ds, _ := newDevices(t)
	code, _, _ := ds.CreatePairingCode(ctx, human, "Phone")
	if _, _, err := ds.Redeem(ctx, code, "ua"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ds.Redeem(ctx, code, "ua"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("second redeem: %v", err)
	}
	// concurrent redemption: exactly one wins
	code, _, _ = ds.CreatePairingCode(ctx, human, "Race")
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 8 {
		wg.Go(func() {
			if _, _, err := ds.Redeem(ctx, code, "ua"); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("wins = %d", wins)
	}
	list, err := ds.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %d, %v", len(list), err)
	}
}

func TestDevice_VerifySlidesExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ds, clk := newDevices(t)
	code, _, _ := ds.CreatePairingCode(ctx, human, "Phone")
	cookie, dev, _ := ds.Redeem(ctx, code, "ua")
	created := clk.Now()
	get := func() DeviceInfo {
		t.Helper()
		l, err := ds.List(ctx)
		if err != nil || len(l) != 1 {
			t.Fatalf("list %v %v", l, err)
		}
		return l[0]
	}
	if got := get(); got.ExpiresAt != created.Add(DeviceTTL).Format(time.RFC3339) || got.LastSeenAt != "" {
		t.Fatalf("fresh row %+v", got)
	}

	_, slid, err := ds.verify(ctx, cookie)
	if err != nil || slid {
		t.Fatalf("first verify slid=%v err=%v", slid, err)
	}
	seen1 := get().LastSeenAt
	if seen1 != created.Format(time.RFC3339) {
		t.Fatalf("last_seen = %q", seen1)
	}
	// within a minute nothing is written
	clk.Advance(30 * time.Second)
	if _, slid, _ = ds.verify(ctx, cookie); slid {
		t.Fatal("slid within a minute")
	}
	if get().LastSeenAt != seen1 {
		t.Fatal("last_seen written twice within a minute")
	}
	clk.Advance(31 * time.Second)
	_, _, _ = ds.verify(ctx, cookie)
	if get().LastSeenAt == seen1 {
		t.Fatal("last_seen not refreshed after a minute")
	}
	// expiry slides at most hourly
	exp := get().ExpiresAt
	clk.Advance(30 * time.Minute)
	if _, slid, _ = ds.verify(ctx, cookie); slid || get().ExpiresAt != exp {
		t.Fatal("expiry slid within the hour")
	}
	clk.Advance(31 * time.Minute)
	if _, slid, _ = ds.verify(ctx, cookie); !slid || get().ExpiresAt == exp {
		t.Fatal("expiry did not slide after the hour")
	}

	// 90 idle days: invalid
	if _, err := ds.VerifyDevice(ctx, cookie); err != nil {
		t.Fatal(err)
	}
	clk.Advance(DeviceTTL - time.Minute)
	if _, err := ds.VerifyDevice(ctx, cookie); err != nil {
		t.Fatalf("just before expiry: %v", err)
	}
	clk.Advance(DeviceTTL + time.Second)
	if _, err := ds.VerifyDevice(ctx, cookie); !errors.Is(err, ErrInvalidDevice) {
		t.Fatalf("after 90 idle days: %v", err)
	}
	_ = dev
}

func TestDevice_SurvivesThirteenHourGap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ds, clk := newDevices(t)
	code, _, _ := ds.CreatePairingCode(ctx, human, "Phone")
	cookie, _, _ := ds.Redeem(ctx, code, "ua")
	if _, _, err := ds.verify(ctx, cookie); err != nil {
		t.Fatal(err)
	}
	clk.Advance(13 * time.Hour)
	_, slid, err := ds.verify(ctx, cookie)
	if err != nil || !slid {
		t.Fatalf("13h later: slid=%v err=%v", slid, err)
	}
	clk.Advance(time.Minute)
	if _, slid, err = ds.verify(ctx, cookie); err != nil || slid {
		t.Fatalf("a minute later: slid=%v err=%v", slid, err)
	}
}

func TestDevice_RevokeAndList(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ds, clk := newDevices(t)
	code, _, _ := ds.CreatePairingCode(ctx, human, "Phone")
	cookie, dev, _ := ds.Redeem(ctx, code, "ua")
	clk.Advance(time.Minute)
	code, _, _ = ds.CreatePairingCode(ctx, human, "Tablet")
	_, dev2, _ := ds.Redeem(ctx, code, "ua2")
	l, _ := ds.List(ctx)
	if len(l) != 2 || l[0].ID != dev2.ID || l[1].ID != dev.ID {
		t.Fatalf("order: %+v", l)
	}
	r1, err := ds.Revoke(ctx, dev.ID)
	if err != nil || r1.RevokedAt == "" {
		t.Fatalf("revoke: %+v %v", r1, err)
	}
	clk.Advance(time.Hour)
	r2, err := ds.Revoke(ctx, dev.ID)
	if err != nil || r2.RevokedAt != r1.RevokedAt {
		t.Fatalf("revoke not idempotent: %+v %v", r2, err)
	}
	if _, err := ds.VerifyDevice(ctx, cookie); !errors.Is(err, ErrInvalidDevice) {
		t.Fatalf("revoked cookie: %v", err)
	}
	if _, err := ds.Revoke(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	for _, bad := range []string{"", "mmd_x", "mmp_" + strings.Repeat("a", 52), "mmd_" + strings.Repeat("A", 52), "mmd_" + strings.Repeat("a", 52)} {
		if _, err := ds.VerifyDevice(ctx, bad); !errors.Is(err, ErrInvalidDevice) {
			t.Errorf("VerifyDevice(%q) = %v", bad, err)
		}
	}
}

func TestDevice_ScopesReadWriteOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ds, _ := newDevices(t)
	code, _, _ := ds.CreatePairingCode(ctx, human, "Phone")
	cookie, _, _ := ds.Redeem(ctx, code, "ua")
	p, err := ds.VerifyDevice(ctx, cookie)
	if err != nil || len(p.Scopes) != 2 || !p.Has(ScopeRead) || !p.Has(ScopeWrite) || p.Has(ScopeGenerate) || p.Has(ScopeAdmin) {
		t.Fatalf("%+v %v", p, err)
	}
	if !p.ActsAsHuman() {
		t.Fatal("device must act as human")
	}
	// the schema refuses wider scopes
	if _, err := ds.st.W.Exec(`UPDATE devices SET scopes='read,write,admin'`); err == nil {
		t.Fatal("frozen scopes column was updated")
	}
}

func TestDevice_NilReceiverSafe(t *testing.T) {
	t.Parallel()
	var d *DeviceStore
	if _, err := d.VerifyDevice(context.Background(), "mmd_"+strings.Repeat("a", 52)); !errors.Is(err, ErrInvalidDevice) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := d.verify(context.Background(), "x"); !errors.Is(err, ErrInvalidDevice) {
		t.Fatalf("verify err = %v", err)
	}
}
