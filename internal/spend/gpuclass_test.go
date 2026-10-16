package spend

import "testing"

func TestGPUClassKey_Table(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		// the catalog names of the allowed set
		"PRO 6000 MIG 24GB": "PRO 6000 MIG 24GB",
		"RTX 3090":          "RTX 3090",
		"RTX 4090":          "RTX 4090",
		"RTX A5000":         "A5000",
		// what the worker reports (torch device names)
		"RTX PRO 6000 Blackwell Server Edition MIG 1g.24gb":        "PRO 6000 MIG 24GB",
		"NVIDIA RTX PRO 6000 Blackwell Server Edition MIG 1g.24gb": "PRO 6000 MIG 24GB",
		"NVIDIA RTX A5000":        "A5000",
		"NVIDIA GeForce RTX 3090": "RTX 3090",
		"NVIDIA GeForce RTX 4090": "RTX 4090",
		// stored forms and spelling noise
		"A5000":               "A5000",
		"  nvidia rtx a5000 ": "A5000",
		"geforce rtx 4090":    "RTX 4090",
		"RTX PRO 6000 Blackwell Workstation Edition": "PRO 6000",
		"NVIDIA RTX PRO 6000 MIG 48gb":               "PRO 6000 MIG 48GB",
		"NVIDIA L4":                                  "L4",
		"NVIDIA H100 80GB HBM3":                      "H100 80GB HBM3",
		"NVIDIA GeForce RTX 3080 Ti":                 "RTX 3080 Ti",
		"":                                           "",
	} {
		if got := GPUClassKey(in); got != want {
			t.Errorf("GPUClassKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGPUClassKey_Idempotent(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"RTX PRO 6000 Blackwell Server Edition MIG 1g.24gb", "NVIDIA RTX A5000", "NVIDIA GeForce RTX 3090", "NVIDIA L4", "PRO 6000 MIG 24GB"} {
		k := GPUClassKey(in)
		if GPUClassKey(k) != k {
			t.Errorf("not idempotent: %q -> %q -> %q", in, k, GPUClassKey(k))
		}
	}
}
