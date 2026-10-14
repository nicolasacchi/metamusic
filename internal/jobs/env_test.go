package jobs

import (
	"context"
	"strings"
	"testing"
)

func TestGPUClass_Table(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"NVIDIA RTX A5000":         "A5000",
		"NVIDIA GeForce RTX 3090":  "RTX 3090",
		"NVIDIA GeForce RTX 4090":  "RTX 4090",
		"NVIDIA L4":                "L4",
		"NVIDIA H100 80GB HBM3":    "H100 80GB HBM3",
		"NVIDIA GeForce RTX 3080":  "GeForce RTX 3080",
		"Something Else":           "Something Else",
		"":                         "",
		"  NVIDIA RTX A5000  ":     "A5000",
		"nvidia rtx a5000":         "nvidia rtx a5000",
		"NVIDIA RTX A5000 Laptop":  "RTX A5000 Laptop",
		"NVIDIA GeForce RTX 4090 ": "RTX 4090",
	} {
		if got := GPUClass(in); got != want {
			t.Errorf("GPUClass(%q) = %q, want %q", in, got, want)
		}
	}
}

func envIn() EnvInput {
	return EnvInput{
		Engine: "yue2", PkgVersion: "0.1.6", CodeCommit: strings.Repeat("3", 40),
		Weights: map[string]string{"m-a-p/YuE2-Vae": strings.Repeat("2", 40), "m-a-p/YuE2-3B": strings.Repeat("1", 40)},
		Decoder: "m-a-p/YuE2-Vae", Backend: "fake", GPUClass: "A5000", ImageDigest: "sha256:" + strings.Repeat("0", 64),
	}
}

// TestEnvID_Golden pins the canonical form of D-M3-26: a changed byte of the recipe for the id changes
// every environment id ever stored.
func TestEnvID_Golden(t *testing.T) {
	t.Parallel()
	got, err := EnvID(envIn())
	if err != nil {
		t.Fatal(err)
	}
	const want = "a1494bb7e1dece833cb6a03006283d290116f72718bfbd6c02c8fb985e8612ea"
	if got != want {
		t.Errorf("EnvID = %s\nwant    %s", got, want)
	}
	// weights order never matters; every identifying field does
	in := envIn()
	in.Weights = map[string]string{"m-a-p/YuE2-3B": strings.Repeat("1", 40), "m-a-p/YuE2-Vae": strings.Repeat("2", 40)}
	if again, _ := EnvID(in); again != got {
		t.Error("weights order changed the id")
	}
	for name, mut := range map[string]func(*EnvInput){
		"engine":  func(e *EnvInput) { e.Engine = "other" },
		"pkg":     func(e *EnvInput) { e.PkgVersion = "0.1.7" },
		"commit":  func(e *EnvInput) { e.CodeCommit = strings.Repeat("4", 40) },
		"weights": func(e *EnvInput) { e.Weights = map[string]string{"m-a-p/YuE2-3B": strings.Repeat("9", 40)} },
		"decoder": func(e *EnvInput) { e.Decoder = "x" },
		"backend": func(e *EnvInput) { e.Backend = "runpod-serverless" },
		"gpu":     func(e *EnvInput) { e.GPUClass = "RTX 4090" },
		"image":   func(e *EnvInput) { e.ImageDigest = "build:abc" },
	} {
		in := envIn()
		mut(&in)
		if other, _ := EnvID(in); other == got {
			t.Errorf("changing %s kept the id", name)
		}
	}
}

func TestImageDigestFor(t *testing.T) {
	t.Parallel()
	dg := "sha256:" + strings.Repeat("a", 64)
	for _, c := range []struct{ image, build, want string }{
		{"registry.example.com/org/img@" + dg, "b1", dg},
		{"registry.example.com/org/img:latest", "b1", "build:b1"},
		{"", "b1", "build:b1"},
		{"img@sha256:short", "b1", "build:b1"},
		{"", "", "build:unknown"},
	} {
		if got := ImageDigestFor(c.image, c.build); got != c.want {
			t.Errorf("ImageDigestFor(%q,%q) = %q, want %q", c.image, c.build, got, c.want)
		}
	}
}

// TestEnv_ImageFromWorkerRecord: the digest comes from the worker that ran the attempt, so an image
// release mid-batch leaves takes on old workers with the old digest (D-M3-26).
func TestEnv_ImageFromWorkerRecord(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	oldDG, newDG := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	ts := "2026-10-14T12:00:00.000Z"
	for k, dg := range map[string]string{"wk-old": oldDG, "wk-new": newDG} {
		r.exec(`INSERT INTO worker_meter(worker_key, started_at, first_seen_at, last_seen_at, uptime_s, status_last, image) VALUES(?,?,?,?,100,'RUNNING',?)`,
			k, ts, ts, ts, "reg.example.com/img@"+dg)
	}
	r.exec(`INSERT INTO worker_meter(worker_key, started_at, first_seen_at, last_seen_at, uptime_s, status_last, image) VALUES('wk-tag',?,?,?,100,'RUNNING','reg.example.com/img:latest')`, ts, ts, ts)
	ctx := context.Background()
	for _, c := range []struct{ key, attempt, build, want string }{
		{"wk-old", "", "bld", oldDG},
		{"wk-new", "", "bld", newDG},
		{"wk-tag", "", "bld", "build:bld"},
		{"", "", "bld", "build:bld"},
		{"wk-gone", "", "bld", "build:bld"},
		{"wk-new", oldDG, "bld", oldDG}, // the attempt's own recorded digest wins
	} {
		got, err := imageDigestOf(ctx, r.st.R, c.attempt, c.key, c.build)
		if err != nil || got != c.want {
			t.Errorf("imageDigestOf(%q,%q) = %q, %v; want %q", c.key, c.attempt, got, err, c.want)
		}
	}
}
