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
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrNotFound     = errors.New("not found")
)

var tokenRE = regexp.MustCompile(`^mmp_[a-z2-7]{52}$`)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

type TokenInfo struct {
	ID         string
	Name       string
	Prefix     string
	Scopes     []Scope
	CreatedAt  string
	LastUsedAt string // empty if never used
	RevokedAt  string // empty if active
}

type TokenStore struct {
	st  *store.Store
	key []byte
}

func NewTokenStore(st *store.Store, key []byte) *TokenStore {
	return &TokenStore{st: st, key: key}
}

func mac(key []byte, s string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return hex.EncodeToString(m.Sum(nil))
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func (t *TokenStore) Create(ctx context.Context, name string, scopes []Scope) (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	plain := "mmp_" + strings.ToLower(b32.EncodeToString(raw))
	id, err := randomHex(8)
	if err != nil {
		return "", "", err
	}
	ss := make([]string, len(scopes))
	for i, s := range scopes {
		ss[i] = string(s)
	}
	_, err = t.st.W.ExecContext(ctx,
		`INSERT INTO api_tokens(id,name,prefix,hmac_hash,scopes,created_at) VALUES(?,?,?,?,?,?)`,
		id, name, plain[4:12], mac(t.key, plain), strings.Join(ss, ","), now())
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
	var revoked sql.NullString
	err := t.st.R.QueryRowContext(ctx,
		`SELECT id,name,scopes,revoked_at FROM api_tokens WHERE hmac_hash=?`, mac(t.key, plaintext)).
		Scan(&id, &name, &scopes, &revoked)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && revoked.Valid) {
		return Principal{}, ErrInvalidToken
	}
	if err != nil {
		return Principal{}, fmt.Errorf("verify token: %w", err)
	}
	// best effort; a failed bookkeeping write must not fail authentication
	_, _ = t.st.W.ExecContext(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, now(), id)
	return Principal{Kind: KindAgent, ID: id, Name: name, Scopes: splitScopes(scopes), Via: "token"}, nil
}

func (t *TokenStore) List(ctx context.Context) ([]TokenInfo, error) {
	rows, err := t.st.R.QueryContext(ctx,
		`SELECT id,name,prefix,scopes,created_at,last_used_at,revoked_at FROM api_tokens ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenInfo
	for rows.Next() {
		var ti TokenInfo
		var scopes string
		var lu, rv sql.NullString
		if err := rows.Scan(&ti.ID, &ti.Name, &ti.Prefix, &scopes, &ti.CreatedAt, &lu, &rv); err != nil {
			return nil, err
		}
		ti.Scopes, ti.LastUsedAt, ti.RevokedAt = splitScopes(scopes), lu.String, rv.String
		out = append(out, ti)
	}
	return out, rows.Err()
}

func (t *TokenStore) Revoke(ctx context.Context, id string) error {
	res, err := t.st.W.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at=COALESCE(revoked_at,?) WHERE id=?`, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
