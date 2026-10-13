package servertest

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/auth"
)

var pairSeq atomic.Uint64

// Device returns a REST client for a paired device. The pairing code is minted
// through the real operator operation (create_pairing_code, as the human) and
// redeemed against the rig's device store, which is what POST /auth/pair does.
// The client sends the resulting mm_dev cookie.
func (r *Rig) Device(tb testing.TB) *Client {
	tb.Helper()
	c, _ := r.DeviceWithCookie(tb, "Test phone")
	return c
}

// DeviceWithCookie is Device with a chosen label; it also returns the cookie value.
func (r *Rig) DeviceWithCookie(tb testing.TB, name string) (*Client, string) {
	tb.Helper()
	res := r.Human(tb).Do(tb, "POST", "/human/api/devices/pairing-codes", map[string]any{"name": name},
		"Idempotency-Key", fmt.Sprintf("servertest-pair-%d", pairSeq.Add(1)))
	if res.Status != http.StatusCreated {
		tb.Fatalf("servertest: create_pairing_code: status %d: %s", res.Status, res.Body)
	}
	code, _ := res.JSON(tb).(map[string]any)["code"].(string)
	cookie, _, err := r.Services.Devices.Redeem(context.Background(), code, "servertest/1.0")
	if err != nil {
		tb.Fatalf("servertest: redeeming pairing code: %v", err)
	}
	return newClient(r.URL, http.Header{"Cookie": {auth.DeviceCookie + "=" + cookie}}, false), cookie
}

// Bearer returns a REST client that presents an existing token plaintext.
func Bearer(r *Rig, token string) *Client {
	return newClient(r.URL, http.Header{"Authorization": {"Bearer " + token}}, false)
}
