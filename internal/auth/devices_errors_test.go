package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/config"
)

func TestDeviceStore_Defaults(t *testing.T) {
	ds := NewDeviceStore(newStore(t), key, nil, func() string { return "id" })
	if ds.now == nil {
		t.Fatal("nil clock must default to time.Now")
	}
	ts := NewTokenStore(newStore(t), key, nil)
	if ts.now == nil {
		t.Fatal("nil token clock must default to time.Now")
	}
}

func TestDeviceStore_NilStoreIsUnsupported(t *testing.T) {
	ctx := context.Background()
	var nilDS *DeviceStore
	for name, f := range map[string]func() error{
		"create": func() error { _, _, err := nilDS.CreatePairingCode(ctx, human, "x"); return err },
		"redeem": func() error { _, _, err := nilDS.Redeem(ctx, "ABCD-EFGH", "ua"); return err },
		"list":   func() error { _, err := nilDS.List(ctx); return err },
		"get":    func() error { _, err := nilDS.Get(ctx, "x"); return err },
		"revoke": func() error { _, err := nilDS.Revoke(ctx, "x"); return err },
	} {
		if err := f(); !errors.Is(err, errors.ErrUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDeviceStore_GetAndNormalize(t *testing.T) {
	ctx := context.Background()
	ds, _ := newDevices(t)
	code, _, _ := ds.CreatePairingCode(ctx, human, "Phone")
	_, dev, _ := ds.Redeem(ctx, code, "ua")
	got, err := ds.Get(ctx, dev.ID)
	if err != nil || got.ID != dev.ID || got.Name != "Phone" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := ds.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if _, ok := normalizeCode("ÄBCD-EFGH"); ok {
		t.Fatal("non-ASCII accepted")
	}
	if n, ok := normalizeCode("il0o-abcd"); !ok || n != "1100ABCD" {
		t.Fatalf("normalize = %q %v", n, ok)
	}
}

// With the database closed every operation reports an error instead of panicking.
func TestDeviceStore_DatabaseErrors(t *testing.T) {
	ctx := context.Background()
	ds, _ := newDevices(t)
	code, _, _ := ds.CreatePairingCode(ctx, human, "Phone")
	cookie, dev, _ := ds.Redeem(ctx, code, "ua")
	ds.st.Close()
	if _, _, err := ds.CreatePairingCode(ctx, human, "x"); err == nil {
		t.Error("create")
	}
	if _, _, err := ds.Redeem(ctx, code, "ua"); err == nil || errors.Is(err, ErrInvalidCode) {
		t.Errorf("redeem: %v", err)
	}
	if _, err := ds.VerifyDevice(ctx, cookie); err == nil || errors.Is(err, ErrInvalidDevice) {
		t.Errorf("verify: %v", err)
	}
	if _, err := ds.List(ctx); err == nil {
		t.Error("list")
	}
	if _, err := ds.Get(ctx, dev.ID); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("get: %v", err)
	}
	if _, err := ds.Revoke(ctx, dev.ID); err == nil {
		t.Error("revoke")
	}
}

func TestDevice_CorruptRows(t *testing.T) {
	ctx := context.Background()
	ds, _ := newDevices(t)
	code, _, _ := ds.CreatePairingCode(ctx, human, "Phone")
	cookie, dev, _ := ds.Redeem(ctx, code, "ua")
	if _, err := ds.st.W.Exec(`UPDATE devices SET expires_at='garbage' WHERE id=?`, dev.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.VerifyDevice(ctx, cookie); !errors.Is(err, ErrInvalidDevice) {
		t.Fatalf("unparseable expiry: %v", err)
	}
}

func TestPairing_HandlerErrors(t *testing.T) {
	ds, _ := newDevices(t)
	h := PairingHandler(PairingOptions{Cfg: config.Config{AuthMode: "proxy"}, Devices: ds, FailDelay: time.Millisecond})
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/auth/pair", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	if rec := post("%zz"); rec.Code != 400 {
		t.Errorf("bad form: %d", rec.Code)
	}
	ds.st.Close()
	code := "ABCD-EFGH"
	if rec := post(url.Values{"code": {code}}.Encode()); rec.Code != 500 || strings.Contains(rec.Body.String(), "ABCD") {
		t.Errorf("db down: %d", rec.Code)
	}
	// all failure-delay slots busy: further wrong codes get 429 at once
	ds2, _ := newDevices(t)
	h = PairingHandler(PairingOptions{Cfg: config.Config{AuthMode: "proxy"}, Devices: ds2, FailDelay: 2 * time.Second})
	done := make(chan int, maxFailWaiters)
	for range maxFailWaiters {
		go func() {
			r := httptest.NewRequest("POST", "/auth/pair", strings.NewReader("code=ZZZZ-ZZZZ"))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			ctx, cancel := context.WithTimeout(r.Context(), 700*time.Millisecond)
			defer cancel()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r.WithContext(ctx))
			done <- rec.Code
		}()
	}
	time.Sleep(300 * time.Millisecond)
	r := httptest.NewRequest("POST", "/auth/pair", strings.NewReader("code=ZZZZ-ZZZZ"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 429 {
		t.Errorf("saturated: %d", rec.Code)
	}
	for range maxFailWaiters {
		<-done
	}
}

func TestStepUp_FallbackClockAndErrors(t *testing.T) {
	// a verifier without a clock falls back to wall time
	p := Principal{Kind: KindHuman, StepUpAt: time.Now()}
	if !stepUpFresh(fakeSessions{}, p) {
		t.Fatal("fresh step-up rejected")
	}
	p.StepUpAt = time.Now().Add(-stepUpWindow - time.Minute)
	if stepUpFresh(fakeSessions{}, p) || stepUpFresh(fakeSessions{}, Principal{}) {
		t.Fatal("stale step-up accepted")
	}

	s := newStepRig(t)
	post := func(body string, cookie bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/auth/step-up", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookie {
			r.AddCookie(s.cookie)
		}
		rec := httptest.NewRecorder()
		s.la.Handler().ServeHTTP(rec, r)
		return rec
	}
	if rec := post("%zz", true); rec.Code != 400 {
		t.Errorf("bad form: %d", rec.Code)
	}
	if rec := post("password="+stepPW, false); rec.Code != 303 || rec.Header().Get("Location") != "/auth/login" {
		t.Errorf("no session: %d", rec.Code)
	}
	// busy: all login slots taken
	s.la.pending.Store(maxPendingLogins)
	if rec := post("password="+stepPW, true); rec.Code != 503 {
		t.Errorf("busy: %d", rec.Code)
	}
	s.la.pending.Store(0)
	// context already done while waiting for a hashing slot
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range maxHashers {
		s.la.sem <- struct{}{}
	}
	r := httptest.NewRequest("POST", "/auth/step-up", strings.NewReader("password="+stepPW)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(s.cookie)
	rec := httptest.NewRecorder()
	s.la.Handler().ServeHTTP(rec, r)
	for range maxHashers {
		<-s.la.sem
	}
	if rec.Code != 503 {
		t.Errorf("hasher busy: %d", rec.Code)
	}
	// database down: 500
	s.la.st.Close()
	if rec := post("password="+stepPW, true); rec.Code != http.StatusInternalServerError {
		t.Errorf("db down: %d", rec.Code)
	}
}

func TestDeviceStore_WriteFailures(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, ddl string
		redeem    bool
	}{
		{"supersede", `ALTER TABLE pairing_codes RENAME TO pairing_codes_x`, false},
		{"insert code", `CREATE TRIGGER t BEFORE INSERT ON pairing_codes BEGIN SELECT RAISE(ABORT,'no'); END`, false},
		{"insert device", `CREATE TRIGGER t BEFORE INSERT ON devices BEGIN SELECT RAISE(ABORT,'no'); END`, true},
		{"mark redeemed", `CREATE TRIGGER t BEFORE UPDATE OF redeemed_at ON pairing_codes BEGIN SELECT RAISE(ABORT,'no'); END`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds, _ := newDevices(t)
			code := ""
			if tc.redeem {
				code, _, _ = ds.CreatePairingCode(ctx, human, "Phone")
			}
			if _, err := ds.st.W.Exec(tc.ddl); err != nil {
				t.Fatal(err)
			}
			if tc.redeem {
				if _, _, err := ds.Redeem(ctx, code, "ua"); err == nil || errors.Is(err, ErrInvalidCode) {
					t.Fatalf("redeem: %v", err)
				}
				return
			}
			if _, _, err := ds.CreatePairingCode(ctx, human, "x"); err == nil {
				t.Fatal("create succeeded")
			}
		})
	}
}

func TestDeviceStore_ListScanError(t *testing.T) {
	ds, _ := newDevices(t)
	ctx := context.Background()
	code, _, _ := ds.CreatePairingCode(ctx, human, "Phone")
	if _, _, err := ds.Redeem(ctx, code, "ua"); err != nil {
		t.Fatal(err)
	}
	// a NULL in a NOT NULL-scanned column cannot be stored, so corrupt via a view-free rewrite
	if _, err := ds.st.W.Exec(`PRAGMA ignore_check_constraints=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := ds.st.W.Exec(`UPDATE devices SET user_agent=NULL`); err == nil {
		if _, err := ds.List(ctx); err == nil {
			t.Fatal("List accepted a NULL user agent")
		}
	}
}

func TestPairing_StartCookieLimitsAndNilDevices(t *testing.T) {
	pr := newPairRig(t, "local", fakeSessions{})
	ctx := context.Background()
	code, _, _ := pr.ds.CreatePairingCode(ctx, human, "Phone")
	ck, _, _ := pr.ds.Redeem(ctx, code, "ua")
	// valid cookie past the examined limit is ignored; slid expiry re-issues the cookie
	many := ""
	for range maxSessionCookies {
		many += DeviceCookie + "=garbage; "
	}
	if rec := pr.req("GET", "/start", "", "Cookie", many+DeviceCookie+"="+ck); rec.Code != 200 {
		t.Fatalf("cookie beyond the limit honoured: %d", rec.Code)
	}
	pr.clk.Advance(2 * time.Hour)
	rec := pr.req("GET", "/start", "", "Cookie", DeviceCookie+"="+ck)
	if rec.Code != 302 || !setsDevice(rec) {
		t.Fatalf("slid cookie not re-issued: %d %v", rec.Code, rec.Header())
	}
	sess := ""
	for range maxSessionCookies {
		sess += "__Host-mm_session=bad; "
	}
	if rec := pr.req("GET", "/start", "", "Cookie", sess+"__Host-mm_session=goodsess"); rec.Code != 200 {
		t.Fatalf("session beyond the limit honoured: %d", rec.Code)
	}
	h := PairingHandler(PairingOptions{Cfg: config.Config{AuthMode: "proxy"}})
	r := httptest.NewRequest("POST", "/auth/pair", strings.NewReader("code=ABCD-EFGH"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	if out.Code != 404 {
		t.Fatalf("nil devices POST: %d", out.Code)
	}
}

func TestStepUp_UpdateFailure(t *testing.T) {
	s := newStepRig(t)
	ctx := context.Background()
	if _, err := s.la.st.W.Exec(`CREATE TRIGGER t BEFORE UPDATE OF step_up_at ON human_sessions BEGIN SELECT RAISE(ABORT,'no'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.la.StepUp(ctx, s.cookie.Value, stepPW); err == nil || errors.Is(err, ErrInvalidSession) || errors.Is(err, ErrBadCredentials) {
		t.Errorf("update failure: %v", err)
	}
	// the account row disappears: the join finds nothing
	if _, err := s.la.st.W.Exec(`DROP TRIGGER t`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.la.st.W.Exec(`DELETE FROM human_account`); err != nil {
		t.Fatal(err)
	}
	if err := s.la.StepUp(ctx, s.cookie.Value, stepPW); !errors.Is(err, ErrInvalidSession) {
		t.Errorf("no account: %v", err)
	}
}
