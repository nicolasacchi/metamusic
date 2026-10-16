package reqinfo

import (
	"net/http"
	"net/netip"
	"testing"
)

func TestClientIP_Table(t *testing.T) {
	t.Parallel()
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	for _, tc := range []struct {
		name, remote, xff, want string
	}{
		{"untrusted peer ignores xff", "192.0.2.9:1234", "198.51.100.7", "192.0.2.9"},
		{"trusted peer uses xff", "10.0.0.1:80", "198.51.100.7", "198.51.100.7"},
		{"right-most untrusted", "10.0.0.1:80", "203.0.113.5, 198.51.100.7, 10.0.0.2", "198.51.100.7"},
		{"spoofed left entry ignored", "10.0.0.1:80", "1.2.3.4, 198.51.100.7", "198.51.100.7"},
		{"all trusted", "10.0.0.1:80", "10.0.0.5, 10.0.0.2", "10.0.0.1"},
		{"garbage", "10.0.0.1:80", "not-an-ip", "10.0.0.1"},
		{"garbage right-most", "10.0.0.1:80", "198.51.100.7, junk", "10.0.0.1"},
		{"no xff", "10.0.0.1:80", "", "10.0.0.1"},
		{"ipv6 peer", "[2001:db8::1]:80", "198.51.100.7", "2001:db8::1"},
	} {
		r := &http.Request{RemoteAddr: tc.remote, Header: http.Header{}}
		if tc.xff != "" {
			r.Header.Add("X-Forwarded-For", tc.xff)
		}
		if got := ClientIP(r, trusted); got.String() != tc.want {
			t.Errorf("%s: ClientIP = %v, want %s", tc.name, got, tc.want)
		}
	}
	// multiple header lines count as one list
	r := &http.Request{RemoteAddr: "10.0.0.1:80", Header: http.Header{}}
	r.Header.Add("X-Forwarded-For", "203.0.113.5")
	r.Header.Add("X-Forwarded-For", "198.51.100.7")
	if got := ClientIP(r, trusted); got.String() != "198.51.100.7" {
		t.Errorf("two lines: %v", got)
	}
	// unparsable RemoteAddr yields the zero address
	if got := ClientIP(&http.Request{RemoteAddr: "junk", Header: http.Header{}}, trusted); got.IsValid() {
		t.Errorf("junk remote: %v", got)
	}
}

func TestTransport_M4a(t *testing.T) {
	t.Parallel()
	if Subsonic != "subsonic" || OAuth != "oauth" {
		t.Fatal(Subsonic, OAuth)
	}
}
