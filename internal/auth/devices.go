package auth

import (
	"context"
	"errors"
	"time"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Device constants (C26). Bodies belong to M2a-B9.
const (
	DeviceCookie = "mm_dev"
	DeviceTTL    = 90 * 24 * time.Hour // sliding
	PairingTTL   = 5 * time.Minute
)

// Device errors.
var (
	ErrInvalidDevice = errors.New("invalid device")
	ErrInvalidCode   = errors.New("invalid pairing code")
)

// DeviceInfo describes one paired device; it never carries the cookie hash.
type DeviceInfo struct {
	ID, Name, Prefix, UserAgent, PairedBy, CreatedAt, LastSeenAt, ExpiresAt, RevokedAt string
	Scopes                                                                             []Scope
}

// PairingCode describes one pairing code (never the code itself).
type PairingCode struct{ ID, Name, CreatedBy, CreatedAt, ExpiresAt string }

// DeviceVerifier resolves a device cookie to a principal.
type DeviceVerifier interface {
	VerifyDevice(ctx context.Context, cookie string) (Principal, error)
}

// DeviceStore owns devices and pairing codes.
type DeviceStore struct {
	st    *store.Store
	key   []byte
	now   func() time.Time
	newID func() string
}

// NewDeviceStore builds a DeviceStore; nil now means time.Now.
func NewDeviceStore(st *store.Store, key []byte, now func() time.Time, newID func() string) *DeviceStore {
	if now == nil {
		now = time.Now
	}
	return &DeviceStore{st: st, key: key, now: now, newID: newID}
}

// CreatePairingCode mints a pairing code; by.Kind must be KindHuman.
func (d *DeviceStore) CreatePairingCode(ctx context.Context, by Principal, name string) (code string, pc PairingCode, err error) {
	// STUB(M2a-B9)
	return "", PairingCode{}, errors.ErrUnsupported
}

// Redeem exchanges a pairing code for a device cookie.
func (d *DeviceStore) Redeem(ctx context.Context, code, userAgent string) (cookie string, dev DeviceInfo, err error) {
	// STUB(M2a-B9)
	return "", DeviceInfo{}, errors.ErrUnsupported
}

// VerifyDevice resolves a device cookie. A nil receiver is a disabled store.
func (d *DeviceStore) VerifyDevice(ctx context.Context, cookie string) (Principal, error) {
	// STUB(M2a-B9)
	return Principal{}, ErrInvalidDevice
}

// List returns every device, newest first; it never returns hashes.
func (d *DeviceStore) List(ctx context.Context) ([]DeviceInfo, error) {
	// STUB(M2a-B9)
	return nil, errors.ErrUnsupported
}

// Revoke revokes a device (idempotent); ErrNotFound for an unknown id.
func (d *DeviceStore) Revoke(ctx context.Context, id string) (DeviceInfo, error) {
	// STUB(M2a-B9)
	return DeviceInfo{}, errors.ErrUnsupported
}
