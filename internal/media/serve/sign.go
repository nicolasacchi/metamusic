package serve

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

// Link verification failures.
var (
	ErrLinkInvalid = errors.New("serve: media link invalid")
	ErrLinkExpired = errors.New("serve: media link expired")
)

// maxLinkAhead bounds exp beyond the signer's clock: human TTL 12h plus one
// 1h bucket is the longest link this server mints.
const maxLinkAhead = 14 * time.Hour

const keyLabel = "metamusic/media-url/v1"

// Signer mints and verifies /media/ links.
type Signer struct {
	key []byte
	now func() time.Time
}

// Link is a signed media URL and its expiry.
type Link struct {
	URL     string
	Expires time.Time
}

// NewSigner derives the media key as HMAC-SHA256(secretKey, keyLabel).
// A nil now means time.Now.
func NewSigner(secretKey []byte, now func() time.Time) *Signer {
	if now == nil {
		now = time.Now
	}
	m := hmac.New(sha256.New, secretKey)
	m.Write([]byte(keyLabel))
	return &Signer{key: m.Sum(nil), now: now}
}

func (s *Signer) mac(sha, ext string, exp int64) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("v1\n" + sha + "\n" + ext + "\n" + strconv.FormatInt(exp, 10)))
	return m.Sum(nil)
}

// Sign returns a link for sha.ext whose expiry is ceil((now+ttl)/bucket)*bucket,
// so URLs are stable within a bucket and the browser cache works.
func (s *Signer) Sign(sha, ext string, ttl, bucket time.Duration) Link {
	if bucket <= 0 {
		bucket = time.Second
	}
	b := int64(bucket / time.Second)
	if b < 1 {
		b = 1
	}
	at := s.now().Add(ttl)
	raw := at.Unix()
	if at.Nanosecond() > 0 {
		raw++
	}
	exp := (raw + b - 1) / b * b
	sig := base64.RawURLEncoding.EncodeToString(s.mac(sha, ext, exp))
	return Link{
		URL:     "/media/" + sha + "." + ext + "?exp=" + strconv.FormatInt(exp, 10) + "&sig=" + sig,
		Expires: time.Unix(exp, 0).UTC(),
	}
}

// Verify checks the MAC first, then expiry (ErrLinkExpired), then the
// maximum horizon (ErrLinkInvalid).
func (s *Signer) Verify(sha, ext, exp, sig string) error {
	e, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || e < 0 || strconv.FormatInt(e, 10) != exp {
		return ErrLinkInvalid
	}
	got, err := base64.RawURLEncoding.Strict().DecodeString(sig)
	if err != nil || len(got) != sha256.Size {
		return ErrLinkInvalid
	}
	if !hmac.Equal(got, s.mac(sha, ext, e)) {
		return ErrLinkInvalid
	}
	now := s.now()
	switch {
	case e < now.Unix():
		return ErrLinkExpired
	case time.Unix(e, 0).After(now.Add(maxLinkAhead)):
		return ErrLinkInvalid
	}
	return nil
}

// TTLFor is the link lifetime and expiry bucket for a principal: humans get
// 12h in 1h buckets, everything else 15m in 1m buckets.
func TTLFor(p auth.Principal) (ttl, bucket time.Duration) {
	if p.Kind == auth.KindHuman {
		return 12 * time.Hour, time.Hour
	}
	return 15 * time.Minute, time.Minute
}
