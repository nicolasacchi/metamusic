package spend

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"
)

// tokenPrefix is the gitleaks-visible prefix of confirmation tokens.
const tokenPrefix = "mmc_"

var tokenEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// randMu serialises reads of an injected deterministic reader (Options.Rand) so concurrent mints stay
// reproducible per rig; crypto/rand needs no lock but costs nothing here.
var randMu sync.Mutex

// NewToken mints a confirmation token: "mmc_" plus 52 lowercase base32 characters of 32 random bytes
// (C72.2). The random source is Options.Rand (deterministic in the parity rigs) or crypto/rand.
func (s *Service) NewToken() (string, error) {
	src := s.rand
	if src == nil {
		src = rand.Reader
	}
	var b [32]byte
	randMu.Lock()
	_, err := io.ReadFull(src, b[:])
	randMu.Unlock()
	if err != nil {
		return "", fmt.Errorf("spend: mint token: %w", err)
	}
	return tokenPrefix + strings.ToLower(tokenEnc.EncodeToString(b[:])), nil
}

// HashToken is the stored form of a token: hex HMAC-SHA256(ConfirmKey, token). Only the hash is ever
// stored.
func (s *Service) HashToken(token string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(token))
	return hex.EncodeToString(m.Sum(nil))
}
