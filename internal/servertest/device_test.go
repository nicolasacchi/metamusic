package servertest_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/servertest"
)

func TestRig_Device(t *testing.T) {
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
