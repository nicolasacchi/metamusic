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
	t.Parallel()
	var d *DeviceStore
	if _, err := d.VerifyDevice(context.Background(), "mmd_x"); !errors.Is(err, ErrInvalidDevice) {
		t.Fatalf("err = %v, want ErrInvalidDevice", err)
	}
}

// TestMiddleware_NilDevices: the device verifier is optional.
func TestMiddleware_NilDevices(t *testing.T) {
	t.Parallel()
	h := Middleware(config.Config{AuthMode: "proxy"}, config.Secrets{}, Verifiers{Tokens: fakeTokens{}, Sessions: fakeSessions{}, Devices: nil})(http.NotFoundHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/ping", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
