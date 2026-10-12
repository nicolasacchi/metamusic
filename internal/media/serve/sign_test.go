package serve

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

var (
	testKey = []byte("0123456789abcdef0123456789abcdef")
	testSHA = strings.Repeat("ab", 32)
)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// refSig recomputes the MAC independently of the implementation.
func refSig(sha, ext string, exp int64) string {
	k := hmac.New(sha256.New, testKey)
	k.Write([]byte("metamusic/media-url/v1"))
	m := hmac.New(sha256.New, k.Sum(nil))
	m.Write([]byte("v1\n" + sha + "\n" + ext + "\n" + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func parseLink(t *testing.T, u string) (exp, sig string) {
	t.Helper()
	i := strings.Index(u, "?exp=")
	j := strings.Index(u, "&sig=")
	if i < 0 || j < 0 {
		t.Fatalf("link %q", u)
	}
	return u[i+5 : j], u[j+5:]
}

func TestSigner_SignVerify(t *testing.T) {
	now := time.Date(2026, 10, 12, 10, 20, 0, 0, time.UTC)
	s := NewSigner(testKey, fixedClock(now))
	l := s.Sign(testSHA, "flac", 12*time.Hour, time.Hour)
	exp := time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC).Unix()
	want := "/media/" + testSHA + ".flac?exp=" + strconv.FormatInt(exp, 10) + "&sig=" + refSig(testSHA, "flac", exp)
	if l.URL != want {
		t.Fatalf("URL = %s\nwant  %s", l.URL, want)
	}
	if !l.Expires.Equal(time.Unix(exp, 0)) {
		t.Fatalf("Expires = %v", l.Expires)
	}
	e, sig := parseLink(t, l.URL)
	if len(sig) != 43 {
		t.Fatalf("sig length %d", len(sig))
	}
	if err := s.Verify(testSHA, "flac", e, sig); err != nil {
		t.Fatal(err)
	}
	other := strings.Repeat("cd", 32)
	flip := []byte(sig)
	flip[0] ^= 1
	if flip[0] > 'z' || flip[0] < '-' {
		flip[0] = 'A'
	}
	for name, args := range map[string][4]string{
		"sha":       {other, "flac", e, sig},
		"ext":       {testSHA, "mp3", e, sig},
		"exp":       {testSHA, "flac", strconv.FormatInt(exp+1, 10), sig},
		"sig":       {testSHA, "flac", e, string(flip)},
		"empty sig": {testSHA, "flac", e, ""},
		"bad exp":   {testSHA, "flac", "x", sig},
		"short sig": {testSHA, "flac", e, sig[:10]},
	} {
		if err := s.Verify(args[0], args[1], args[2], args[3]); !errors.Is(err, ErrLinkInvalid) {
			t.Errorf("%s: err = %v, want ErrLinkInvalid", name, err)
		}
	}

	// expired
	late := NewSigner(testKey, fixedClock(time.Unix(exp+1, 0)))
	if err := late.Verify(testSHA, "flac", e, sig); !errors.Is(err, ErrLinkExpired) {
		t.Errorf("late: err = %v, want ErrLinkExpired", err)
	}
	// exactly at exp is still valid
	at := NewSigner(testKey, fixedClock(time.Unix(exp, 0)))
	if err := at.Verify(testSHA, "flac", e, sig); err != nil {
		t.Errorf("at exp: %v", err)
	}
	// exp beyond now+14h is invalid even with a correct MAC
	far := now.Add(15 * time.Hour).Unix()
	if err := s.Verify(testSHA, "flac", strconv.FormatInt(far, 10), refSig(testSHA, "flac", far)); !errors.Is(err, ErrLinkInvalid) {
		t.Errorf("far: err = %v, want ErrLinkInvalid", err)
	}
	// a different key never verifies
	if err := NewSigner([]byte("another key another key another!"), fixedClock(now)).Verify(testSHA, "flac", e, sig); !errors.Is(err, ErrLinkInvalid) {
		t.Errorf("other key: %v", err)
	}
}

func TestSigner_BucketedExpiry(t *testing.T) {
	human := NewSigner(testKey, fixedClock(time.Date(2026, 10, 12, 10, 20, 0, 0, time.UTC)))
	ttl, bucket := TTLFor(auth.Principal{Kind: auth.KindHuman})
	if got := human.Sign(testSHA, "mp3", ttl, bucket).Expires; !got.Equal(time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC)) {
		t.Errorf("human exp = %v", got)
	}
	agent := NewSigner(testKey, fixedClock(time.Date(2026, 10, 12, 10, 20, 30, 0, time.UTC)))
	ttl, bucket = TTLFor(auth.Principal{Kind: auth.KindAgent})
	if got := agent.Sign(testSHA, "mp3", ttl, bucket).Expires; !got.Equal(time.Date(2026, 10, 12, 10, 36, 0, 0, time.UTC)) {
		t.Errorf("agent exp = %v", got)
	}
	// stable within a bucket
	a := human.Sign(testSHA, "mp3", 12*time.Hour, time.Hour)
	b := NewSigner(testKey, fixedClock(time.Date(2026, 10, 12, 10, 59, 59, 0, time.UTC))).Sign(testSHA, "mp3", 12*time.Hour, time.Hour)
	if a.URL != b.URL {
		t.Errorf("URL not stable within a bucket")
	}
	// a human link is always verifiable by the same clock (12h + bucket < 14h)
	e, sig := parseLink(t, a.URL)
	if err := human.Verify(testSHA, "mp3", e, sig); err != nil {
		t.Errorf("human link must verify: %v", err)
	}
}

func TestTTLFor(t *testing.T) {
	tests := []struct {
		name         string
		p            auth.Principal
		ttl, backing time.Duration
	}{
		{"human", auth.Principal{Kind: auth.KindHuman}, 12 * time.Hour, time.Hour},
		{"agent", auth.Principal{Kind: auth.KindAgent}, 15 * time.Minute, time.Minute},
		{"anonymous", auth.Principal{Kind: auth.KindAnonymous}, 15 * time.Minute, time.Minute},
		{"zero", auth.Principal{}, 15 * time.Minute, time.Minute},
	}
	for _, tc := range tests {
		ttl, bucket := TTLFor(tc.p)
		if ttl != tc.ttl || bucket != tc.backing {
			t.Errorf("%s: %v %v", tc.name, ttl, bucket)
		}
	}
}
