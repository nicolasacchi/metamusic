package store_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// legacyToken puts a working token into dst, a database below schema 11, the way an older binary would have
// (no expires_at column): it mints one in a scratch current-schema store and copies the row without the expiry.
func legacyToken(ctx context.Context, dst *store.Store, key []byte) (string, error) {
	dir, err := os.MkdirTemp("", "mm-legacy-token")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	scratch, err := store.Open(ctx, filepath.Join(dir, "scratch.db"))
	if err != nil {
		return "", err
	}
	defer scratch.Close()
	plain, id, err := auth.NewTokenStore(scratch, key, time.Now).Create(ctx, "t", []auth.Scope{auth.ScopeRead})
	if err != nil {
		return "", err
	}
	var name, prefix, hash, scopes, created string
	if err := scratch.R.QueryRowContext(ctx, `SELECT name,prefix,hmac_hash,scopes,created_at FROM api_tokens WHERE id=?`, id).
		Scan(&name, &prefix, &hash, &scopes, &created); err != nil {
		return "", err
	}
	_, err = dst.W.ExecContext(ctx, `INSERT INTO api_tokens(id,name,prefix,hmac_hash,scopes,created_at) VALUES(?,?,?,?,?,?)`,
		id, name, prefix, hash, scopes, created)
	return plain, err
}
