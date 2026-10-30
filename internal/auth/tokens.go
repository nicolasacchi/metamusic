package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrNotFound     = errors.New("not found")
	ErrInvalidScope = errors.New("invalid token name or scopes")
	// ErrTokenExpired is returned by Verify for a token past its expires_at. It also matches
	// ErrInvalidToken, so callers that only know "rejected" keep refusing it.
	ErrTokenExpired = &expiredError{}
)

type expiredError struct{}

func (*expiredError) Error() string { return "token expired" }

func (*expiredError) Is(target error) bool { return target == ErrInvalidToken }

// DefaultTokenTTL is the lifetime of a token minted without an explicit TTL.
const DefaultTokenTTL = 90 * 24 * time.Hour

var tokenRE = regexp.MustCompile(`^mmp_[a-z2-7]{52}$`)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

type TokenInfo struct {
	ID         string
	Name       string
	Prefix     string
	Scopes     []Scope
	CreatedAt  string
	LastUsedAt string // empty if never used
	ExpiresAt  string // empty = no expiry (tokens minted before migration 0011, or created with TTL 0)
	RevokedAt  string // empty if active
}

type TokenStore struct {
	st  *store.Store
	key []byte
	now func() time.Time
}

// NewTokenStore returns a store whose timestamps come from now (time.Now in
// production, the test clock in tests).
func NewTokenStore(st *store.Store, key []byte, now func() time.Time) *TokenStore {
	if now == nil {
		now = time.Now
	}
	return &TokenStore{st: st, key: key, now: now}
}

func mac(key []byte, s string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return hex.EncodeToString(m.Sum(nil))
}

const touchInterval = time.Minute // minimum gap between last_used_at writes per token

func (t *TokenStore) ts() string { return t.now().UTC().Format(time.RFC3339) }

// Create mints a token that expires after DefaultTokenTTL.
func (t *TokenStore) Create(ctx context.Context, name string, scopes []Scope) (string, string, error) {
	return t.CreateTTL(ctx, name, scopes, DefaultTokenTTL)
}

// CreateTTL mints a token that expires ttl from now; ttl 0 means it never expires, a negative ttl is invalid.
func (t *TokenStore) CreateTTL(ctx context.Context, name string, scopes []Scope, ttl time.Duration) (string, string, error) {
	if strings.TrimSpace(name) == "" || len(scopes) == 0 || ttl < 0 {
		return "", "", ErrInvalidScope
	}
	seen := map[Scope]bool{}
	for _, s := range scopes {
		if !slices.Contains(allScopes, s) || seen[s] {
			return "", "", ErrInvalidScope
		}
		seen[s] = true
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	plain := "mmp_" + strings.ToLower(b32.EncodeToString(raw))
	id := uuid.NewV7().String()
	ss := make([]string, len(scopes))
	for i, s := range scopes {
		ss[i] = string(s)
	}
	var expires any
	if ttl > 0 {
		expires = t.now().UTC().Add(ttl).Format(time.RFC3339)
	}
	_, err := t.st.W.ExecContext(ctx,
		`INSERT INTO api_tokens(id,name,prefix,hmac_hash,scopes,created_at,expires_at) VALUES(?,?,?,?,?,?,?)`,
		id, name, plain[4:12], mac(t.key, plain), strings.Join(ss, ","), t.ts(), expires)
	if err != nil {
		return "", "", fmt.Errorf("create token: %w", err)
	}
	return plain, id, nil
}

func splitScopes(s string) []Scope {
	var out []Scope
	for _, p := range strings.Split(s, ",") {
		if p != "" {
			out = append(out, Scope(p))
		}
	}
	return out
}

func (t *TokenStore) Verify(ctx context.Context, plaintext string) (Principal, error) {
	if !tokenRE.MatchString(plaintext) {
		return Principal{}, ErrInvalidToken
	}
	var id, name, scopes string
	var revoked, lastUsed, expires sql.NullString
	err := t.st.R.QueryRowContext(ctx,
		`SELECT id,name,scopes,revoked_at,last_used_at,expires_at FROM api_tokens WHERE hmac_hash=?`, mac(t.key, plaintext)).
		Scan(&id, &name, &scopes, &revoked, &lastUsed, &expires)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && revoked.Valid) {
		return Principal{}, ErrInvalidToken
	}
	if err != nil {
		return Principal{}, fmt.Errorf("verify token: %w", err)
	}
	n := t.now().UTC()
	if expires.Valid {
		// An unparseable expiry fails closed.
		if exp, perr := time.Parse(time.RFC3339, expires.String); perr != nil || !n.Before(exp) {
			return Principal{}, ErrTokenExpired
		}
	}
	// Best effort bookkeeping: decided in Go from the reader-pool value so the
	// single writer connection is only touched when a write is actually due,
	// and then only under a short bounded context.
	if lu, perr := time.Parse(time.RFC3339, lastUsed.String); !lastUsed.Valid || perr != nil || n.Sub(lu) >= touchInterval {
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 250*time.Millisecond)
		_, _ = t.st.W.ExecContext(wctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, n.Format(time.RFC3339), id)
		cancel()
	}
	return Principal{Kind: KindAgent, ID: id, Name: name, Scopes: splitScopes(scopes), Via: "token"}, nil
}

func (t *TokenStore) List(ctx context.Context) ([]TokenInfo, error) {
	rows, err := t.st.R.QueryContext(ctx,
		`SELECT id,name,prefix,scopes,created_at,last_used_at,revoked_at,expires_at FROM api_tokens ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenInfo
	for rows.Next() {
		var ti TokenInfo
		var scopes string
		var lu, rv, ex sql.NullString
		if err := rows.Scan(&ti.ID, &ti.Name, &ti.Prefix, &scopes, &ti.CreatedAt, &lu, &rv, &ex); err != nil {
			return nil, err
		}
		ti.Scopes, ti.LastUsedAt, ti.RevokedAt, ti.ExpiresAt = splitScopes(scopes), lu.String, rv.String, ex.String
		out = append(out, ti)
	}
	return out, rows.Err()
}

// Get returns one token's metadata (never hash material); ErrNotFound if absent.
func (t *TokenStore) Get(ctx context.Context, id string) (TokenInfo, error) {
	var ti TokenInfo
	var scopes string
	var lu, rv, ex sql.NullString
	err := t.st.R.QueryRowContext(ctx,
		`SELECT id,name,prefix,scopes,created_at,last_used_at,revoked_at,expires_at FROM api_tokens WHERE id=?`, id).
		Scan(&ti.ID, &ti.Name, &ti.Prefix, &scopes, &ti.CreatedAt, &lu, &rv, &ex)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenInfo{}, ErrNotFound
	}
	if err != nil {
		return TokenInfo{}, err
	}
	ti.Scopes, ti.LastUsedAt, ti.RevokedAt, ti.ExpiresAt = splitScopes(scopes), lu.String, rv.String, ex.String
	return ti, nil
}

func (t *TokenStore) Revoke(ctx context.Context, id string) error {
	res, err := t.st.W.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at=COALESCE(revoked_at,?) WHERE id=?`, t.ts(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
