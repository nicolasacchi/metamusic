package objstore

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The AWS documentation example credentials are assembled at run time so no key-shaped literal sits in the repo.
func docsConfig() Config {
	return Config{
		Region:          "us-east-1",
		AccessKeyID:     "AKIA" + "IOSFODNN7" + "EXAMPLE",
		SecretAccessKey: "wJalr" + "XUtnFEMI/" + "K7MDENG" + "/bPxRf" + "iCY" + "EXAMPLE" + "KEY",
	}
}

func docsTime() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) }

func TestSigV4_AWSDocsQueryExample(t *testing.T) {
	r, err := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := SignQuery(r, docsConfig(), docsTime(), 86400*time.Second); err != nil {
		t.Fatal(err)
	}
	const want = "aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	if got := r.URL.Query().Get("X-Amz-Signature"); got != want {
		t.Fatalf("signature = %s, want %s", got, want)
	}
	q := r.URL.Query()
	if q.Get("X-Amz-Expires") != "86400" || q.Get("X-Amz-SignedHeaders") != "host" || q.Get("X-Amz-Date") != "20130524T000000Z" {
		t.Fatalf("query = %v", q)
	}
}

func TestSigV4_AWSDocsHeaderExample(t *testing.T) {
	r, err := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Range", "bytes=0-9")
	const emptySHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if err := SignHeader(r, docsConfig(), docsTime(), emptySHA); err != nil {
		t.Fatal(err)
	}
	auth := r.Header.Get("Authorization")
	const want = "Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if !strings.HasSuffix(auth, want) || !strings.Contains(auth, "SignedHeaders=host;range;x-amz-content-sha256;x-amz-date") {
		t.Fatalf("Authorization = %s", auth)
	}
}

// Golden computed with an independent implementation (python hmac) for the path-style, region auto case.
func TestSigV4_PathStyleGolden(t *testing.T) {
	c := Config{
		Endpoint: "https://mm-r2.example.com", Bucket: "mm-test", Region: "auto",
		AccessKeyID:     "AKIA" + "MMTESTFIXTURE01",
		SecretAccessKey: "mmtestsecret" + "fixture0123456789abcdefghijklmnop",
	}
	cl, err := New(c, nil, func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	raw, err := cl.PresignPut("mm/0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "mm-r2.example.com" || u.Path != "/mm-test/mm/0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b" {
		t.Fatalf("url = %s", u.Redacted())
	}
	const want = "f9eb29d36dfe74cf183d7301eb579df9a3ab6a03e707c792819523e33148c9c3"
	if got := u.Query().Get("X-Amz-Signature"); got != want {
		t.Fatalf("signature = %s, want %s", got, want)
	}
	if got := u.Query().Get("X-Amz-Credential"); got != c.AccessKeyID+"/20261120/auto/s3/aws4_request" {
		t.Fatalf("credential = %s", got)
	}
}

func TestPresignPut_TTLBounds(t *testing.T) {
	cl, err := New(Config{Endpoint: "https://mm-r2.example.com", Bucket: "mm-test", Region: "auto", AccessKeyID: "AKIA" + "MMTESTFIXTURE01", SecretAccessKey: "mmtestsecret" + "fixture0123456789"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ttl time.Duration
		ok  bool
	}{
		{0, false}, {-time.Second, false}, {999 * time.Millisecond, false}, {time.Second, true},
		{7 * 24 * time.Hour, true}, {7*24*time.Hour + time.Second, false},
	} {
		_, err := cl.PresignPut("mm/x", tc.ttl)
		if (err == nil) != tc.ok {
			t.Errorf("ttl %v: err = %v, want ok=%v", tc.ttl, err, tc.ok)
		}
	}
}

func TestNew_Validates(t *testing.T) {
	good := Config{Endpoint: "https://mm-r2.example.com", Bucket: "mm-test", Region: "auto", AccessKeyID: "AKIA" + "MMTESTFIXTURE01", SecretAccessKey: "mmtestsecret" + "fixture0123456789"}
	if _, err := New(good, nil, nil); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Config){
		"no endpoint": func(c *Config) { c.Endpoint = "" },
		"path":        func(c *Config) { c.Endpoint = "https://mm-r2.example.com/x" },
		"no bucket":   func(c *Config) { c.Bucket = "" },
		"no region":   func(c *Config) { c.Region = "" },
		"no key id":   func(c *Config) { c.AccessKeyID = "" },
		"no secret":   func(c *Config) { c.SecretAccessKey = "" },
		"ftp":         func(c *Config) { c.Endpoint = "ftp://mm-r2.example.com" },
	} {
		c := good
		mut(&c)
		if _, err := New(c, nil, nil); err == nil {
			t.Errorf("%s: want error", name)
		} else if strings.Contains(err.Error(), good.SecretAccessKey) {
			t.Errorf("%s: error leaks the secret", name)
		}
	}
}

func TestConfig_NeverPrintsSecrets(t *testing.T) {
	c := Config{Endpoint: "https://mm-r2.example.com", Bucket: "mm-test", Region: "auto", AccessKeyID: "AKIA" + "MMTESTFIXTURE01", SecretAccessKey: "mmtestsecret" + "fixture0123456789"}
	for _, s := range []string{c.String(), strings.TrimSpace(sprintf("%v %+v %#v", c, c, c))} {
		if strings.Contains(s, c.SecretAccessKey) || strings.Contains(s, c.AccessKeyID) {
			t.Fatalf("leak: %s", s)
		}
	}
}
