package api_test

// implementedVoiceM5 lists the M5 operations whose 501 stub lane M5-V1 has replaced (ids from m5Ops in ops_m5_test.go). The lane
// adds its ids here, drops the matching 501 parity cases and covers the behaviour in its own tests; it owns this file alone.
var implementedVoiceM5 = map[string]bool{}
