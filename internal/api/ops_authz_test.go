package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/api"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

var (
	tokenRE  = regexp.MustCompile(`^mmp_[a-z2-7]{52}$`)
	pairRE   = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)
	problemC = func(t *testing.T, r *servertest.Response) string {
		t.Helper()
		m, _ := r.JSON(t).(map[string]any)
		s, _ := m["code"].(string)
		return s
	}
)

func respObj(t *testing.T, r *servertest.Response) map[string]any {
	t.Helper()
	m, ok := r.JSON(t).(map[string]any)
	if !ok {
		t.Fatalf("%s %s: not an object: %s", r.Method, r.Path, r.Body)
	}
	return m
}

func TestCreateToken_TTL(t *testing.T) {
	t.Parallel()
	rig := servertest.New(t, servertest.Options{Canary: true})
	human := rig.Human(t)
	mk := func(key string, body map[string]any) *servertest.Response {
		body["name"], body["scopes"] = key, []string{"read"}
		return human.Do(t, "POST", "/human/api/tokens", body, "Idempotency-Key", key)
	}
	// the rig runs on a fixed clock, so lifetimes are measured against the response's own created_at
	lifetime := func(r *servertest.Response) time.Duration {
		if r.Status != 201 {
			t.Fatalf("create: %d %s", r.Status, r.Body)
		}
		o := respObj(t, r)
		created, err := time.Parse(time.RFC3339, o["created_at"].(string))
		if err != nil {
			t.Fatal(err)
		}
		exp, ok := o["expires_at"].(string)
		if !ok {
			return 0 // null: never expires
		}
		at, err := time.Parse(time.RFC3339, exp)
		if err != nil {
			t.Fatal(err)
		}
		return at.Sub(created)
	}
	if d := lifetime(mk("default", map[string]any{})); d != 90*24*time.Hour {
		t.Fatalf("default lifetime = %v, want 90 days", d)
	}
	if d := lifetime(mk("never", map[string]any{"ttl_days": 0})); d != 0 {
		t.Fatalf("ttl_days 0 lifetime = %v, want none", d)
	}
	if d := lifetime(mk("week", map[string]any{"ttl_days": 7})); d != 7*24*time.Hour {
		t.Fatalf("ttl_days 7 lifetime = %v", d)
	}
	for _, bad := range []int{-1, 3651} {
		if r := mk("bad", map[string]any{"ttl_days": bad}); r.Status != 422 {
			t.Errorf("ttl_days %d: %d", bad, r.Status)
		}
	}
}

func TestTokenOps(t *testing.T) {
	t.Parallel()
	rig := servertest.New(t, servertest.Options{Canary: true})
	human := rig.Human(t)

	// create_token: plaintext once
	res := human.Do(t, "POST", "/human/api/tokens", map[string]any{"name": "bot", "scopes": []string{"read", "write"}}, "Idempotency-Key", "tok-1")
	if res.Status != 201 {
		t.Fatalf("create_token: %d %s", res.Status, res.Body)
	}
	created := respObj(t, res)
	plain, _ := created["token"].(string)
	if !tokenRE.MatchString(plain) || created["name"] != "bot" || created["prefix"] != plain[4:12] || created["revoked_at"] != nil {
		t.Fatalf("created = %v", created)
	}
	id := created["id"].(string)
	var note string
	for i := 0; i < 100 && note == ""; i++ {
		if reqs := rig.Ntfy.Requests(); len(reqs) > 0 {
			note = reqs[0].Body
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(note, "Token created") || strings.Contains(note, plain) {
		t.Fatalf("ntfy message = %q", note)
	}
	replay := human.Do(t, "POST", "/human/api/tokens", map[string]any{"name": "bot", "scopes": []string{"read", "write"}}, "Idempotency-Key", "tok-1")
	rb := respObj(t, replay)
	if replay.Status != 201 || rb["token"] != nil || rb["id"] != id || strings.Contains(string(replay.Body), plain) {
		t.Fatalf("replay = %d %s", replay.Status, replay.Body)
	}
	for _, bad := range []map[string]any{
		{"name": "", "scopes": []string{"read"}}, {"name": "x", "scopes": []string{}},
		{"name": "x", "scopes": []string{"read", "read"}}, {"name": "x", "scopes": []string{"root"}},
	} {
		if r := human.Do(t, "POST", "/human/api/tokens", bad, "Idempotency-Key", "tok-bad"); r.Status != 422 {
			t.Errorf("%v: %d", bad, r.Status)
		}
	}

	// list_tokens: no hash fields; the new token works
	agent := servertest.Bearer(rig, plain)
	list := respObj(t, agent.Do(t, "GET", "/api/tokens", nil))
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", list)
	}
	item := items[0].(map[string]any)
	keys := make([]string, 0, len(item))
	for k := range item {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"created_at", "expires_at", "id", "last_used_at", "name", "prefix", "revoked_at", "scopes"}; !slices.Equal(keys, want) {
		t.Fatalf("keys = %v", keys)
	}
	if item["last_used_at"] == nil {
		t.Fatal("last_used_at not recorded for the calling token")
	}

	// revoke_token: that token gets 401
	rv := human.Do(t, "POST", "/human/api/tokens/"+id+"/revoke", nil)
	rvb := respObj(t, rv)
	if rv.Status != 200 || rvb["revoked_at"] == nil || rvb["id"] != id {
		t.Fatalf("revoke_token: %d %s", rv.Status, rv.Body)
	}
	if r := agent.Do(t, "GET", "/api/tokens", nil); r.Status != 401 {
		t.Fatalf("revoked token: %d", r.Status)
	}
	if r := human.Do(t, "POST", "/human/api/tokens/"+id+"/revoke", nil); r.Status != 200 || respObj(t, r)["revoked_at"] != rvb["revoked_at"] {
		t.Fatalf("second revoke: %d %s", r.Status, r.Body)
	}
	if r := human.Do(t, "POST", "/human/api/tokens/nope/revoke", nil); r.Status != 404 || problemC(t, r) != "NOT_FOUND" {
		t.Fatalf("unknown id: %d %s", r.Status, r.Body)
	}

	// revoke_own_token: an agent retires itself with read scope only; a human may not
	own, ownID := rig.Agent(t, auth.ScopeRead)
	r := own.Do(t, "POST", "/api/tokens/self/revoke", nil)
	ob := respObj(t, r)
	if r.Status != 200 || ob["id"] != ownID || ob["revoked_at"] == nil {
		t.Fatalf("revoke_own_token: %d %s", r.Status, r.Body)
	}
	if r := own.Do(t, "GET", "/api/me", nil); r.Status != 401 {
		t.Fatalf("self-revoked token: %d", r.Status)
	}
	if r := human.Do(t, "POST", "/api/tokens/self/revoke", nil); r.Status != 403 || problemC(t, r) != "NOT_A_TOKEN" {
		t.Fatalf("human self-revoke: %d %s", r.Status, r.Body)
	}
	dev := rig.Device(t)
	if r := dev.Do(t, "POST", "/api/tokens/self/revoke", nil); r.Status != 403 || problemC(t, r) != "NOT_A_TOKEN" {
		t.Fatalf("device self-revoke: %d %s", r.Status, r.Body)
	}
}

func TestDeviceOps(t *testing.T) {
	t.Parallel()
	rig := servertest.New(t, servertest.Options{})
	human := rig.Human(t)

	// create_pairing_code
	res := human.Do(t, "POST", "/human/api/devices/pairing-codes", map[string]any{"name": "Kitchen tablet"}, "Idempotency-Key", "pair-1")
	pc := respObj(t, res)
	code, _ := pc["code"].(string)
	if res.Status != 201 || !pairRE.MatchString(code) || pc["pair_url"] != "http://mm.example.com/pair#"+code || pc["name"] != "Kitchen tablet" {
		t.Fatalf("create_pairing_code: %d %s", res.Status, res.Body)
	}
	replay := human.Do(t, "POST", "/human/api/devices/pairing-codes", map[string]any{"name": "Kitchen tablet"}, "Idempotency-Key", "pair-1")
	rb := respObj(t, replay)
	if rb["code"] != nil || rb["pair_url"] != nil || rb["id"] != pc["id"] || strings.Contains(string(replay.Body), code) {
		t.Fatalf("replay: %s", replay.Body)
	}
	if r := human.Do(t, "POST", "/human/api/devices/pairing-codes", map[string]any{"name": ""}, "Idempotency-Key", "pair-2"); r.Status != 422 {
		t.Fatalf("empty name: %d", r.Status)
	}
	// pair the tablet for real
	if _, _, err := rig.Services.Devices.Redeem(context.Background(), code, "TabletUA/2"); err != nil {
		t.Fatal(err)
	}
	phone, _ := rig.DeviceWithCookie(t, "Phone")

	// list_devices: human only, with user agent and paired_by
	list := respObj(t, human.Do(t, "GET", "/human/api/devices", nil))
	items, _ := list["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %v", list)
	}
	byName := map[string]map[string]any{}
	for _, it := range items {
		m := it.(map[string]any)
		byName[m["name"].(string)] = m
		for _, k := range []string{"hmac_hash", "cookie", "hash"} {
			if _, ok := m[k]; ok {
				t.Fatalf("device row leaks %s", k)
			}
		}
	}
	tab := byName["Kitchen tablet"]
	if tab["user_agent"] != "TabletUA/2" || tab["paired_by"] != "human:alice" || tab["revoked_at"] != nil ||
		!slices.Equal(anyStrings(tab["scopes"]), []string{"read", "write"}) {
		t.Fatalf("tablet = %v", tab)
	}
	agent, _ := rig.Agent(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin)
	if r := agent.Do(t, "GET", "/human/api/devices", nil); r.Status != 403 || problemC(t, r) != "HUMAN_ONLY" {
		t.Fatalf("agent list_devices: %d %s", r.Status, r.Body)
	}
	if r := phone.Do(t, "GET", "/human/api/devices", nil); r.Status != 401 {
		t.Fatalf("device list_devices: %d", r.Status)
	}
	if r := phone.Do(t, "POST", "/human/api/devices/pairing-codes", map[string]any{"name": "x"}, "Idempotency-Key", "k"); r.Status != 401 {
		t.Fatalf("device create_pairing_code: %d", r.Status)
	}

	// revoke_device: the cookie is rejected next request
	phoneID := byName["Phone"]["id"].(string)
	if r := phone.Do(t, "GET", "/api/me", nil); r.Status != 200 {
		t.Fatalf("device before revoke: %d %s", r.Status, r.Body)
	}
	rv := human.Do(t, "POST", "/human/api/devices/"+phoneID+"/revoke", nil)
	if rv.Status != 200 || respObj(t, rv)["revoked_at"] == nil {
		t.Fatalf("revoke_device: %d %s", rv.Status, rv.Body)
	}
	if r := human.Do(t, "POST", "/human/api/devices/"+phoneID+"/revoke", nil); r.Status != 200 || respObj(t, r)["revoked_at"] != respObj(t, rv)["revoked_at"] {
		t.Fatalf("second revoke: %d", r.Status)
	}
	html := phone.Do(t, "GET", "/", nil)
	if html.Status != 303 || html.Header.Get("Location") != "/start?expired=1" || !strings.Contains(strings.Join(html.Header.Values("Set-Cookie"), ";"), "mm_dev=;") {
		t.Fatalf("revoked device HTML: %d %v", html.Status, html.Header)
	}
	if r := phone.Do(t, "GET", "/api/me", nil); r.Status != 401 {
		t.Fatalf("revoked device API: %d", r.Status)
	}
	if r := human.Do(t, "POST", "/human/api/devices/nope/revoke", nil); r.Status != 404 {
		t.Fatalf("unknown device: %d", r.Status)
	}

	// revoke_own_device: a device only; cookie cleared
	own := rig.Device(t)
	r := own.Do(t, "POST", "/api/devices/self/revoke", nil)
	ob := respObj(t, r)
	if r.Status != 200 || ob["revoked_at"] == nil || ob["name"] != "Test phone" || len(ob) != 3 {
		t.Fatalf("revoke_own_device: %d %s", r.Status, r.Body)
	}
	if sc := strings.Join(r.Header.Values("Set-Cookie"), ";"); !strings.Contains(sc, "mm_dev=;") || !strings.Contains(sc, "Max-Age=0") {
		t.Fatalf("cookie not cleared: %q", sc)
	}
	if r := own.Do(t, "GET", "/api/me", nil); r.Status != 401 {
		t.Fatalf("self-revoked device: %d", r.Status)
	}
	if r := human.Do(t, "POST", "/api/devices/self/revoke", nil); r.Status != 403 || problemC(t, r) != "NOT_A_DEVICE" {
		t.Fatalf("human: %d %s", r.Status, r.Body)
	}
	if r := agent.Do(t, "POST", "/api/devices/self/revoke", nil); r.Status != 403 || problemC(t, r) != "NOT_A_DEVICE" {
		t.Fatalf("agent: %d %s", r.Status, r.Body)
	}
}

func anyStrings(v any) []string {
	var out []string
	for _, e := range v.([]any) {
		out = append(out, e.(string))
	}
	return out
}

func TestDevice_ScopesReadWriteOnly(t *testing.T) {
	t.Parallel()
	rig := servertest.New(t, servertest.Options{})
	dev, cookie := rig.DeviceWithCookie(t, "Phone")
	me := respObj(t, dev.Do(t, "GET", "/api/me", nil))
	if me["kind"] != "device" || me["via"] != "device" || !slices.Equal(anyStrings(me["scopes"]), []string{"read", "write"}) {
		t.Fatalf("whoami = %v", me)
	}
	// a generate-scope operation is refused for a device principal
	p, err := rig.Services.Devices.VerifyDevice(context.Background(), cookie)
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(context.Background(), p)
	for _, sc := range []auth.Scope{auth.ScopeGenerate, auth.ScopeAdmin} {
		err := api.Authorize(ctx, api.Meta{Scope: sc})
		var pr *api.Problem
		if !asProblem(err, &pr) || pr.Status != 403 || pr.Code != "FORBIDDEN_SCOPE" {
			t.Errorf("scope %s: %v", sc, err)
		}
	}
	if err := api.Authorize(ctx, api.Meta{Scope: auth.ScopeWrite}); err != nil {
		t.Errorf("write: %v", err)
	}
}

func asProblem(err error, target **api.Problem) bool {
	return errors.As(err, target)
}

var humanOnlyOps = []string{"create_token", "revoke_token", "list_devices", "create_pairing_code", "revoke_device", "publish_record", "set_curation_settings"}

func TestHumanOnly_RefusedForAgentsDevicesStdio(t *testing.T) {
	t.Parallel()
	rig := servertest.New(t, servertest.Options{})
	agent, _ := rig.Agent(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin)
	dev := rig.Device(t)

	if r := agent.Do(t, "POST", "/human/api/tokens", map[string]any{"name": "x", "scopes": []string{"read"}}, "Idempotency-Key", "k"); r.Status != 403 || problemC(t, r) != "HUMAN_ONLY" {
		t.Fatalf("bearer on /human/api/tokens: %d %s", r.Status, r.Body)
	}
	if r := dev.Do(t, "POST", "/human/api/tokens", map[string]any{"name": "x", "scopes": []string{"read"}}, "Idempotency-Key", "k"); r.Status != 401 {
		t.Fatalf("device cookie on /human/api/tokens: %d", r.Status)
	}

	sessions := map[string]*mcp.ClientSession{"mcp": rig.MCP(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin), "stdio": rig.Stdio(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin)}
	for face, sess := range sessions {
		tools, err := sess.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tl := range tools.Tools {
			if slices.Contains(humanOnlyOps, tl.Name) || tl.Name == "revoke_own_device" {
				t.Errorf("%s tools/list exposes %s", face, tl.Name)
			}
		}
		for _, op := range append(slices.Clone(humanOnlyOps), "revoke_own_device") {
			args := map[string]any{"id": "x", "name": "x", "scopes": []string{"read"}}
			res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "call_operation", Arguments: map[string]any{"operation_id": op, "arguments": args}})
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError {
				b, _ := json.Marshal(res)
				t.Errorf("%s call_operation %s not refused: %s", face, op, b)
			}
		}
	}
}

func TestHumanOnly_NotFoundOffHumanPrefix(t *testing.T) {
	t.Parallel()
	rig := servertest.New(t, servertest.Options{})
	human := rig.Human(t)
	for _, path := range []string{"/api/tokens", "/api/devices", "/api/devices/pairing-codes"} {
		r := human.Do(t, "POST", path, map[string]any{"name": "x", "scopes": []string{"read"}}, "Idempotency-Key", "k")
		if r.Status != 404 && r.Status != 405 {
			t.Errorf("POST %s: %d %s", path, r.Status, r.Body)
		}
		if c := problemC(t, r); c == "" {
			t.Errorf("POST %s: not a problem body: %s", path, r.Body)
		}
	}
}
