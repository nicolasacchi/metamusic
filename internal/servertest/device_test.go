package servertest_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/subsonic/subsonictest"
)

func TestRig_Device(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"proxy", "local"} {
		t.Run(mode, func(t *testing.T) {
			r := servertest.New(t, servertest.Options{Mode: mode})
			d := r.Device(t)
			resp := d.Do(t, "GET", "/api/me", nil)
			if resp.Status != 200 || !strings.Contains(string(resp.Body), `"kind":"device"`) {
				t.Fatalf("whoami as device: %d %s", resp.Status, resp.Body)
			}
		})
	}
}

func TestRig_LocalHumanStepsUp(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Mode: "local"})
	h := r.Human(t)
	if resp := h.Do(t, "GET", "/human/api/devices", nil); resp.Status != 200 {
		t.Fatalf("after step-up: %d %s", resp.Status, resp.Body)
	}
	r.Clock.Advance(10*time.Minute + time.Second)
	resp := h.Do(t, "GET", "/human/api/devices", nil)
	if resp.Status != 403 || !strings.Contains(string(resp.Body), "STEP_UP_REQUIRED") {
		t.Fatalf("stale step-up: %d %s", resp.Status, resp.Body)
	}
	r.StepUp(t, h)
	if resp := h.Do(t, "GET", "/human/api/devices", nil); resp.Status != 200 {
		t.Fatalf("after second step-up: %d", resp.Status)
	}
	// proxy mode: /auth/step-up does not exist
	p := servertest.New(t, servertest.Options{})
	if resp := p.Human(t).Do(t, "GET", "/auth/step-up", nil); resp.Status != 404 {
		t.Fatalf("proxy /auth/step-up: %d", resp.Status)
	}
}

func TestDevicePairsThroughHTTP(t *testing.T) {
	t.Parallel()
	rig := servertest.New(t, servertest.Options{Canary: true})
	dev, cookie := rig.DeviceWithCookie(t, "Pair HTTP")
	if cookie == "" {
		t.Fatal("no cookie")
	}
	if r := dev.Do(t, "GET", "/api/ping", nil); r.Status != 200 {
		t.Fatalf("device ping: %d %s", r.Status, r.Body)
	}
	// the pairing pages are served by the real server, not the SPA
	resp, err := http.Get(rig.URL + "/pair")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("/pair: %d", resp.StatusCode)
	}
	// OnPaired side effects: audit row and notification
	rows := rig.Human(t).Do(t, "GET", "/api/audit?op=auth:pair", nil)
	if !strings.Contains(string(rows.Body), "auth:pair") {
		t.Fatalf("no auth:pair audit row: %d %s", rows.Status, rows.Body)
	}
	var note string
	for i := 0; i < 100 && note == ""; i++ {
		for _, q := range rig.Ntfy.Requests() {
			if strings.Contains(q.Body, "Device paired") {
				note = q.Body
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if note == "" {
		t.Fatal("no Device paired notification")
	}
}

func TestSec_PairAuditTransportIsPair(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Mode: "proxy"})
	r.Device(t)
	var tr string
	if err := r.Store.R.QueryRow(`SELECT transport FROM audit_log WHERE op = 'auth:pair'`).Scan(&tr); err != nil {
		t.Fatal(err)
	}
	if tr != "pair" {
		t.Fatalf("transport = %q, want pair", tr)
	}
}

// TestRig_DeviceForAndSubsonicClientFor: a device and a Subsonic client created for bob and carol go through the real pairing
// and creation operations, belong to that user and act with the user's role; an unknown user fails the test helper (checked by
// the rig's own tests, not here).
func TestRig_DeviceForAndSubsonicClientFor(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{})
	for user, role := range map[string]string{servertest.UserEditor: "editor", servertest.UserListener: "listener"} {
		d := r.DeviceFor(t, user)
		resp := d.Do(t, "GET", "/api/me", nil)
		if resp.Status != 200 || !strings.Contains(string(resp.Body), `"kind":"device"`) || !strings.Contains(string(resp.Body), role) {
			t.Errorf("%s's device whoami: %d %s", user, resp.Status, resp.Body)
		}
		c := r.SubsonicClientFor(t, user, subsonictest.ClientOpts{Write: true})
		if res := c.Call(t, "ping", nil, subsonictest.CallOpts{JSON: true}); res.HTTP != 200 || res.Code != 0 {
			t.Errorf("%s's Subsonic client ping: %d %d", user, res.HTTP, res.Code)
		}
	}
	var n int
	if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM devices WHERE owner_key IN ('bob','carol')`).Scan(&n); err != nil || n != 2 {
		t.Errorf("devices owned by bob and carol: %d %v", n, err)
	}
	if err := r.Store.R.QueryRow(`SELECT COUNT(*) FROM subsonic_clients WHERE owner_key IN ('bob','carol')`).Scan(&n); err != nil || n != 2 {
		t.Errorf("clients owned by bob and carol: %d %v", n, err)
	}
}
