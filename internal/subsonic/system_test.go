package subsonic

import (
	"net/url"
	"strings"
	"testing"
)

func TestSubsonic_SystemEndpoints(t *testing.T) {
	t.Parallel()
	r := newRig(t, rigOpts{})
	ip := "198.51.100.100"

	d := r.get("getLicense", keyParams(r.rwKey), ip).doc(t)
	if lic, _ := d["license"].(map[string]any); lic["valid"] != true {
		t.Errorf("getLicense: %v", d)
	}

	// tokenInfo: API key and token authentication both answer the client's username
	for name, q := range map[string]url.Values{"key": keyParams(r.rwKey), "token": tokenParams("rwclient", r.rwPW, "tokeninfo-1")} {
		d := r.get("tokenInfo", q, ip).doc(t)
		if ti, _ := d["tokenInfo"].(map[string]any); ti["username"] != "rwclient" {
			t.Errorf("tokenInfo via %s: %v", name, d)
		}
	}

	// getUser: own username => roles by scope
	own := func(key, user string) map[string]any {
		return r.get("getUser", keyParams(key, "username", user), ip).doc(t)
	}
	u := own(r.rwKey, "rwclient")["user"].(map[string]any)
	for _, k := range []string{"streamRole", "downloadRole", "coverArtRole", "playlistRole", "commentRole", "scrobblingEnabled"} {
		if u[k] != true {
			t.Errorf("rw user %s = %v", k, u[k])
		}
	}
	for _, k := range []string{"adminRole", "settingsRole", "uploadRole", "shareRole", "podcastRole", "jukeboxRole", "videoConversionRole"} {
		if u[k] != false {
			t.Errorf("rw user %s = %v", k, u[k])
		}
	}
	if f, _ := u["folder"].([]any); len(f) != 3 || f[0].(float64) != 1 || f[2].(float64) != 3 {
		t.Errorf("folder = %v", u["folder"])
	}
	ro := own(r.roKey, "roclient")["user"].(map[string]any)
	if ro["playlistRole"] != false || ro["commentRole"] != false || ro["streamRole"] != true || ro["scrobblingEnabled"] != false {
		t.Errorf("read-only user: %v", ro)
	}
	// another user => 50; no username => 10
	if w := r.get("getUser", keyParams(r.rwKey, "username", "roclient"), ip); w.code(t) != 50 {
		t.Errorf("other user: %s", w.Body)
	}
	if w := r.get("getUser", keyParams(r.rwKey), ip); w.code(t) != 10 {
		t.Errorf("no username: %s", w.Body)
	}

	// getMusicFolders: the three folders
	mf := r.get("getMusicFolders", keyParams(r.rwKey), ip).doc(t)["musicFolders"].(map[string]any)["musicFolder"].([]any)
	if len(mf) != 3 {
		t.Fatalf("folders: %v", mf)
	}
	for i, want := range []string{"Library", "Drafts", "Discarded"} {
		m := mf[i].(map[string]any)
		if int(m["id"].(float64)) != i+1 || m["name"] != want {
			t.Errorf("folder %d: %v", i, m)
		}
	}
	// XML forms
	q := keyParams(r.rwKey)
	q.Del("f")
	x := r.get("getMusicFolders", q, ip).Body.String()
	if !strings.Contains(x, `<musicFolders><musicFolder id="1" name="Library">`) {
		t.Errorf("xml folders: %s", x)
	}
	x = r.get("getLicense", q, ip).Body.String()
	if !strings.Contains(x, `<license valid="true">`) {
		t.Errorf("xml license: %s", x)
	}
	q.Set("username", "rwclient")
	x = r.get("getUser", q, ip).Body.String()
	if !strings.Contains(x, `<user username="rwclient"`) || !strings.Contains(x, `<folder>1</folder><folder>2</folder><folder>3</folder>`) {
		t.Errorf("xml user: %s", x)
	}
}
