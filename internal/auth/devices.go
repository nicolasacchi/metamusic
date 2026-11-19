package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Device constants (C26).
const (
	DeviceCookie = "mm_dev"
	DeviceTTL    = 90 * 24 * time.Hour // sliding
	PairingTTL   = 5 * time.Minute

	deviceSlide = time.Hour   // server-side expiry slides at most hourly
	deviceTouch = time.Minute // last_seen_at written at most once a minute

	maxUserAgent = 512
	maxNameRunes = 80
	crockford    = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	codeSymbols  = 8
)

// Device errors.
var (
	ErrInvalidDevice = errors.New("invalid device")
	ErrInvalidCode   = errors.New("invalid pairing code")
	ErrInvalidName   = errors.New("name must be 1 to 80 characters")
	ErrNotHuman      = errors.New("only a signed-in user can mint pairing codes")
)

var deviceRE = regexp.MustCompile(`^mmd_[a-z2-7]{52}$`)

// DeviceInfo describes one paired device; it never carries the cookie hash.
type DeviceInfo struct {
	ID, Name, Prefix, UserAgent, PairedBy, CreatedAt, LastSeenAt, ExpiresAt, RevokedAt string
	OwnerKey                                                                           string // "owner" or the username that paired it (D-U1-14)
	Scopes                                                                             []Scope
}

// PairingCode describes one pairing code (never the code itself).
type PairingCode struct{ ID, Name, CreatedBy, OwnerKey, CreatedAt, ExpiresAt string }

// DeviceVerifier resolves a device cookie to a principal.
type DeviceVerifier interface {
	VerifyDevice(ctx context.Context, cookie string) (Principal, error)
}

// deviceSlider is implemented by verifiers that report whether the server-side
// expiry slid, so the middleware re-issues the cookie only then.
type deviceSlider interface {
	verify(ctx context.Context, cookie string) (p Principal, slid bool, err error)
}

// DeviceStore owns devices and pairing codes.
type DeviceStore struct {
	st    *store.Store
	key   []byte
	now   func() time.Time
	newID func() string
	users map[string]Role // the configured users (startup, D-U1-4); resolves a device's owner key to its role
}

// NewDeviceStore builds a DeviceStore; nil now means time.Now.
func NewDeviceStore(st *store.Store, key []byte, now func() time.Time, newID func() string) *DeviceStore {
	if now == nil {
		now = time.Now
	}
	return &DeviceStore{st: st, key: key, now: now, newID: newID}
}

// UseUsers sets the user map a device's owner key is resolved against on every request (C312.2). Call once at startup;
// without it only the Owner's devices authenticate (a non-owner key answers invalid, never the Owner).
func (d *DeviceStore) UseUsers(users map[string]Role) { d.users = maps.Clone(users) }

func (d *DeviceStore) ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// normalizeCode maps user input to the canonical 8 Crockford symbols.
func normalizeCode(in string) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToUpper(in) {
		switch r {
		case '-', ' ':
			continue
		case 'I', 'L':
			r = '1'
		case 'O':
			r = '0'
		}
		if r > 127 || !strings.ContainsRune(crockford, r) {
			return "", false
		}
		b.WriteRune(r)
	}
	s := b.String()
	return s, len(s) == codeSymbols
}

func (d *DeviceStore) codeHash(norm string) string { return mac(d.key, "pair\n"+norm) }

// CreatePairingCode mints a pairing code; by.Kind must be KindHuman. It revokes
// every live code first, in the same transaction (this is the only writer of
// pairing_codes.revoked_at).
func (d *DeviceStore) CreatePairingCode(ctx context.Context, by Principal, name string) (code string, pc PairingCode, err error) {
	if d == nil || d.st == nil {
		return "", PairingCode{}, errors.ErrUnsupported
	}
	ownerKey, ok := OwnerKeyOf(by) // the code is bound to the calling human; a device or agent never pairs (D-U1-14)
	if !ok {
		return "", PairingCode{}, ErrNotHuman
	}
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameRunes || strings.TrimSpace(name) == "" {
		return "", PairingCode{}, ErrInvalidName
	}
	raw := make([]byte, codeSymbols)
	if _, err := rand.Read(raw); err != nil {
		return "", PairingCode{}, err
	}
	sym := make([]byte, codeSymbols)
	for i, b := range raw {
		sym[i] = crockford[b&31] // 256 is a multiple of 32: no modulo bias
	}
	norm := string(sym)
	code = norm[:4] + "-" + norm[4:]
	now := d.now()
	pc = PairingCode{ID: d.newID(), Name: name, CreatedBy: by.Key(), OwnerKey: ownerKey, CreatedAt: d.ts(now), ExpiresAt: d.ts(now.Add(PairingTTL))}
	tx, err := d.st.W.BeginTx(ctx, nil)
	if err != nil {
		return "", PairingCode{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := d.checkLimit(ctx, tx, ownerKey); err != nil {
		return "", PairingCode{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE pairing_codes SET revoked_at=? WHERE redeemed_at IS NULL AND revoked_at IS NULL AND expires_at>? AND owner_key=?`,
		pc.CreatedAt, pc.CreatedAt, ownerKey); err != nil {
		return "", PairingCode{}, fmt.Errorf("supersede pairing codes: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO pairing_codes(id,hmac_hash,name,created_by,created_at,expires_at,owner_key) VALUES(?,?,?,?,?,?,?)`,
		pc.ID, d.codeHash(norm), pc.Name, pc.CreatedBy, pc.CreatedAt, pc.ExpiresAt, ownerKey); err != nil {
		return "", PairingCode{}, fmt.Errorf("create pairing code: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", PairingCode{}, err
	}
	return code, pc, nil
}

// checkLimit refuses a non-owner key that already holds MaxUserDevices active devices (the Owner is unlimited).
func (d *DeviceStore) checkLimit(ctx context.Context, tx *sql.Tx, ownerKey string) error {
	if ownerKey == OwnerKeyOwner {
		return nil
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE owner_key=? AND revoked_at IS NULL AND expires_at>?`, ownerKey, d.ts(d.now())).Scan(&n); err != nil {
		return fmt.Errorf("count devices: %w", err)
	}
	if n >= MaxUserDevices {
		return ErrDeviceLimit
	}
	return nil
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Redeem exchanges a pairing code for a device cookie. Every failure is
// ErrInvalidCode, so wrong, expired, used and superseded codes look alike.
func (d *DeviceStore) Redeem(ctx context.Context, code, userAgent string) (cookie string, dev DeviceInfo, err error) {
	if d == nil || d.st == nil {
		return "", DeviceInfo{}, errors.ErrUnsupported
	}
	norm, ok := normalizeCode(code)
	if !ok {
		return "", DeviceInfo{}, ErrInvalidCode
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", DeviceInfo{}, err
	}
	cookie = "mmd_" + strings.ToLower(b32.EncodeToString(raw))
	now := d.now()
	tx, err := d.st.W.BeginTx(ctx, nil)
	if err != nil {
		return "", DeviceInfo{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var codeID, name, by, ownerKey string
	err = tx.QueryRowContext(ctx,
		`SELECT id,name,created_by,owner_key FROM pairing_codes WHERE hmac_hash=? AND redeemed_at IS NULL AND revoked_at IS NULL AND expires_at>?`,
		d.codeHash(norm), d.ts(now)).Scan(&codeID, &name, &by, &ownerKey)
	if errors.Is(err, sql.ErrNoRows) {
		return "", DeviceInfo{}, ErrInvalidCode
	}
	if err != nil {
		return "", DeviceInfo{}, fmt.Errorf("redeem pairing code: %w", err)
	}
	// The owner key comes from the code row the creator's login wrote, never from the (unauthenticated) request. A user
	// who filled the limit between creating and redeeming the code, or who was removed, gets the generic invalid answer.
	if _, _, _, ok := ownerIdentity(d.users, ownerKey, nil); !ok || d.checkLimit(ctx, tx, ownerKey) != nil {
		return "", DeviceInfo{}, ErrInvalidCode
	}
	dev = DeviceInfo{
		OwnerKey: ownerKey, ID: d.newID(), Name: name, Prefix: cookie[4:12], UserAgent: truncateUTF8(userAgent, maxUserAgent), PairedBy: by,
		CreatedAt: d.ts(now), ExpiresAt: d.ts(now.Add(DeviceTTL)), Scopes: []Scope{ScopeRead, ScopeWrite},
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO devices(id,name,prefix,hmac_hash,scopes,user_agent,paired_by,created_at,expires_at,owner_key) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		dev.ID, dev.Name, dev.Prefix, mac(d.key, cookie), "read,write", dev.UserAgent, dev.PairedBy, dev.CreatedAt, dev.ExpiresAt, ownerKey); err != nil {
		return "", DeviceInfo{}, fmt.Errorf("create device: %w", err)
	}
	// The single writer connection serialises redemptions, and the SELECT above
	// ran inside this transaction, so the row is still unredeemed here.
	if _, err := tx.ExecContext(ctx,
		`UPDATE pairing_codes SET redeemed_at=?, device_id=? WHERE id=?`, dev.CreatedAt, dev.ID, codeID); err != nil {
		return "", DeviceInfo{}, fmt.Errorf("redeem pairing code: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", DeviceInfo{}, err
	}
	return cookie, dev, nil
}

// VerifyDevice resolves a device cookie. A nil receiver is a disabled store.
func (d *DeviceStore) VerifyDevice(ctx context.Context, cookie string) (Principal, error) {
	p, _, err := d.verify(ctx, cookie)
	return p, err
}

// verify resolves cookie and reports whether the server-side expiry slid.
// Bookkeeping writes are best effort under a short bounded context.
func (d *DeviceStore) verify(ctx context.Context, cookie string) (Principal, bool, error) {
	if d == nil || d.st == nil || !deviceRE.MatchString(cookie) {
		return Principal{}, false, ErrInvalidDevice
	}
	var id, name, scopes, exp, ownerKey string
	var seen, revoked sql.NullString
	err := d.st.R.QueryRowContext(ctx,
		`SELECT id,name,scopes,expires_at,last_seen_at,revoked_at,owner_key FROM devices WHERE hmac_hash=?`, mac(d.key, cookie)).
		Scan(&id, &name, &scopes, &exp, &seen, &revoked, &ownerKey)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && revoked.Valid) {
		return Principal{}, false, ErrInvalidDevice
	}
	if err != nil {
		return Principal{}, false, fmt.Errorf("verify device: %w", err)
	}
	role, user, eff, ok := ownerIdentity(d.users, ownerKey, splitScopes(scopes))
	if !ok { // the owner left the user map: the device is inert (C312.5), never the Owner's
		return Principal{}, false, ErrInvalidDevice
	}
	n := d.now().UTC()
	expires, perr := time.Parse(time.RFC3339, exp)
	if perr != nil || !n.Before(expires) {
		return Principal{}, false, ErrInvalidDevice
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 250*time.Millisecond)
	defer cancel()
	slid := false
	if expires.Sub(n) < DeviceTTL-deviceSlide {
		_, werr := d.st.W.ExecContext(wctx, `UPDATE devices SET expires_at=? WHERE id=? AND revoked_at IS NULL`, d.ts(n.Add(DeviceTTL)), id)
		slid = werr == nil
	}
	if ls, perr := time.Parse(time.RFC3339, seen.String); !seen.Valid || perr != nil || n.Sub(ls) >= deviceTouch {
		_, _ = d.st.W.ExecContext(wctx, `UPDATE devices SET last_seen_at=? WHERE id=?`, d.ts(n), id)
	}
	return Principal{Kind: KindDevice, ID: id, Name: name, Role: role, User: user, Scopes: eff, Via: "device"}, slid, nil
}

const deviceCols = `id,name,prefix,scopes,user_agent,paired_by,created_at,last_seen_at,expires_at,revoked_at,owner_key`

type rowScanner interface{ Scan(dest ...any) error }

func scanDevice(r rowScanner) (DeviceInfo, error) {
	var di DeviceInfo
	var scopes string
	var seen, rev sql.NullString
	if err := r.Scan(&di.ID, &di.Name, &di.Prefix, &scopes, &di.UserAgent, &di.PairedBy, &di.CreatedAt, &seen, &di.ExpiresAt, &rev, &di.OwnerKey); err != nil {
		return DeviceInfo{}, err
	}
	di.Scopes, di.LastSeenAt, di.RevokedAt = splitScopes(scopes), seen.String, rev.String
	return di, nil
}

// List returns the devices viewer may see, newest first: every user's for the Owner, only the viewer's own otherwise
// (D-U1-14). It never returns hashes.
func (d *DeviceStore) List(ctx context.Context, viewer Principal) ([]DeviceInfo, error) {
	if d == nil || d.st == nil {
		return nil, errors.ErrUnsupported
	}
	q, args := `SELECT `+deviceCols+` FROM devices`, []any(nil)
	if !SeesAllDevices(viewer) {
		key, ok := OwnerKeyOf(viewer)
		if !ok {
			return nil, nil
		}
		q, args = q+` WHERE owner_key=?`, []any{key}
	}
	rows, err := d.st.R.QueryContext(ctx, q+` ORDER BY created_at DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceInfo
	for rows.Next() {
		di, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, di)
	}
	return out, rows.Err()
}

// Get returns one device; ErrNotFound if absent.
func (d *DeviceStore) Get(ctx context.Context, id string) (DeviceInfo, error) {
	if d == nil || d.st == nil {
		return DeviceInfo{}, errors.ErrUnsupported
	}
	di, err := scanDevice(d.st.R.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM devices WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return DeviceInfo{}, ErrNotFound
	}
	return di, err
}

// Revoke revokes a device on behalf of by (idempotent): the Owner may revoke any device at once, any other user only
// their own, and a foreign id is ErrNotFound exactly like an unknown one.
func (d *DeviceStore) Revoke(ctx context.Context, by Principal, id string) (DeviceInfo, error) {
	if d == nil || d.st == nil {
		return DeviceInfo{}, errors.ErrUnsupported
	}
	if !SeesAllDevices(by) {
		di, err := d.Get(ctx, id)
		if err != nil {
			return DeviceInfo{}, err
		}
		if key, ok := OwnerKeyOf(by); !ok || key != di.OwnerKey {
			return DeviceInfo{}, ErrNotFound
		}
	}
	return d.RevokeSelf(ctx, id)
}

// RevokeSelf revokes the device with id without an ownership check: the caller is that device (revoke_own_device) or has
// already checked ownership.
func (d *DeviceStore) RevokeSelf(ctx context.Context, id string) (DeviceInfo, error) {
	if d == nil || d.st == nil {
		return DeviceInfo{}, errors.ErrUnsupported
	}
	res, err := d.st.W.ExecContext(ctx, `UPDATE devices SET revoked_at=COALESCE(revoked_at,?) WHERE id=?`, d.ts(d.now()), id)
	if err != nil {
		return DeviceInfo{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return DeviceInfo{}, ErrNotFound
	}
	return d.Get(ctx, id)
}
