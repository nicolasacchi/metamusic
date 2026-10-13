package api_test

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/nicolasacchi/metamusic/internal/audit"
	"github.com/nicolasacchi/metamusic/internal/auth"
	lt "github.com/nicolasacchi/metamusic/internal/library/librarytest"
	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/servertest"
)

type seedRow struct {
	p      auth.Principal
	op     string
	status int
	at     time.Duration // relative to the fixed clock
	ids    int           // number of created ids
}

var (
	pAlice = auth.Principal{Kind: auth.KindHuman, ID: "alice", Name: "alice"}
	pAgent = auth.Principal{Kind: auth.KindAgent, ID: "t1", Name: "bot"}
	pDev   = auth.Principal{Kind: auth.KindDevice, ID: "d1", Name: "Phone"}
	pCLI   = auth.Principal{Kind: auth.KindSystem, ID: "cli", Name: "cli"}
)

// seedAudit appends rows in order (ids ascend) and returns their audit ids, oldest first.
func seedAudit(t *testing.T, r *servertest.Rig, rows []seedRow) []string {
	t.Helper()
	var ids []string
	for _, s := range rows {
		created := make([]string, s.ids)
		for i := range created {
			created[i] = fmt.Sprintf("01900000-0000-7000-8000-%012x", i)
		}
		e := audit.Entry{At: lt.FixedNow.Add(s.at), Principal: s.p, Transport: reqinfo.REST, Client: "seed", Op: s.op, Method: "POST",
			ArgsSHA256: strings.Repeat("a", 64), Status: s.status, Idempotency: audit.IdemNone, CreatedIDs: created, Duration: time.Millisecond}
		if s.status >= 400 {
			e.ErrorCode = "BOOM"
		}
		if err := r.Services.Audit.Insert(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	rowsOut, err := r.Store.R.Query(`SELECT id FROM audit_log WHERE client = 'seed' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rowsOut.Close() }()
	for rowsOut.Next() {
		var id string
		if err := rowsOut.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func auditIDs(t *testing.T, resp *servertest.Response) []string {
	t.Helper()
	if resp.Status != 200 {
		t.Fatalf("%s: %d %s", resp.Path, resp.Status, resp.Body)
	}
	var out []string
	for _, it := range obj(t, resp.JSON(t))["items"].([]any) {
		out = append(out, obj(t, it)["id"].(string))
	}
	return out
}

func TestGetAudit_FiltersAndCursor(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	rows := []seedRow{
		{pAlice, "rate_track", 200, -10 * time.Hour, 0},
		{pAgent, "add_comment", 200, -9 * time.Hour, 1},
		{pAgent, "add_comment", 422, -8 * time.Hour, 0},
		{pDev, "log_listen_events", 200, -7 * time.Hour, 0},
		{pCLI, "cli:restore", 200, -6 * time.Hour, 0},
		{pAlice, "log_listen_events", 200, -5 * time.Hour, 0},
		{pAgent, "log_listen_events", 500, -4 * time.Hour, 0},
		{pAlice, "update_track", 404, -3 * time.Hour, 0},
	}
	ids := seedAudit(t, r, rows)
	pick := func(idx ...int) []string { // newest first
		var out []string
		for _, i := range idx {
			out = append(out, ids[i])
		}
		slices.Reverse(out)
		return out
	}
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"all, newest first", "limit=200", pick(0, 1, 2, 3, 4, 5, 6, 7)},
		{"principal_kind", "principal_kind=agent", pick(1, 2, 6)},
		{"principal_kind twice", "principal_kind=device&principal_kind=system", pick(3, 4)},
		{"principal_id", "principal_id=" + url.QueryEscape(pAgent.Key()), pick(1, 2, 6)},
		{"human principal_id", "principal_id=" + url.QueryEscape(pAlice.Key()), pick(0, 5, 7)},
		{"op", "op=add_comment&op=rate_track", pick(0, 1, 2)},
		{"exclude_op", "exclude_op=log_listen_events", pick(0, 1, 2, 4, 7)},
		{"exclude_op twice", "exclude_op=log_listen_events&exclude_op=add_comment", pick(0, 4, 7)},
		{"status_min", "status_min=400", pick(2, 6, 7)},
		{"since", "since=" + url.QueryEscape(lt.FixedNow.Add(-7*time.Hour).UTC().Format(time.RFC3339)), pick(3, 4, 5, 6, 7)},
		{"until is exclusive", "until=" + url.QueryEscape(lt.FixedNow.Add(-7*time.Hour).UTC().Format(time.RFC3339)), pick(0, 1, 2)},
		{"combined", "principal_kind=agent&exclude_op=add_comment", pick(6)},
	}
	for _, tc := range tests {
		got := auditIDs(t, human.Do(t, "GET", "/api/audit?"+tc.query, nil))
		// the seeded rows are the only rows of a fresh rig (human reads are not audited)
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("%s: (-want +got)\n%s", tc.name, diff)
		}
	}

	// cursor walk: no duplicate, no miss
	var walked []string
	cursor := ""
	for range 10 {
		q := "limit=3"
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		resp := human.Do(t, "GET", "/api/audit?"+q, nil)
		walked = append(walked, auditIDs(t, resp)...)
		nc := obj(t, resp.JSON(t))["next_cursor"]
		if nc == nil {
			break
		}
		cursor = nc.(string)
	}
	if diff := cmp.Diff(pick(0, 1, 2, 3, 4, 5, 6, 7), walked); diff != "" {
		t.Errorf("cursor walk (-want +got)\n%s", diff)
	}

	// the entry shape
	one := obj(t, obj(t, human.Do(t, "GET", "/api/audit?op=add_comment&status_min=400", nil).JSON(t))["items"].([]any)[0])
	if one["error_code"] != "BOOM" || one["transport"] != "rest" || one["client"] != "seed" || one["status"] != 422.0 || one["created_ids"] == nil {
		t.Errorf("entry %v", one)
	}
	if p := obj(t, one["principal"]); p["kind"] != "agent" || p["id"] != "agent:t1" || p["name"] != "bot" {
		t.Errorf("principal %v", p)
	}
	ok := obj(t, obj(t, human.Do(t, "GET", "/api/audit?op=rate_track", nil).JSON(t))["items"].([]any)[0])
	if ok["error_code"] != nil {
		t.Errorf("error_code of a success: %v", ok["error_code"])
	}
	// bad inputs
	for _, q := range []string{"since=yesterday", "until=2026-13-45", "limit=0", "status_min=99", "principal_kind=robot"} {
		if resp := human.Do(t, "GET", "/api/audit?"+q, nil); resp.Status != 422 {
			t.Errorf("%s: %d", q, resp.Status)
		}
	}
	if resp := human.Do(t, "GET", "/api/audit?cursor=", nil); resp.Status != 200 {
		t.Errorf("empty cursor: %d", resp.Status)
	}
}

func TestGetAudit_PageByteCap(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	human := r.Human(t)
	var rows []seedRow
	for range 120 {
		rows = append(rows, seedRow{pAlice, "bulk", 200, -time.Hour, 100}) // ~4 KB of created ids each
	}
	ids := seedAudit(t, r, rows)
	resp := human.Do(t, "GET", "/api/audit?limit=200&op=bulk", nil)
	if len(resp.Body) > 90000 {
		t.Fatalf("page of %d bytes exceeds the 90000-byte cap", len(resp.Body))
	}
	body := obj(t, resp.JSON(t))
	items := body["items"].([]any)
	if len(items) == 0 || len(items) >= 120 || body["next_cursor"] == nil {
		t.Fatalf("%d items, cursor %v", len(items), body["next_cursor"])
	}
	// walking the cursors yields every row exactly once, newest first
	var walked []string
	cursor := ""
	for range 20 {
		q := "limit=200&op=bulk"
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		resp := human.Do(t, "GET", "/api/audit?"+q, nil)
		if len(resp.Body) > 90000 {
			t.Fatalf("page of %d bytes", len(resp.Body))
		}
		walked = append(walked, auditIDs(t, resp)...)
		nc := obj(t, resp.JSON(t))["next_cursor"]
		if nc == nil {
			break
		}
		cursor = nc.(string)
	}
	slices.Reverse(ids)
	if diff := cmp.Diff(ids, walked); diff != "" {
		t.Errorf("walk (-want +got)\n%s", diff)
	}
}

func TestGetAudit_AgentHumanKeyFilter422(t *testing.T) {
	t.Parallel()
	r := judgRig(t)
	seedAudit(t, r, []seedRow{
		{pAlice, "update_track", 200, -time.Hour, 0},
		{pDev, "log_listen_events", 200, -time.Hour, 0},
		{pAgent, "add_comment", 200, -time.Hour, 0},
	})
	agent, _ := r.Agent(t)
	for _, key := range []string{"human:alice", "human", "human:"} {
		resp := agent.Do(t, "GET", "/api/audit?principal_id="+url.QueryEscape(key), nil)
		if resp.Status != 422 || obj(t, resp.JSON(t))["code"] != "VALIDATION_FAILED" {
			t.Errorf("principal_id=%s: %d %s", key, resp.Status, resp.Body)
		}
	}
	// the human's own key is fine for the human
	if resp := r.Human(t).Do(t, "GET", "/api/audit?principal_id=human%3Aalice", nil); resp.Status != 200 {
		t.Errorf("human: %d", resp.Status)
	}
	// agents see the operator's rows through principal_kind, masked
	resp := agent.Do(t, "GET", "/api/audit?principal_kind=human", nil)
	items := obj(t, resp.JSON(t))["items"].([]any)
	if resp.Status != 200 || len(items) != 1 {
		t.Fatalf("%d %s", resp.Status, resp.Body)
	}
	if p := obj(t, obj(t, items[0])["principal"]); p["kind"] != "human" || p["id"] != "human" || p["name"] != "human" {
		t.Errorf("masked principal %v", p)
	}
	// a device keeps its key but not its name
	items = obj(t, agent.Do(t, "GET", "/api/audit?principal_kind=device", nil).JSON(t))["items"].([]any)
	if p := obj(t, obj(t, items[0])["principal"]); p["kind"] != "device" || p["id"] != "device:d1" || p["name"] != "device" {
		t.Errorf("device principal %v", p)
	}
}
