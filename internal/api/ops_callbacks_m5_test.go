package api_test

// implementedCallbacksM5 lists the M5 operations whose 501 stub lane M5-C1 has replaced (ids from m5Ops in ops_m5_test.go). The lane
// adds its ids here, drops the matching 501 parity cases and covers the behaviour in its own tests; it owns this file alone.
var implementedCallbacksM5 = map[string]bool{}

// noObjectCallbacksM5 lists the {id} reads the L1 leak sweep (ops_l1_leaks_test.go) has no object for in its rig; the lane deletes the
// entry when INT-B's M5 sweep covers the operation.
var noObjectCallbacksM5 = map[string]bool{"get_callback": true}
