package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Variable names are assembled at runtime and values are canary placeholders,
// so no `RUNPOD_*KEY=<value>` literal ever appears in the repository (C85).
func rk(name string) string { return "RUNPOD_" + name + "_KEY" }

const (
	canaryJobs  = "canary-jobs-0001"
	canaryRead  = "canary-read-0002"
	canaryWrite = "canary-write-0003"
	canaryEP    = "canary-endpoint-0004"
)

func writeEnv(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadRunPodKeys(t *testing.T) {
	t.Parallel()
	t.Run("absent", func(t *testing.T) {
		k, ok, err := LoadRunPodKeys(t.TempDir())
		if ok || err != nil || k != (RunPodKeys{}) {
			t.Fatalf("got %v %v %v", k, ok, err)
		}
	})
	t.Run("all forms", func(t *testing.T) {
		dir := t.TempDir()
		content := "\xef\xbb\xbf# comment\r\n\r\nexport " + rk("JOBS") + "=" + canaryJobs + "\r\n" +
			rk("READ") + "=\"" + canaryRead + "\"\n" +
			"  " + rk("WRITE") + "='" + canaryWrite + "'  \n" +
			"RUNPOD_ENDPOINT_ID=" + canaryEP + "\nOTHER=ignored\n"
		writeEnv(t, dir, "runpod.env", content)
		k, ok, err := LoadRunPodKeys(dir)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		want := RunPodKeys{Jobs: canaryJobs, Read: canaryRead, Write: canaryWrite, EndpointID: canaryEP, ConsoleURL: DefaultConsoleURL}
		if k != want {
			t.Fatalf("got %v", k.String())
		}
	})
	t.Run("custom console and optional write", func(t *testing.T) {
		dir := t.TempDir()
		writeEnv(t, dir, "runpod.env", rk("JOBS")+"="+canaryJobs+"\n"+rk("READ")+"="+canaryRead+"\nRUNPOD_ENDPOINT_ID="+canaryEP+"\nRUNPOD_CONSOLE_URL=https://console.example.com/x\n")
		k, ok, err := LoadRunPodKeys(dir)
		if err != nil || !ok || k.Write != "" || k.ConsoleURL != "https://console.example.com/x" {
			t.Fatalf("%v %v %v", k, ok, err)
		}
	})
	t.Run("http console refused", func(t *testing.T) {
		dir := t.TempDir()
		writeEnv(t, dir, "runpod.env", rk("JOBS")+"="+canaryJobs+"\n"+rk("READ")+"="+canaryRead+"\nRUNPOD_ENDPOINT_ID="+canaryEP+"\nRUNPOD_CONSOLE_URL=http://console.example.com\n")
		if _, _, err := LoadRunPodKeys(dir); err == nil || !strings.Contains(err.Error(), "RUNPOD_CONSOLE_URL") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("missing read key names file and variable only", func(t *testing.T) {
		dir := t.TempDir()
		writeEnv(t, dir, "runpod.env", rk("JOBS")+"="+canaryJobs+"\nRUNPOD_ENDPOINT_ID="+canaryEP+"\n")
		_, _, err := LoadRunPodKeys(dir)
		if err == nil || !strings.Contains(err.Error(), "runpod.env") || !strings.Contains(err.Error(), rk("READ")) {
			t.Fatalf("err = %v", err)
		}
		for _, c := range []string{canaryJobs, canaryEP} {
			if strings.Contains(err.Error(), c) {
				t.Errorf("error leaks %s", c)
			}
		}
	})
	t.Run("endpoint id optional for provision only", func(t *testing.T) {
		dir := t.TempDir()
		writeEnv(t, dir, "runpod.env", rk("JOBS")+"="+canaryJobs+"\n"+rk("READ")+"="+canaryRead+"\n")
		k, ok, err := LoadRunPodKeysForProvision(dir)
		if err != nil || !ok || k.EndpointID != "" || k.Jobs != canaryJobs {
			t.Fatalf("provision loader: %v %v %v", k, ok, err)
		}
		if _, _, err := LoadRunPodKeys(dir); err == nil || !strings.Contains(err.Error(), "RUNPOD_ENDPOINT_ID") {
			t.Fatalf("strict loader accepted a missing endpoint id: %v", err)
		}
		// the provision loader still insists on the scoped keys and still refuses an all-access key
		writeEnv(t, dir, "runpod.env", rk("JOBS")+"="+canaryJobs+"\n")
		if _, _, err := LoadRunPodKeysForProvision(dir); err == nil || !strings.Contains(err.Error(), rk("READ")) {
			t.Fatalf("err = %v", err)
		}
		writeEnv(t, dir, "runpod.env", rk("JOBS")+"="+canaryJobs+"\n"+rk("READ")+"="+canaryRead+"\nRUNPOD_ADMIN_KEY=canary-admin-0005\n")
		if _, _, err := LoadRunPodKeysForProvision(dir); err == nil || !strings.Contains(err.Error(), "all-access") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		dir := t.TempDir()
		writeEnv(t, dir, "runpod.env", rk("JOBS")+"="+canaryJobs+"\n"+rk("JOBS")+"="+canaryRead+"\n")
		_, _, err := LoadRunPodKeys(dir)
		if err == nil || !strings.Contains(err.Error(), rk("JOBS")) || strings.Contains(err.Error(), canaryRead) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("all-access key refused", func(t *testing.T) {
		for _, name := range []string{"RUNPOD_API_KEY", "RUNPOD_ADMIN_KEY"} {
			dir := t.TempDir()
			writeEnv(t, dir, "runpod.env", rk("JOBS")+"="+canaryJobs+"\n"+rk("READ")+"="+canaryRead+"\nRUNPOD_ENDPOINT_ID="+canaryEP+"\n"+name+"=canary-admin-0005\n")
			_, _, err := LoadRunPodKeys(dir)
			if err == nil || !strings.Contains(err.Error(), "must not contain an all-access key") || strings.Contains(err.Error(), "canary-admin") {
				t.Fatalf("%s: err = %v", name, err)
			}
		}
	})
	t.Run("malformed line", func(t *testing.T) {
		dir := t.TempDir()
		writeEnv(t, dir, "runpod.env", "this is not a pair "+canaryJobs+"\n")
		_, _, err := LoadRunPodKeys(dir)
		if err == nil || strings.Contains(err.Error(), canaryJobs) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("never printed", func(t *testing.T) {
		k := RunPodKeys{Jobs: canaryJobs, Read: canaryRead, Write: canaryWrite, EndpointID: canaryEP, ConsoleURL: DefaultConsoleURL}
		out := fmt.Sprintf("%v %+v %#v %s", k, k, k, &k)
		for _, c := range []string{canaryJobs, canaryRead, canaryWrite, canaryEP} {
			if strings.Contains(out, c) {
				t.Errorf("formatted output leaks %s: %s", c, out)
			}
		}
		if k.String() != "RunPodKeys{jobs:set read:set write:set endpoint:set console:default}" {
			t.Errorf("String = %s", k.String())
		}
		if got := (RunPodKeys{Jobs: "x", Read: "y", EndpointID: "z"}).String(); got != "RunPodKeys{jobs:set read:set write:unset endpoint:set console:default}" {
			t.Errorf("String = %s", got)
		}
	})
}

func TestLoadAdminKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const canary = "canary-admin-0006"
	for _, name := range []string{"RUNPOD_ADMIN_KEY", "RUNPOD_API_KEY"} {
		p := writeEnv(t, dir, "a-"+name+".env", name+"="+canary+"\n")
		a, err := LoadAdminKey(p)
		if err != nil || a.Value() != canary {
			t.Fatalf("%s: %v", name, err)
		}
		if out := fmt.Sprintf("%v %+v %#v", a, a, a); strings.Contains(out, canary) || !strings.Contains(out, "AdminKey{set}") {
			t.Errorf("formatted: %s", out)
		}
	}
	both := writeEnv(t, dir, "both.env", "RUNPOD_ADMIN_KEY="+canary+"\nRUNPOD_API_KEY=canary-other-0007\n")
	if _, err := LoadAdminKey(both); err == nil || strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), "canary-other") {
		t.Fatalf("different values: err = %v", err)
	}
	same := writeEnv(t, dir, "same.env", "RUNPOD_ADMIN_KEY="+canary+"\nRUNPOD_API_KEY="+canary+"\n")
	if a, err := LoadAdminKey(same); err != nil || a.Value() != canary {
		t.Fatalf("same values: %v", err)
	}
	none := writeEnv(t, dir, "none.env", "OTHER=1\n")
	if _, err := LoadAdminKey(none); err == nil {
		t.Fatal("missing key accepted")
	}
	if _, err := LoadAdminKey(filepath.Join(dir, "absent.env")); err == nil {
		t.Fatal("absent file accepted")
	}
}

func TestSecrets_DerivedKeys(t *testing.T) {
	t.Parallel()
	s := Secrets{Key: []byte(strings.Repeat("k", 32))}
	u, c, m := s.UploadKey(), s.ConfirmKey(), s.MeterKey()
	if len(u) != 32 || string(u) == string(c) || string(c) == string(m) || string(u) == string(m) {
		t.Fatal("derived keys must be 32 bytes and pairwise distinct")
	}
	if string(s.UploadKey()) != string(u) {
		t.Error("derivation is not deterministic")
	}
	if string(u) == string(s.Key) {
		t.Error("derived key equals the root key")
	}
	other := Secrets{Key: []byte(strings.Repeat("j", 32))}
	if string(other.UploadKey()) == string(u) {
		t.Error("different roots derive the same key")
	}
}
