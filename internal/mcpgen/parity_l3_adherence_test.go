package mcpgen_test

// l3AdherenceCases are the parity cases of get_adherence (lane AD owns this file). Until AD lands the stub answers 501 on every
// face; AD replaces the case and drops Status.
var l3AdherenceCases = []ParityCase{
	{Op: "get_adherence", Args: fixedArgs(map[string]any{"min_n": 3}), Status: 501},
}
