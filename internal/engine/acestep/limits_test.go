package acestep

import "testing"

// Placeholder RTFs of the review oracle (D-E2-17): base 1200 milli with LoadS 20, turbo 150 with LoadS 10.
func TestQuoteInputs_BaseClassBoundAtDefaultRuntime(t *testing.T) {
	t.Parallel()
	const rtf, load, maxRT = 1200, 20, 900
	for _, tc := range []struct {
		audio, gen, timeout int
		ok                  bool
	}{
		{300, 380, 688, true},
		{410, 512, 900, true},
		{411, 514, 903, false},
		{480, 596, 1034, false},
	} {
		gen := GenSeconds(tc.audio, rtf, load)
		to := UnclampedTimeoutS(gen)
		if gen != tc.gen || to != tc.timeout {
			t.Errorf("%d s: gen %d timeout %d, want %d %d", tc.audio, gen, to, tc.gen, tc.timeout)
		}
		is := CheckRuntime(tc.audio, rtf, load, maxRT)
		if (is == nil) != tc.ok {
			t.Errorf("%d s: CheckRuntime = %v, ok %v", tc.audio, is, tc.ok)
		}
		if is != nil && (is.Code != "exceeds_max_runtime" || !contains(is.Message, "MM_ACESTEP_MAX_RUNTIME")) {
			t.Errorf("issue %+v", is)
		}
	}
	if got := MaxAudioForRuntime(rtf, load, maxRT); got != 410 {
		t.Errorf("max base audio at 900 s = %d, want 410", got)
	}
	if CheckRuntime(480, rtf, load, 1200) != nil {
		t.Error("480 s must fit once the runtime is raised to 1200 s")
	}
}

func TestQuoteInputs_600sTurboFits(t *testing.T) {
	t.Parallel()
	gen := GenSeconds(600, 150, 10)
	if gen != 100 || UnclampedTimeoutS(gen) != 240 || CheckRuntime(600, 150, 10, 900) != nil {
		t.Errorf("gen %d timeout %d", gen, UnclampedTimeoutS(gen))
	}
	if got := GenSeconds(180, 150, 10); got != 37 || UnclampedTimeoutS(got) != 140 {
		t.Errorf("turbo 180: %d", got)
	}
	if MaxAudioForRuntime(150, 10, 30) != 0 {
		t.Error("a runtime nothing fits in must give 0")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
