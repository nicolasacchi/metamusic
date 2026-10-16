package subsonic

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/audit"
)

func TestSubsonic_ArgsSHA256(t *testing.T) {
	t.Parallel()
	base := url.Values{"id": {"a", "b"}, "rating": {"3"}}
	with := url.Values{"id": {"a", "b"}, "rating": {"3"}, "u": {"x"}, "p": {"x"}, "t": {"x"}, "s": {"x"}, "apiKey": {"x"}, "c": {"x"}, "v": {"x"}, "f": {"json"}}
	if argsSHA256(base) != argsSHA256(with) {
		t.Error("credentials and framing changed the hash")
	}
	if argsSHA256(base) == argsSHA256(url.Values{"id": {"b", "a"}, "rating": {"3"}}) {
		t.Error("value order is part of the hash")
	}
	if argsSHA256(base) == argsSHA256(url.Values{"id": {"a", "b"}, "rating": {"4"}}) {
		t.Error("values are part of the hash")
	}
}

// TestSubsonic_WriteAuditRow: a write endpoint inserts one audit row after the call: the device principal,
// transport subsonic, op subsonic:<endpoint>, the created ids and a SUBSONIC_<code> on failure; reads write none.
func TestSubsonic_WriteAuditRow(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	r.srv.add("starTest", true, func(c *call) error {
		audit.NoteCreated(c.ctx, "created-1", "created-2")
		if c.str("fail") != "" {
			return fail(errNotFound, "data not found")
		}
		return nil
	})
	r.srv.add("readTest", false, func(c *call) error { return nil })
	ip := "198.51.100.110"
	q := tokenParams("rwclient", r.rwPW, "audit-salt-1", "id", "x1")
	q.Set("c", "Amperfy/1.2 (iOS)")
	if w := r.get("starTest", q, ip); w.code(t) != 0 {
		t.Fatal(w.Body)
	}
	if w := r.get("starTest", tokenParams("rwclient", r.rwPW, "audit-salt-2", "fail", "1"), ip); w.code(t) != 70 {
		t.Fatal(w.Body)
	}
	if w := r.get("readTest", keyParams(r.rwKey), ip); w.code(t) != 0 {
		t.Fatal(w.Body)
	}
	// a rejected write (read-only client) writes no row
	if w := r.get("starTest", keyParams(r.roKey), ip); w.code(t) != 50 {
		t.Fatal(w.Body)
	}

	page, err := r.auditStore.List(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("audit rows = %d, want 2: %+v", len(page.Items), page.Items)
	}
	byCode := map[string]audit.Row{}
	for _, row := range page.Items {
		byCode[row.ErrorCode] = row
	}
	ok, bad := byCode[""], byCode["SUBSONIC_70"]
	if ok.Op != "subsonic:starTest" || ok.Transport != "subsonic" || ok.Method != "GET" || ok.Status != 200 ||
		ok.PrincipalKind != "device" || ok.PrincipalID != "device:"+r.rw.ID || ok.Client != "subsonic:Amperfy/1.2" ||
		len(ok.CreatedIDs) != 2 || ok.CreatedIDs[0] != "created-1" || len(ok.ArgsSHA256) != 64 {
		t.Errorf("ok row: %+v", ok)
	}
	if bad.Op != "subsonic:starTest" || bad.Status != 200 {
		t.Errorf("failed row: %+v", bad)
	}
	// no credential material in any row
	for _, row := range page.Items {
		blob := strings.Join([]string{row.ArgsSHA256, row.Client, row.Op, row.PrincipalName, row.PrincipalID}, " ")
		for _, secret := range []string{r.rwKey, r.rwPW, token(r.rwPW, "audit-salt-1"), "audit-salt-1"} {
			if strings.Contains(blob, secret) {
				t.Errorf("row leaks %q", secret)
			}
		}
	}
}

func TestSubsonic_InternalErrorIsFixedText(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	r.srv.add("boomTest", false, func(c *call) error {
		return errors.New("secret detail /srv/data/file.flac")
	})
	w := r.get("boomTest", keyParams(r.rwKey), "198.51.100.120")
	if w.code(t) != 0 || strings.Contains(w.Body.String(), "secret detail") || !strings.Contains(w.Body.String(), "internal error") {
		t.Errorf("body: %s", w.Body)
	}
	if strings.Contains(r.logs.String(), "mms_") {
		t.Error("logs hold a key")
	}
}

// TestSubsonic_AuditCreatedIDs: scrobble, reportPlayback and createPlaylist rows carry what they created.
func TestSubsonic_AuditCreatedIDs(t *testing.T) {
	t.Parallel()
	x := newS3Rig(t)
	eq(t, "scrobble", x.callCode("scrobble", "id", luna), 0)
	eq(t, "report", x.callCode("reportPlayback", "mediaId", citta, "state", "starting", "positionMs", "0"), 0)
	w := x.get("createPlaylist", x.rwq("name", "Audited", "songId", luna), libIP)
	eq(t, "create", w.code(t), 0)
	plID := dig(t, w.doc(t), "playlist", "id").(string)
	page, err := x.auditStore.List(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, row := range page.Items {
		got[row.Op] = row.CreatedIDs
	}
	eq(t, "scrobble ids", len(got["subsonic:scrobble"]), 2) // start + complete
	eq(t, "report ids", len(got["subsonic:reportPlayback"]), 1)
	eq(t, "playlist ids", strings.Join(got["subsonic:createPlaylist"], ","), plID)
	ev := map[string]bool{}
	for _, e := range append(x.events(luna), x.events(citta)...) {
		ev[e.ID] = true
	}
	for _, id := range append(got["subsonic:scrobble"], got["subsonic:reportPlayback"]...) {
		if !ev[id] {
			t.Errorf("audited id %s is no listen event", id)
		}
	}
}
