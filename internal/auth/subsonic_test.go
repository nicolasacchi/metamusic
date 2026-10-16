package auth

import (
	"context"
	"crypto/md5" //nolint:gosec // protocol-defined token
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *manualClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type subRig struct {
	st    *store.Store
	clock *manualClock
	s     *SubsonicStore
}

var adminHuman = Principal{Kind: KindHuman, ID: "alice", Name: "alice", Scopes: allScopes, Via: "proxy"}

func newSubRig(t *testing.T) *subRig {
	t.Helper()
	st := newStore(t)
	clk := &manualClock{t: time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)}
	var n atomic.Int64
	rnd := rand.NewChaCha8([32]byte{1, 2, 3})
	return &subRig{st: st, clock: clk, s: NewSubsonicStore(st, key, clk.now, func() string {
		return fmt.Sprintf("01900000-0000-7000-8000-%012x", n.Add(1))
	}, rnd)}
}

func subToken(pw, salt string) string {
	sum := md5.Sum([]byte(pw + salt)) //nolint:gosec // protocol-defined
	return hex.EncodeToString(sum[:])
}

func (r *subRig) create(t *testing.T, in NewSubsonicClient) (SubsonicClient, SubsonicSecrets) {
	t.Helper()
	c, sec, err := r.s.Create(context.Background(), adminHuman, in)
	if err != nil {
		t.Fatal(err)
	}
	return c, sec
}

func dumpTable(t *testing.T, st *store.Store, table string) string {
	t.Helper()
	rows, err := st.R.Query(`SELECT * FROM ` + table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var b strings.Builder
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for _, v := range vals {
			switch x := v.(type) {
			case []byte:
				b.Write(x)
				b.WriteString(hex.EncodeToString(x))
			default:
				fmt.Fprint(&b, x)
			}
			b.WriteByte('|')
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func TestSubsonicStore_CreateAndStorage(t *testing.T) {
	t.Parallel()
	r := newSubRig(t)
	c, sec := r.create(t, NewSubsonicClient{Name: "Amperfy iPhone", Write: true, APIKey: true, Password: true})
	if !regexp.MustCompile(`^mms_[a-z2-7]{52}$`).MatchString(sec.APIKey) || !regexp.MustCompile(`^[a-z2-7]{24}$`).MatchString(sec.Password) {
		t.Fatalf("secrets %q %q", sec.APIKey, sec.Password)
	}
	if c.Username != "amperfy-iphone" || c.DefaultFormat != "raw" || !c.HasAPIKey || !c.HasPassword || c.APIKeyPrefix != sec.APIKey[4:12] ||
		len(c.Scopes) != 2 || c.CreatedBy != "human:alice" || c.RevokedAt != nil {
		t.Fatalf("client %+v", c)
	}
	dump := dumpTable(t, r.st, "subsonic_clients")
	for _, secret := range []string{sec.APIKey, sec.Password, sec.APIKey[4:], hex.EncodeToString([]byte(sec.Password))} {
		if strings.Contains(dump, secret) {
			t.Fatalf("plaintext secret %q stored", secret)
		}
	}
	list, err := r.s.List(context.Background())
	if err != nil || len(list) != 1 || list[0].ID != c.ID {
		t.Fatalf("list %+v %v", list, err)
	}
	// read-only client without an API key
	c2, sec2 := r.create(t, NewSubsonicClient{Name: "Tempus", Password: true, DefaultFormat: "mp3"})
	if sec2.APIKey != "" || c2.HasAPIKey || len(c2.Scopes) != 1 || c2.Scopes[0] != ScopeRead || c2.DefaultFormat != "mp3" {
		t.Fatalf("second client %+v %+v", c2, sec2)
	}
}

func TestSubsonicStore_CreateValidation(t *testing.T) {
	t.Parallel()
	r := newSubRig(t)
	for name, in := range map[string]NewSubsonicClient{
		"no credential":   {Name: "x"},
		"empty name":      {Name: "  ", APIKey: true},
		"long name":       {Name: strings.Repeat("n", 81), APIKey: true},
		"control name":    {Name: "a\nb", APIKey: true},
		"bad format":      {Name: "x", APIKey: true, DefaultFormat: "flac"},
		"bad username":    {Name: "x", APIKey: true, Username: "Bad Name"},
		"short username":  {Name: "x", APIKey: true, Username: "ab"},
		"leading dot":     {Name: "x", APIKey: true, Username: ".abc"},
		"long username":   {Name: "x", APIKey: true, Username: strings.Repeat("a", 33)},
		"uppercase":       {Name: "x", APIKey: true, Username: "Amperfy"},
		"username symbol": {Name: "x", APIKey: true, Username: "am@perfy"},
	} {
		if _, _, err := r.s.Create(context.Background(), adminHuman, in); !errors.Is(err, ErrSubsonicInput) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	for name, want := range map[string]string{"Q": "q-app", "": "client", "My  Player!!": "my-player", "ÄÖ": "client", "a.b": "a.b",
		strings.Repeat("abc ", 20): "abc-abc-abc-abc-abc-abc-abc-abc"} {
		if got := slugUsername(name); got != want || !subsonicUserRE.MatchString(got) {
			t.Errorf("slug(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestSubsonicStore_VerifyAPIKey(t *testing.T) {
	t.Parallel()
	r := newSubRig(t)
	ctx := context.Background()
	c, sec := r.create(t, NewSubsonicClient{Name: "Symfonium", Write: true, APIKey: true})
	p, err := r.s.VerifyAPIKey(ctx, sec.APIKey, "Symfonium 1.2 (Android)")
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindDevice || p.ID != c.ID || p.Name != "Symfonium" || p.Via != "subsonic" || p.Client != "Symfonium" ||
		!p.Has(ScopeWrite) || p.Has(ScopeGenerate) || p.Has(ScopeAdmin) || p.Key() != "device:"+c.ID || !p.ActsAsHuman() {
		t.Fatalf("principal %+v", p)
	}
	for name, k := range map[string]string{"unknown": "mms_" + strings.Repeat("a", 52), "empty": "", "pat": "mmp_" + strings.Repeat("a", 52),
		"short": "mms_abc", "uppercase": strings.ToUpper(sec.APIKey)} {
		if _, err := r.s.VerifyAPIKey(ctx, k, "x"); !errors.Is(err, ErrSubsonicAPIKey) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := r.s.Revoke(ctx, adminHuman, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.VerifyAPIKey(ctx, sec.APIKey, "x"); !errors.Is(err, ErrSubsonicAPIKey) {
		t.Fatalf("revoked key: %v", err)
	}
	// idempotent revoke keeps the first revocation; unknown id is ErrNotFound
	first, _ := r.s.get(ctx, c.ID)
	r.clock.advance(time.Hour)
	again, err := r.s.Revoke(ctx, adminHuman, c.ID)
	if err != nil || again.RevokedAt == nil || !again.RevokedAt.Equal(*first.RevokedAt) {
		t.Fatalf("second revoke %+v %v", again, err)
	}
	if _, err := r.s.Revoke(ctx, adminHuman, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestSubsonicStore_VerifyToken(t *testing.T) {
	t.Parallel()
	r := newSubRig(t)
	ctx := context.Background()
	c, sec := r.create(t, NewSubsonicClient{Name: "Tempus", Username: "tempus", Password: true})
	const salt = "abcdef"
	p, err := r.s.VerifyToken(ctx, "tempus", subToken(sec.Password, salt), salt, "Tempus/1", SaltWindowAPI)
	if err != nil || p.ID != c.ID || p.Via != "subsonic" || p.Client != "Tempus/1" || p.Has(ScopeWrite) {
		t.Fatalf("verify: %+v %v", p, err)
	}
	before := r.s.decrypts.Load()
	for name, tc := range map[string]struct{ user, token, salt string }{
		"wrong token":   {"tempus", subToken("wrongwrongwrongwrongwron", "abcdef"), "abcdef"},
		"unknown user":  {"nobody", subToken(sec.Password, "abcdef"), "abcdef"},
		"salt 5 chars":  {"tempus", subToken(sec.Password, "abcde"), "abcde"},
		"salt 65 chars": {"tempus", subToken(sec.Password, strings.Repeat("a", 65)), strings.Repeat("a", 65)},
		"salt space":    {"tempus", subToken(sec.Password, "abc def"), "abc def"},
		"salt control":  {"tempus", subToken(sec.Password, "abc\x01def"), "abc\x01def"},
		"token upper":   {"tempus", strings.ToUpper(subToken(sec.Password, "abcdef")), "abcdef"},
		"token short":   {"tempus", "abc", "abcdef"},
	} {
		if _, err := r.s.VerifyToken(ctx, tc.user, tc.token, tc.salt, "x", SaltWindowAPI); !errors.Is(err, ErrSubsonicCredentials) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// wrong token + unknown user went through the (dummy) decryption; shape failures never reach it
	if got := r.s.decrypts.Load() - before; got != 2 {
		t.Errorf("decryptions = %d, want 2 (wrong token, unknown user)", got)
	}
	// an API-key-only client has no password: same refusal, dummy path
	_, sec2 := r.create(t, NewSubsonicClient{Name: "KeyOnly", Username: "keyonly", APIKey: true})
	_ = sec2
	if _, err := r.s.VerifyToken(ctx, "keyonly", subToken("x", "abcdef"), "abcdef", "x", SaltWindowAPI); !errors.Is(err, ErrSubsonicCredentials) {
		t.Fatalf("key-only client: %v", err)
	}
	// a rejected token records no salt sighting
	var n int
	if err := r.st.R.QueryRow(`SELECT count(*) FROM subsonic_salts`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("salt rows = %d %v", n, err)
	}
	// revoked client
	if _, err := r.s.Revoke(ctx, adminHuman, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.VerifyToken(ctx, "tempus", subToken(sec.Password, "ghijkl"), "ghijkl", "x", SaltWindowAPI); !errors.Is(err, ErrSubsonicCredentials) {
		t.Fatalf("revoked: %v", err)
	}
}

func TestSubsonicAuth_SaltWindow(t *testing.T) {
	t.Parallel()
	r := newSubRig(t)
	ctx := context.Background()
	_, sec := r.create(t, NewSubsonicClient{Name: "A", Username: "aaa", Password: true})
	verify := func(s *SubsonicStore, salt string, w time.Duration) error {
		_, err := s.VerifyToken(ctx, "aaa", subToken(sec.Password, salt), salt, "x", w)
		return err
	}
	if err := verify(r.s, "salt-1", SaltWindowAPI); err != nil {
		t.Fatal(err)
	}
	r.clock.advance(9*time.Minute + 59*time.Second)
	if err := verify(r.s, "salt-1", SaltWindowAPI); err != nil {
		t.Fatalf("T0+9m59s: %v", err)
	}
	r.clock.advance(2 * time.Second) // T0 + 10m01s
	if err := verify(r.s, "salt-1", SaltWindowAPI); !errors.Is(err, ErrSaltExpired) {
		t.Fatalf("T0+10m01s: %v", err)
	}
	// a new store instance on the same DB still refuses it (the sighting is persisted)
	s2 := NewSubsonicStore(r.st, key, r.clock.now, nil, nil)
	if err := verify(s2, "salt-1", SaltWindowAPI); !errors.Is(err, ErrSaltExpired) {
		t.Fatalf("new instance: %v", err)
	}
	// a fresh salt is accepted again; the first sighting is never refreshed by replays
	if err := verify(s2, "salt-2", SaltWindowAPI); err != nil {
		t.Fatal(err)
	}
}

func TestSubsonicAuth_SaltWindowMedia(t *testing.T) {
	t.Parallel()
	for ep, want := range map[string]time.Duration{"stream": SaltWindowMedia, "download": SaltWindowMedia, "getCoverArt": SaltWindowMedia,
		"getSong": SaltWindowAPI, "setRating": SaltWindowAPI, "": SaltWindowAPI, "Stream": SaltWindowAPI} {
		if got := SaltWindowFor(ep); got != want {
			t.Errorf("SaltWindowFor(%q) = %v, want %v", ep, got, want)
		}
	}
	if SaltWindowMedia != 12*time.Hour || SaltWindowAPI != 10*time.Minute {
		t.Fatal("window constants")
	}
	r := newSubRig(t)
	ctx := context.Background()
	c, sec := r.create(t, NewSubsonicClient{Name: "M", Username: "mmm", Password: true})
	verify := func(salt, ep string) error {
		_, err := r.s.VerifyToken(ctx, "mmm", subToken(sec.Password, salt), salt, "x", SaltWindowFor(ep))
		return err
	}
	if err := verify("mediasalt", "stream"); err != nil {
		t.Fatal(err)
	}
	r.clock.advance(11*time.Hour + 59*time.Minute)
	if err := verify("mediasalt", "stream"); err != nil {
		t.Fatalf("T0+11h59m stream: %v", err)
	}
	r.clock.advance(2 * time.Minute) // T0 + 12h01m
	if err := verify("mediasalt", "stream"); !errors.Is(err, ErrSaltExpired) {
		t.Fatalf("T0+12h01m stream: %v", err)
	}
	// the window belongs to the current request: the same first-use salt on an API endpoint at +11 m is expired
	r2 := newSubRig(t)
	c2, sec2 := r2.create(t, NewSubsonicClient{Name: "M", Username: "mmm", Password: true})
	_ = c2
	v2 := func(salt, ep string) error {
		_, err := r2.s.VerifyToken(ctx, "mmm", subToken(sec2.Password, salt), salt, "x", SaltWindowFor(ep))
		return err
	}
	if err := v2("sharedsalt", "stream"); err != nil {
		t.Fatal(err)
	}
	r2.clock.advance(11 * time.Minute)
	if err := v2("sharedsalt", "setRating"); !errors.Is(err, ErrSaltExpired) {
		t.Fatalf("api window at +11m: %v", err)
	}
	if err := v2("sharedsalt", "stream"); err != nil {
		t.Fatalf("media window at +11m: %v", err)
	}
	// prune: keeps a row for the 30-day retention, far past the replay windows
	_ = c
	r2.clock.advance(13 * time.Hour) // T0 + ~13h: expired, but must survive pruning
	if n, err := r2.s.PruneSalts(ctx); err != nil || n != 0 {
		t.Fatalf("prune keeps (T0+13h): %d %v", n, err)
	}
	if err := v2("sharedsalt", "stream"); !errors.Is(err, ErrSaltExpired) {
		t.Fatalf("expired salt after prune pass: %v", err)
	}
	r2.clock.advance(31 * 24 * time.Hour)
	if n, err := r2.s.PruneSalts(ctx); err != nil || n != 1 {
		t.Fatalf("prune removes (T0+31d): %d %v", n, err)
	}
	var left int
	if err := r2.st.R.QueryRow(`SELECT count(*) FROM subsonic_salts`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("rows left %d %v", left, err)
	}
}

func TestSubsonicPassword_AADBindsRow(t *testing.T) {
	t.Parallel()
	r := newSubRig(t)
	ctx := context.Background()
	_, a := r.create(t, NewSubsonicClient{Name: "A", Username: "aaa", Password: true})
	_, b := r.create(t, NewSubsonicClient{Name: "B", Username: "bbb", Password: true})
	// a copy of A's ciphertext under another row id must not open (the frozen-column trigger forbids
	// UPDATE, so the copy goes in as a new row)
	if _, err := r.st.W.Exec(`INSERT INTO subsonic_clients(id, name, username, scopes, default_format, password_nonce, password_ct, created_by, created_at)
		SELECT 'copy-of-a', 'C', 'ccc', scopes, default_format, password_nonce, password_ct, created_by, created_at FROM subsonic_clients WHERE username='aaa'`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.VerifyToken(ctx, "ccc", subToken(a.Password, "abcdef"), "abcdef", "x", SaltWindowAPI); !errors.Is(err, ErrSubsonicCredentials) {
		t.Fatalf("copied ciphertext opened: %v", err)
	}
	if _, err := r.s.VerifyToken(ctx, "aaa", subToken(a.Password, "abcdef"), "abcdef", "x", SaltWindowAPI); err != nil {
		t.Fatalf("original: %v", err)
	}
	if _, err := r.s.VerifyToken(ctx, "bbb", subToken(b.Password, "abcdef"), "abcdef", "x", SaltWindowAPI); err != nil {
		t.Fatalf("other client: %v", err)
	}
	// a different server key cannot read the passwords either
	other := NewSubsonicStore(r.st, []byte("another-secret-key-another-secre"), r.clock.now, nil, nil)
	if _, err := other.VerifyToken(ctx, "bbb", subToken(b.Password, "ghijkl"), "ghijkl", "x", SaltWindowAPI); !errors.Is(err, ErrSubsonicCredentials) {
		t.Fatalf("foreign key opened: %v", err)
	}
}

func TestSubsonicStore_UsernameTaken(t *testing.T) {
	t.Parallel()
	r := newSubRig(t)
	ctx := context.Background()
	c, _ := r.create(t, NewSubsonicClient{Name: "Amperfy", Username: "amperfy", APIKey: true})
	if _, _, err := r.s.Create(ctx, adminHuman, NewSubsonicClient{Name: "Other", Username: "amperfy", APIKey: true}); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("live duplicate: %v", err)
	}
	// the default username derives from the name: a second "Amperfy" collides too
	if _, _, err := r.s.Create(ctx, adminHuman, NewSubsonicClient{Name: "Amperfy", APIKey: true}); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("derived duplicate: %v", err)
	}
	if _, err := r.s.Revoke(ctx, adminHuman, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.s.Create(ctx, adminHuman, NewSubsonicClient{Name: "Amperfy", Username: "amperfy", APIKey: true}); err != nil {
		t.Fatalf("after revoke: %v", err)
	}
}

func TestSubsonicStore_TouchAtMostOncePerMinute(t *testing.T) {
	t.Parallel()
	r := newSubRig(t)
	ctx := context.Background()
	c, sec := r.create(t, NewSubsonicClient{Name: "T", Username: "ttt", APIKey: true})
	read := func() (seen, client sql.NullString) {
		if err := r.st.R.QueryRow(`SELECT last_seen_at, last_client FROM subsonic_clients WHERE id=?`, c.ID).Scan(&seen, &client); err != nil {
			t.Fatal(err)
		}
		return
	}
	if s, _ := read(); s.Valid {
		t.Fatal("last_seen_at set before first use")
	}
	use := func(client string) {
		if _, err := r.s.VerifyAPIKey(ctx, sec.APIKey, client); err != nil {
			t.Fatal(err)
		}
	}
	use("Amperfy/1")
	s0, c0 := read()
	if !s0.Valid || len(s0.String) != 24 || c0.String != "Amperfy/1" {
		t.Fatalf("first touch %v %v", s0, c0)
	}
	r.clock.advance(30 * time.Second)
	use("Amperfy/1")
	if s, _ := read(); s != s0 {
		t.Fatalf("touched again within a minute: %v -> %v", s0, s)
	}
	r.clock.advance(31 * time.Second)
	use("Amperfy/1")
	s1, _ := read()
	if s1 == s0 {
		t.Fatal("not touched after a minute")
	}
	r.clock.advance(time.Second)
	use("Other/2") // a changed label is written at once
	if s, cl := read(); s == s1 || cl.String != "Other/2" {
		t.Fatalf("label change: %v %v", s, cl)
	}
	// a request without `c` leaves the stored label alone once it is in sync, and never errors
	use("")
	use("")
}

func TestSubsonicStore_ListNeverReturnsKeyMaterial(t *testing.T) {
	t.Parallel()
	r := newSubRig(t)
	_, sec := r.create(t, NewSubsonicClient{Name: "L", Username: "lll", APIKey: true, Password: true})
	list, err := r.s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s := fmt.Sprintf("%+v", list); strings.Contains(s, sec.APIKey) || strings.Contains(s, sec.Password) || strings.Contains(s, sec.APIKey[4:]) {
		t.Fatal("List leaks key material")
	}
}

func TestSubsonicStore_Defaults(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	s := NewSubsonicStore(st, key, nil, nil, nil)
	c, sec, err := s.Create(context.Background(), adminHuman, NewSubsonicClient{Name: "Default", APIKey: true})
	if err != nil || sec.APIKey == "" || len(c.ID) != 36 {
		t.Fatalf("%+v %+v %v", c, sec, err)
	}
}
