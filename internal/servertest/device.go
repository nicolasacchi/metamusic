package servertest

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
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
	form := url.Values{"code": {code}}
	req, err := http.NewRequest(http.MethodPost, r.URL+"/auth/pair", strings.NewReader(form.Encode()))
	if err != nil {
		tb.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "servertest/1.0")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Do(req)
	if err != nil {
		tb.Fatalf("servertest: POST /auth/pair: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		tb.Fatalf("servertest: POST /auth/pair: status %d", resp.StatusCode)
	}
	var cookie string
	for _, c := range resp.Cookies() {
		if c.Name == auth.DeviceCookie {
			cookie = c.Value
		}
	}
	if cookie == "" {
		tb.Fatal("servertest: /auth/pair set no device cookie")
	}
	return newClient(r.URL, http.Header{"Cookie": {auth.DeviceCookie + "=" + cookie}}, false), cookie
}

// Bearer returns a REST client that presents an existing token plaintext.
func Bearer(r *Rig, token string) *Client {
	return newClient(r.URL, http.Header{"Authorization": {"Bearer " + token}}, false)
}
