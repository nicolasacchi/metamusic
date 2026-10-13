package upload

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
)

// Signer mints and verifies the capability URLs of the upload channel (C74.1). The capability is the
// authorisation: ClassMachine routes carry no principal.
type Signer struct{ key []byte }

// NewSigner derives the upload key as HMAC-SHA256(secretKey, "metamusic/upload-url/v1").
func NewSigner(secretKey []byte) *Signer {
	m := hmac.New(sha256.New, secretKey)
	m.Write([]byte("metamusic/upload-url/v1"))
	return &Signer{key: m.Sum(nil)}
}

func (s *Signer) sig(uploadID, takeID, artifact string, maxBytes, exp int64) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("v1|" + uploadID + "|" + takeID + "|" + artifact + "|" + strconv.FormatInt(maxBytes, 10) + "|" + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func link(publicURL, path string, exp int64, sig string) string {
	q := url.Values{"exp": {strconv.FormatInt(exp, 10)}, "sig": {sig}}
	return strings.TrimRight(publicURL, "/") + path + "?" + q.Encode()
}

// URL is `<PublicURL>/worker/v1/uploads/<upload_id>?exp=<unix>&sig=<b64url>`.
func (s *Signer) URL(publicURL, uploadID, takeID, artifact string, maxBytes, exp int64) string {
	return link(publicURL, "/worker/v1/uploads/"+uploadID, exp, s.sig(uploadID, takeID, artifact, maxBytes, exp))
}

// Verify checks a signature in constant time.
func (s *Signer) Verify(uploadID, takeID, artifact string, maxBytes, exp int64, sig string) bool {
	return hmac.Equal([]byte(s.sig(uploadID, takeID, artifact, maxBytes, exp)), []byte(sig))
}

// StartURL is `<PublicURL>/worker/v1/attempts/<attempt_id>/start?exp=…&sig=…`; it signs the artifact
// field "start" with max_bytes 0.
func (s *Signer) StartURL(publicURL, attemptID, takeID string, exp int64) string {
	return link(publicURL, "/worker/v1/attempts/"+attemptID+"/start", exp, s.sig(attemptID, takeID, "start", 0, exp))
}

// VerifyStart checks a start-handshake signature.
func (s *Signer) VerifyStart(attemptID, takeID string, exp int64, sig string) bool {
	return hmac.Equal([]byte(s.sig(attemptID, takeID, "start", 0, exp)), []byte(sig))
}
