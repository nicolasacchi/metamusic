package oidctest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
)

type signingKey struct {
	kid string
	key *rsa.PrivateKey
}

// keyPool holds a few RSA-2048 keys generated once per test binary (in parallel): keygen under -race costs
// seconds, and the pool is immutable after the Once. Providers draw by index; kids stay unique per provider.
const poolSize = 4

var keyPool struct {
	once sync.Once
	keys [poolSize]*rsa.PrivateKey
}

func genRSA() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
}

func newKey(idx int) signingKey {
	keyPool.once.Do(func() {
		var wg sync.WaitGroup
		for i := range keyPool.keys {
			wg.Go(func() { keyPool.keys[i] = genRSA() })
		}
		wg.Wait()
	})
	k := (*rsa.PrivateKey)(nil)
	if idx < poolSize {
		k = keyPool.keys[idx]
	} else {
		k = genRSA()
	}
	return signingKey{kid: "kid-" + randID(6), key: k}
}

// RotateKey publishes a new signing key (new kid); the old one stays published until DropOldKeys.
func (p *Provider) RotateKey() {
	p.mu.Lock()
	k := newKey(p.nkeys)
	p.nkeys++
	p.keys = append(p.keys, k)
	p.mu.Unlock()
}

// DropOldKeys unpublishes every key but the current one.
func (p *Provider) DropOldKeys() {
	p.mu.Lock()
	p.keys = p.keys[len(p.keys)-1:]
	p.mu.Unlock()
}

func (p *Provider) jwks(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	var keys []map[string]string
	for _, k := range p.keys {
		keys = append(keys, map[string]string{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": k.kid,
			"n": b64(k.key.N.Bytes()), "e": b64(big.NewInt(int64(k.key.E)).Bytes()),
		})
	}
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// sign builds a compact JWS with the current key. The header and claims are serialised as given (after Mutate)
// so fault injection can produce any header; the signature is always RS256 over the signing input.
func (p *Provider) sign(header, claims map[string]any) string {
	p.mu.Lock()
	k := p.keys[len(p.keys)-1]
	p.mu.Unlock()
	if _, ok := header["kid"]; !ok {
		header["kid"] = k.kid
	}
	if p.opts.Mutate != nil {
		p.opts.Mutate(header, claims)
	}
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	in := b64(hb) + "." + b64(cb)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return in + "." + b64(sig)
}

func (p *Provider) accessToken(clientID, user string, scopes, aud []string) string {
	now := p.opts.Now()
	sub := p.opts.Users[user]
	if sub == "" {
		sub = user
	}
	claims := map[string]any{
		"iss": p.URL, "sub": sub, "aud": aud, "client_id": clientID,
		"iat": now.Unix(), "exp": now.Add(p.opts.AccessTTL).Unix(), "jti": randID(8),
	}
	switch p.opts.ScopeClaim {
	case "scp":
		claims["scp"] = scopes
	case "both":
		claims["scope"] = strings.Join(scopes, " ")
		claims["scp"] = scopes
	default:
		claims["scope"] = strings.Join(scopes, " ")
	}
	if p.opts.UserClaim && slices.Contains(scopes, "metamusic") {
		claims["mm_user"] = user
	}
	return p.sign(map[string]any{"alg": "RS256", "typ": "at+jwt"}, claims)
}

// Mint signs an access token directly for unit tests. An empty aud means the client's configured audience.
func (p *Provider) Mint(tb testing.TB, client, user string, scopes, aud []string) string {
	tb.Helper()
	if len(aud) == 0 {
		if c := p.client(client); c != nil {
			aud = c.Audience
		}
	}
	return p.accessToken(client, user, scopes, aud)
}
