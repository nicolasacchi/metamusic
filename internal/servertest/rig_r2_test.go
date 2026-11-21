package servertest_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolasacchi/metamusic/internal/servertest"
)

// TestRig_ObjStore: Options.ObjStore starts a TLS bucket on the rig clock, writes a mode-0600 r2.env for it and the app builds the
// upload fallback from that file exactly as production does: get_provider_status says configured.
func TestRig_ObjStore(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, Backend: "fake", ObjStore: true})
	if r.ObjStore == nil {
		t.Fatal("no bucket")
	}
	fi, err := os.Stat(filepath.Join(filepath.Dir(r.DataDir), "secrets", "r2.env"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("r2.env: %v %v", fi, err)
	}
	resp := r.Human(t).Do(t, "GET", "/api/provider", nil)
	if resp.Status != 200 {
		t.Fatalf("status %d: %s", resp.Status, resp.Body)
	}
	var body struct {
		UploadFallback struct {
			Configured     bool `json:"configured"`
			ObjectsPending int  `json:"objects_pending"`
		} `json:"upload_fallback"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatal(err)
	}
	if !body.UploadFallback.Configured || body.UploadFallback.ObjectsPending != 0 {
		t.Errorf("upload_fallback = %+v", body.UploadFallback)
	}
}

// TestRig_NoObjStoreIsM3: without the option nothing of the fallback is built.
func TestRig_NoObjStoreIsM3(t *testing.T) {
	t.Parallel()
	r := servertest.New(t, servertest.Options{Fixture: true, M2c: true, M3: true, Backend: "fake"})
	if r.ObjStore != nil {
		t.Fatal("a bucket without the option")
	}
	resp := r.Human(t).Do(t, "GET", "/api/provider", nil)
	var body struct {
		UploadFallback struct {
			Configured bool `json:"configured"`
		} `json:"upload_fallback"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil || body.UploadFallback.Configured {
		t.Errorf("configured without r2.env: %v %s", err, resp.Body)
	}
}
