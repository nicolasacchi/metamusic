package acceptance_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/archivetest"
	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/servertest"
	"github.com/nicolasacchi/metamusic/internal/spend"
)

// TestNoSecretLeak_E2 extends TestNoSecretLeak_M3 to the second engine on the serverless backend (both endpoints against one
// runpodtest server): the two endpoint ids, the RunPod keys, the upload signatures, the SIGNED INPUT LINKS minted for a reference
// render and the provider references appear in no response (REST, MCP, stdio), no log line, no job event, no audit row, no table
// that must not hold them, no ntfy message and no backup. Provider references live only in job_attempts.
func TestNoSecretLeak_E2(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy acceptance test")
	}
	logs := captureLogs(t)
	caps := spend.DefaultCaps
	caps.AutoApproveTakes = 0 // every agent batch needs the operator
	p := aceRunPodRig(t, archivetest.SyntheticArchive(t, "acceptance"), servertest.Options{Canary: true, Caps: &caps})
	r := p.Rig
	allOn(t, r)
	l := newLeaks(t)
	l.add("secret_key", keyForms(canaryRigKey)...)
	l.add("edge_secret", canaryEdge)
	l.add("ntfy_token", canaryNtfy)
	l.add("operator_password", canaryPassword)
	l.add("runpod-jobs-key", p.keys.Jobs)
	l.add("runpod-read-key", p.keys.Read)
	l.add("runpod-write-key", p.keys.Write)
	l.add("runpod-admin-key", "rp-admin-"+strings.Repeat("d", 8))
	l.add("yue2-endpoint-id", "ep-canary-0000")
	l.add("acestep-endpoint-id", aceCanaryEndpoint)

	human := r.Human(t)
	agentTok, _, err := r.Services.Tokens.Create(context.Background(), "e2-leak", []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate})
	if err != nil {
		t.Fatal(err)
	}
	l.add("pat", agentTok)
	agent := servertest.Bearer(r, agentTok)
	mcpSess := mcpWithToken(t, r, agentTok)
	stdio := r.Stdio(t, auth.ScopeRead, auth.ScopeWrite, auth.ScopeGenerate)
	mother := knownMother(t, r)
	var tokens []string

	run := func(who *servertest.Client, path, key string, body map[string]any) string {
		est := obj(who.OK(t, "POST", "/api/generations/estimate", body))
		tokens = append(tokens, est["confirmation_token"].(string))
		resp := who.Do(t, "POST", path, map[string]any{"confirmation_token": est["confirmation_token"], "rationale": "leak"}, "Idempotency-Key", key)
		if resp.Status != 201 {
			t.Fatalf("create %s: %d %s", key, resp.Status, resp.Body)
		}
		b := obj(resp.JSON(t))
		if b["state"] == "awaiting_confirmation" {
			who.OK(t, "POST", "/api/batches/"+b["id"].(string)+"/approval-request", nil) // the push to ntfy
			human.OK(t, "POST", "/human/api/batches/"+b["id"].(string)+"/approve", map[string]any{"reason": "ok"})
		}
		return obj(arr(b["jobs"])[0])["id"].(string)
	}
	// 1. the operator ports a yue2 take to ACE-Step; 2. an agent renders with a reference (the worker is given a signed input link);
	// 3. one yue2 job on the other endpoint
	jobs := []string{
		run(human, "/human/api/generations", "e2leak-port", acePortBody(mother)),
		run(agent, "/api/generations", "e2leak-ref", editBody("reference", mother, nil, map[string]any{"track_id": mother, "from_s": 0.5, "to_s": 5.5})),
	}
	_, y := startVariant(t, r, mother, "", "e2leak-yue")
	jobs = append(jobs, y)
	pump(t, r, func() bool {
		for _, id := range jobs {
			if s, _ := jobState(t, r, id); !terminalJob(s) {
				return false
			}
		}
		return true
	})
	for _, id := range jobs {
		if s, code := jobState(t, r, id); s != "completed" {
			t.Fatalf("job %s: %s (%s)", id, s, code)
		}
	}

	// the reads, over REST, MCP and stdio
	var results []recorded
	for face, sess := range map[string]*mcp.ClientSession{"mcp": mcpSess, "stdio": stdio} {
		for _, c := range []struct {
			op   string
			args map[string]any
		}{
			{"list_engines", nil}, {"get_provider_status", nil}, {"list_jobs", map[string]any{"limit": 20}}, {"get_job", map[string]any{"id": jobs[1]}},
			{"get_budget", nil}, {"list_ledger", map[string]any{"limit": 50}}, {"get_system_info", nil},
		} {
			res := rawTool(t, sess, c.op, c.args, true)
			results = append(results, recorded{leakClient{name: face}, servertest.Response{Method: "CALL", Path: c.op, Status: 200, Body: []byte(resultText(res))}})
		}
	}
	for _, c := range []*servertest.Client{human, agent} {
		for _, path := range []string{"/api/engines", "/api/provider", "/api/provider/status", "/api/jobs?limit=50", "/api/jobs/" + jobs[0], "/api/jobs/" + jobs[1], "/api/batches?limit=50",
			"/api/ledger?limit=100", "/api/system", "/api/tracks?engine=acestep15", "/api/openapi.json"} {
			c.Do(t, "GET", path, nil)
		}
	}
	for _, id := range jobs[:2] {
		if body := string(agent.Do(t, "GET", "/api/jobs/"+id, nil).Body); strings.Contains(body, "/media/") || strings.Contains(body, "sig=") {
			t.Errorf("get_job %s holds a signed link", id)
		}
	}

	// the provider's side: every signature (upload and media) and reference learned from what it received and issued
	sigRe := regexp.MustCompile(`sig=([A-Za-z0-9_-]{16,})`)
	inputLinks := 0
	for _, rq := range p.rp.Requests() {
		for _, m := range sigRe.FindAllStringSubmatch(string(rq.Body), -1) {
			l.add("signature", m[1])
		}
		if strings.Contains(string(rq.Body), "/media/") {
			inputLinks++
		}
	}
	if inputLinks == 0 {
		t.Fatal("the provider received no signed input link: the reference render did not exercise the minter")
	}
	rows, err := r.Store.R.Query(`SELECT ref, announced_ref FROM job_attempts`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var ref, ann sql.NullString
		if err := rows.Scan(&ref, &ann); err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{ref.String, ann.String} {
			if len(v) >= 8 {
				l.add("provider-ref", v)
			}
		}
	}
	_ = rows.Close()

	for _, tok := range tokens {
		l.add("confirmation-token", tok)
	}
	for _, lc := range []leakClient{{name: "human", c: human}, {name: "agent", c: agent}} {
		for _, resp := range lc.c.Responses() {
			var allowed []string
			if strings.HasSuffix(strings.SplitN(resp.Path, "?", 2)[0], "/estimate") {
				var m map[string]any
				_ = json.Unmarshal(resp.Body, &m)
				if tok, _ := m["confirmation_token"].(string); tok != "" {
					allowed = append(allowed, tok)
				}
			}
			l.scan(fmt.Sprintf("response of %s %s %s (status %d)", lc.name, resp.Method, strings.SplitN(resp.Path, "?", 2)[0], resp.Status), responseText(resp, ""), allowed...)
		}
	}
	for _, rc := range results {
		l.scan(fmt.Sprintf("%s result of %s", rc.cli.name, rc.resp.Path), string(rc.resp.Body))
	}

	// stored state: every table that could carry them
	waitBackupsIdle(t, agent)
	tables := dumpTables(t, r.Store.R, "audit_log", "idempotency", "job_events", "ledger", "provider_trips", "estimates", "batches", "jobs", "approvals",
		"provider_state", "worker_meter", "tracks", "recipes", "mutations", "env_fingerprints", "billing_windows", "billing_buckets", "artifacts", "engine_adoption_log", "engine_rtf")
	for name, text := range tables {
		l.scan("table "+name, text)
	}
	noRefs := &leaks{t: t, seen: map[string]bool{}}
	for _, s := range l.secrets {
		if s.kind != "provider-ref" {
			noRefs.add(s.kind, s.value)
		}
	}
	noRefs.check("table job_attempts", dumpTables(t, r.Store.R, "job_attempts")["job_attempts"])
	// the take artifacts on disk (request.json, result.json, mm-result.json) carry no link either
	scanDir(t, l, "rig takes", r.DataDir+"/takes")
	l.scan("captured logs", logs.String())
	for i, nr := range r.Ntfy.Requests() {
		l.scan(fmt.Sprintf("ntfy request %d body", i), nr.Body)
		l.scan(fmt.Sprintf("ntfy request %d path", i), nr.Path)
	}
	scanDir(t, l, "rig backups", r.BackupDir)
	l.flush()
	if l.scanned < 40 {
		t.Errorf("scanned only %d texts", l.scanned)
	}
	if len(r.Ntfy.Requests()) == 0 {
		t.Error("no ntfy request was captured: the approval push was not exercised")
	}
}
