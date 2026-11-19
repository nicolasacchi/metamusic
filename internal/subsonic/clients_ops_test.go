package subsonic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/subsonic"
	"github.com/nicolasacchi/metamusic/internal/subsonic/subsonictest"
)

func waitPushes(t *testing.T, r *servertest.Rig, n int) []servertest.NtfyRequest {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := r.Ntfy.Requests()
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// dumpTables returns every cell of the tables that could hold a one-time secret.
func dumpTables(t *testing.T, r *servertest.Rig, tables ...string) string {
	t.Helper()
	var b strings.Builder
	for _, tbl := range tables {
		rows, err := r.Store.R.Query(`SELECT * FROM ` + tbl)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			cells := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range cells {
				ptrs[i] = &cells[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%s: %v\n", tbl, cells)
		}
		_ = rows.Close()
	}
	return b.String()
}

func TestSubsonicClients_Ops(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Canary: true})
	human := r.Human(t)

	// create: 201 with both secrets, once
	body := map[string]any{"name": "Phone", "username": "phone.one"}
	resp := human.Do(t, "POST", "/human/api/subsonic-clients", body, "Idempotency-Key", "k-create-1")
	if resp.Status != 201 {
		t.Fatalf("create: %d %s", resp.Status, resp.Body)
	}
	var created struct {
		Client    map[string]any `json:"client"`
		APIKey    *string        `json:"api_key"`
		Password  *string        `json:"password"`
		ServerURL string         `json:"server_url"`
		Username  string         `json:"username"`
	}
	if err := json.Unmarshal(resp.Body, &created); err != nil || created.APIKey == nil || created.Password == nil ||
		!strings.HasPrefix(*created.APIKey, "mms_") || created.Username != "phone.one" || created.ServerURL != "http://mm.example.com" {
		t.Fatalf("created: %+v (%v)", created, err)
	}
	id := created.Client["id"].(string)
	if created.Client["has_api_key"] != true || created.Client["has_password"] != true || fmt.Sprint(created.Client["scopes"]) != "[read write]" {
		t.Errorf("client view: %v", created.Client)
	}
	apiKey, password := *created.APIKey, *created.Password

	// replay with the same key: the same client, both secrets null
	replay := human.Do(t, "POST", "/human/api/subsonic-clients", body, "Idempotency-Key", "k-create-1")
	var rep struct {
		Client   map[string]any `json:"client"`
		APIKey   *string        `json:"api_key"`
		Password *string        `json:"password"`
	}
	if err := json.Unmarshal(replay.Body, &rep); err != nil || replay.Status != 201 || rep.APIKey != nil || rep.Password != nil || rep.Client["id"] != id {
		t.Fatalf("replay: %d %s", replay.Status, replay.Body)
	}

	// the new credential works on /rest/
	cl := &subsonictest.Client{BaseURL: r.URL, Username: "phone.one", Password: password, APIKey: apiKey}
	if got := cl.Call(t, "ping", nil, subsonictest.CallOpts{JSON: true}); got.Status != "ok" {
		t.Fatalf("token ping: %+v", got)
	}
	if got := cl.Call(t, "ping", nil, subsonictest.CallOpts{JSON: true, APIKey: true}); got.Status != "ok" {
		t.Fatalf("key ping: %+v", got)
	}

	// list never contains the secrets
	list := human.Do(t, "GET", "/human/api/subsonic-clients", nil)
	if list.Status != 200 || strings.Contains(string(list.Body), apiKey) || strings.Contains(string(list.Body), password) ||
		strings.Contains(string(list.Body), "api_key\":\"mms_") {
		t.Fatalf("list: %d %s", list.Status, list.Body)
	}
	var lv struct {
		ServerURL  string           `json:"server_url"`
		APIVersion string           `json:"api_version"`
		Items      []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(list.Body, &lv); err != nil || len(lv.Items) != 1 || lv.APIVersion != subsonic.APIVersion || lv.ServerURL != "http://mm.example.com" {
		t.Fatalf("list view: %+v (%v)", lv, err)
	}
	if lv.Items[0]["last_client"] == nil || lv.Items[0]["last_seen_at"] == nil {
		t.Errorf("last use not recorded after the pings: %v", lv.Items[0])
	}
	if p, _ := lv.Items[0]["api_key_prefix"].(string); p != "" && strings.Contains(apiKey, p) == false {
		t.Errorf("prefix %q is not part of the key", p)
	}

	// duplicate username: 409 USERNAME_TAKEN
	dup := human.Do(t, "POST", "/human/api/subsonic-clients", body, "Idempotency-Key", "k-create-2")
	if dup.Status != 409 || !strings.Contains(string(dup.Body), "USERNAME_TAKEN") {
		t.Errorf("dup: %d %s", dup.Status, dup.Body)
	}
	// both methods false: 422
	none := human.Do(t, "POST", "/human/api/subsonic-clients", map[string]any{"name": "x", "api_key": false, "password": false}, "Idempotency-Key", "k-create-3")
	if none.Status != 422 {
		t.Errorf("no method: %d %s", none.Status, none.Body)
	}
	// an explicit false survives: read-only, key only
	ro := human.Do(t, "POST", "/human/api/subsonic-clients", map[string]any{"name": "Reader", "write": false, "password": false}, "Idempotency-Key", "k-create-4")
	var rov struct {
		Client   map[string]any `json:"client"`
		Password *string        `json:"password"`
		APIKey   *string        `json:"api_key"`
	}
	if err := json.Unmarshal(ro.Body, &rov); err != nil || ro.Status != 201 || rov.Password != nil || rov.APIKey == nil || fmt.Sprint(rov.Client["scopes"]) != "[read]" {
		t.Errorf("read-only: %d %s", ro.Status, ro.Body)
	}

	// revoke: idempotent; the credential stops working
	for i := 0; i < 2; i++ {
		rv := human.Do(t, "POST", "/human/api/subsonic-clients/"+id+"/revoke", nil)
		if rv.Status != 200 || !strings.Contains(string(rv.Body), `"revoked_at":"`) {
			t.Fatalf("revoke %d: %d %s", i, rv.Status, rv.Body)
		}
	}
	if got := cl.Call(t, "ping", nil, subsonictest.CallOpts{JSON: true, APIKey: true}); got.Code != 44 {
		t.Errorf("revoked key: %+v", got)
	}
	if rv := human.Do(t, "POST", "/human/api/subsonic-clients/nope/revoke", nil); rv.Status != 404 {
		t.Errorf("unknown id: %d", rv.Status)
	}

	// a read-scope agent is refused
	reader, _ := r.Agent(t, auth.ScopeRead)
	for _, c := range [][3]string{{"GET", "/human/api/subsonic-clients", ""}, {"POST", "/human/api/subsonic-clients", `{"name":"x"}`}, {"POST", "/human/api/subsonic-clients/" + id + "/revoke", ""}} {
		var b any
		if c[2] != "" {
			b = json.RawMessage(c[2])
		}
		resp := reader.Do(t, c[0], c[1], b, "Idempotency-Key", "k-read")
		if resp.Status != 403 || !strings.Contains(string(resp.Body), "HUMAN_ONLY") {
			t.Errorf("read agent %s %s: %d %s", c[0], c[1], resp.Status, resp.Body)
		}
	}
}

// TestSubsonicClients_AgentRefusedHumanCreates: since U1-D1 the client ops are human-only self-service (D-U1-14): even an
// admin-scope agent is refused (HUMAN_ONLY), the human's creation is audited and pushed once, the secrets stay out of every
// stored row and push, a list sends no push and a repeated revoke sends none.
func TestSubsonicClients_AgentRefusedHumanCreates(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Canary: true})
	agent, _ := r.Agent(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin)
	for _, c := range [][2]string{{"POST", "/human/api/subsonic-clients"}, {"GET", "/human/api/subsonic-clients"}, {"POST", "/human/api/subsonic-clients/x/revoke"}} {
		if resp := agent.Do(t, c[0], c[1], map[string]any{"name": "Agent Made"}, "Idempotency-Key", "agent-k-1"); resp.Status != 403 || !strings.Contains(string(resp.Body), "HUMAN_ONLY") {
			t.Errorf("admin agent %s %s: %d %s", c[0], c[1], resp.Status, resp.Body)
		}
	}
	before := len(r.Ntfy.Requests())
	human := r.Human(t)
	resp := human.Do(t, "POST", "/human/api/subsonic-clients", map[string]any{"name": "Desk Made"}, "Idempotency-Key", "human-k-1")
	if resp.Status != 201 {
		t.Fatalf("create: %d %s", resp.Status, resp.Body)
	}
	var created struct {
		Client   map[string]any `json:"client"`
		APIKey   *string        `json:"api_key"`
		Password *string        `json:"password"`
	}
	if err := json.Unmarshal(resp.Body, &created); err != nil || created.APIKey == nil || created.Password == nil {
		t.Fatalf("created: %s", resp.Body)
	}
	apiKey, password := *created.APIKey, *created.Password
	if created.Client["owner"] != "owner" {
		t.Errorf("the Owner's own create shows owner = %v", created.Client["owner"])
	}

	pushes := waitPushes(t, r, before+1)
	if len(pushes) != before+1 {
		t.Fatalf("pushes = %d, want %d", len(pushes), before+1)
	}
	push := pushes[len(pushes)-1].Body
	if !strings.Contains(push, "Subsonic client created") || !strings.Contains(push, "created Desk Made") {
		t.Errorf("push: %s", push)
	}
	tables := dumpTables(t, r, "audit_log", "idempotency")
	for _, secret := range []string{apiKey, password} {
		if strings.Contains(tables, secret) || strings.Contains(push, secret) {
			t.Errorf("a stored row or push holds a one-time secret")
		}
	}
	page, err := r.Services.Audit.List(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range page.Items {
		if row.Op == "create_subsonic_client" && row.Status == 201 {
			found = true
			if row.PrincipalKind != "human" || len(row.CreatedIDs) != 1 || row.CreatedIDs[0] != created.Client["id"] {
				t.Errorf("audit row: %+v", row)
			}
		}
	}
	if !found {
		t.Error("no audit row for create_subsonic_client")
	}
	lst := human.Do(t, "GET", "/human/api/subsonic-clients", nil)
	if lst.Status != 200 || strings.Contains(string(lst.Body), apiKey) || strings.Contains(string(lst.Body), password) {
		t.Fatalf("list: %d %s", lst.Status, lst.Body)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(r.Ntfy.Requests()); n != before+1 {
		t.Errorf("list pushed: %d requests", n)
	}
	id := created.Client["id"].(string)
	human.OK(t, "POST", "/human/api/subsonic-clients/"+id+"/revoke", nil)
	human.OK(t, "POST", "/human/api/subsonic-clients/"+id+"/revoke", nil)
	waitPushes(t, r, before+2)
	time.Sleep(100 * time.Millisecond)
	got := r.Ntfy.Requests()
	if len(got) != before+2 || !strings.Contains(got[len(got)-1].Body, "Subsonic client revoked") {
		t.Errorf("revoke pushes: %d %+v", len(got), got)
	}
}

// TestSubsonictest_Call: the kit signs like a client, parses both envelopes and honours explicit credentials.
func TestSubsonictest_Call(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{})
	c := r.Subsonic(t, subsonictest.ClientOpts{Name: "Kit", Username: "kit.client", Write: true, Format: "mp3"})
	if c.Username != "kit.client" || c.Password == "" || !strings.HasPrefix(c.APIKey, "mms_") {
		t.Fatalf("client: %+v", c)
	}
	for name, o := range map[string]subsonictest.CallOpts{
		"xml": {}, "json": {JSON: true}, "view": {View: true}, "post": {POST: true}, "key": {APIKey: true, JSON: true},
		"ua": {UserAgent: "Kit/1.0"},
	} {
		got := c.Call(t, "ping", nil, o)
		if got.HTTP != 200 || got.Status != "ok" || got.Doc == nil {
			t.Errorf("%s: %+v", name, got)
		}
	}
	// a fixed salt can be replayed within its window
	for i := 0; i < 2; i++ {
		if got := c.Call(t, "ping", nil, subsonictest.CallOpts{Salt: "fixed-salt-1"}); got.Status != "ok" {
			t.Errorf("fixed salt %d: %+v", i, got)
		}
	}
	// no credentials: error 10 in XML, parsed
	none := (&subsonictest.Client{BaseURL: r.URL}).Call(t, "ping", nil, subsonictest.CallOpts{})
	if none.Status != "failed" || none.Code != 10 {
		t.Errorf("anonymous: %+v", none)
	}
	// caller-supplied credentials win (u+p => 42)
	up := c.Call(t, "ping", url.Values{"u": {c.Username}, "p": {"x"}}, subsonictest.CallOpts{JSON: true})
	if up.Code != 42 {
		t.Errorf("u+p: %+v", up)
	}
}
