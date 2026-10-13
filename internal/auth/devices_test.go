package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/config"
)

// TestDeviceStore_NilReceiver: a nil *DeviceStore is a disabled store, never a panic.
func TestDeviceStore_NilReceiver(t *testing.T) {
	var d *DeviceStore
	if _, err := d.VerifyDevice(context.Background(), "mmd_x"); !errors.Is(err, ErrInvalidDevice) {
		t.Fatalf("err = %v, want ErrInvalidDevice", err)
	}
}

func TestDeviceStore_StubsAreMarked(t *testing.T) {
	d := NewDeviceStore(nil, []byte("k"), nil, func() string { return "id" })
	if _, _, err := d.CreatePairingCode(context.Background(), Principal{Kind: KindHuman}, "Phone"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("CreatePairingCode = %v, want ErrUnsupported until M2a-B9", err)
	}
}

// TestMiddleware_NilDevices: the device verifier is optional.
func TestMiddleware_NilDevices(t *testing.T) {
	h := Middleware(config.Config{AuthMode: "proxy"}, config.Secrets{}, fakeTokens{}, fakeSessions{}, nil)(http.NotFoundHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/ping", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPairingHandler_Stub(t *testing.T) {
	rec := httptest.NewRecorder()
	PairingHandler(PairingOptions{}).ServeHTTP(rec, httptest.NewRequest("GET", "/start", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 until M2a-B9", rec.Code)
	}
}
