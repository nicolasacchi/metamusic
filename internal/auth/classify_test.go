package auth

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		path string
		want PathClass
	}{
		{"/healthz", ClassPublic}, {"/manifest.webmanifest", ClassPublic},
		{"/icons/a.png", ClassPublic}, {"/icons/", ClassPublic},
		{"/auth/login", ClassPublic}, {"/auth/logout", ClassPublic},
		{"/assets/app.js", ClassPublic}, {"/assets/", ClassPublic},
		{"/mcp", ClassMachine}, {"/mcp/", ClassMachine}, {"/mcp/x", ClassMachine},
		{"/rest/tracks", ClassMachine}, {"/media/a.flac", ClassMachine},
		{"/worker/cb", ClassMachine}, {"/.well-known/oauth-protected-resource/mcp", ClassMachine},
		{"/oauth/token", ClassMachine}, {"/oauth/register", ClassMachine}, {"/oauth/revoke", ClassMachine},
		// near-misses fall to UI
		{"/restore", ClassUI}, {"/mcpx", ClassUI}, {"/media", ClassUI}, {"/api", ClassUI},
		{"/rest", ClassUI}, {"/worker", ClassUI}, {"/healthz/x", ClassUI}, {"/icons", ClassUI},
		{"/assets", ClassUI}, {"/auth/loginx", ClassUI}, {"/oauth/tokenx", ClassUI},
		{"/.well-known", ClassUI}, {"/oauth/authorize", ClassUI},
		{"/", ClassUI}, {"/tracks/1", ClassUI}, {"/api/tracks", ClassUI}, {"/human/api/x", ClassUI},
		{"/human/x", ClassUI}, {"", ClassUI},
		// dot-segment near-misses are never public/machine
		{"/assets/../api/x", ClassUI}, {"/mcp/../api/x", ClassUI}, {"/icons/../../x", ClassUI},
		{"/media/..", ClassUI}, {"/assets/a..b", ClassPublic},
	}
	for _, tc := range tests {
		if got := Classify(tc.path); got != tc.want {
			t.Errorf("Classify(%q) = %d, want %d", tc.path, got, tc.want)
		}
	}
}
