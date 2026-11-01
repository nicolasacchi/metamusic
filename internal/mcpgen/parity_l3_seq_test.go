package mcpgen_test

// l3SeqCases are the parity cases of suggest_sequence (lane SR owns this file). Until SR lands the stub answers 501 on every
// face; SR replaces the case and drops Status.
var l3SeqCases = []ParityCase{
	{Op: "suggest_sequence", Args: fixedArgs(map[string]any{"record_id": "no-such-record"}), Status: 501},
}
