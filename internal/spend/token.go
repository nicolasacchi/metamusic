package spend

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
	"io"
	"strings"
	"sync"

	"github.com/nicolasacchi/metamusic/internal/store"
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

// Token refusal reasons (TokenError.Reason).
const (
	TokenUnknown  = "unknown"  // no such token, or another principal's: CONFIRMATION_INVALID
	TokenConsumed = "consumed" // CONFIRMATION_INVALID with details.reason = "consumed"
	TokenExpired  = "expired"  // CONFIRMATION_EXPIRED
)

// TokenError says why a confirmation token was refused (C72.4 step 2).
type TokenError struct {
	Reason string
	// BatchID is the batch that consumed the token; set only for the same principal.
	BatchID string
}

// Error implements error without leaking the token.
func (e *TokenError) Error() string { return "spend: confirmation token " + e.Reason }

// ConsumeToken marks the estimate behind token consumed by batchID (C69 token consumption SQL; same
// transaction as the create). It returns the estimate id. A token of another principal reads as
// unknown, so the two cases are indistinguishable.
func (s *Service) ConsumeToken(ctx context.Context, tx *sql.Tx, token, principalKey, batchID string) (string, error) {
	h := s.HashToken(token)
	now := store.FormatTime(s.now())
	var id string
	err := tx.QueryRowContext(ctx, `UPDATE estimates SET consumed_at = ?, consumed_batch_id = ?
		WHERE token_hash = ? AND principal_id = ? AND consumed_at IS NULL AND expires_at > ? RETURNING id`,
		now, batchID, h, principalKey, now).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("spend: consume token: %w", err)
	}
	var owner, exp string
	var by sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT principal_id, expires_at, consumed_batch_id FROM estimates WHERE token_hash = ?`, h).Scan(&owner, &exp, &by)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != principalKey) {
		return "", &TokenError{Reason: TokenUnknown}
	}
	if err != nil {
		return "", fmt.Errorf("spend: consume token: %w", err)
	}
	if by.Valid {
		return "", &TokenError{Reason: TokenConsumed, BatchID: by.String}
	}
	return "", &TokenError{Reason: TokenExpired}
}
