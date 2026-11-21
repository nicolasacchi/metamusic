// Package pods is the RunPod pod API client of the bulk backend (C392). It speaks REST v2 on the management host with a
// scoped pod key that is read from a mode-600 file at every call. It imports neither the serverless package, nor the job
// runner, nor any engine (TestImportGraph_Bulk): the dependency arrow points the other way.
package pods

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/money"
)

// Hosts, timeouts and limits (C392.1).
const (
	DefaultBase = "https://api.runpod.io"

	CreateTimeout   = 60 * time.Second
	OtherTimeout    = 15 * time.Second
	LogsReadTimeout = 5 * time.Second
	MaxRequestBody  = 64 << 10
	MaxResponse     = 1 << 20
	MaxLogTail      = 5000 // the v2 `tail` maximum

	maxGETRetries = 3
	maxListPages  = 50
)

// MinCudaVersion is the `gpu.minCudaVersion` of a pod create: the value of the serverless endpoint's desired spec
// (TestPodMinCUDA_EqualsServerless pins the two together).
const MinCudaVersion = "12.8"

// Pod statuses (the v2 `PodStatus` enum).
const (
	StatusProvisioning = "PROVISIONING"
	StatusStarting     = "STARTING"
	StatusRunning      = "RUNNING"
	StatusExited       = "EXITED"
	StatusError        = "ERROR"
	StatusTerminated   = "TERMINATED"
)

// Pod is the part of a v2 pod the lifecycle reads. CostPPH is the hourly rate in micro-USD (zero when RunPod gave none).
type Pod struct {
	ID, Name, Status, Image string
	CostPPH                 money.MicroUSD
	CreatedAt, StartedAt    *time.Time
}

// CreateBody is what the lifecycle chooses for one pod; everything else of the v2 create body is fixed by MarshalJSON (C392.2):
// one GPU, SECURE cloud, no ports, no SSH, no Jupyter, no global networking, and never `queue`, `templateId`, `mounts`,
// `dataCenterIds`, `cpu`, `registry` or any v1 name.
type CreateBody struct {
	Name      string            // mm-<12 hex> (the canary pod: mm-c<11 hex>)
	Image     string            // <ref>@sha256:<64 hex>
	GPUTypeID string            // catalog type id
	DiskGB    int               // container disk
	Env       map[string]string // MODE, MM_POLL_URL (secret), deadline, dead-man, weights revision and dir
}

var (
	podNameRE  = regexp.MustCompile(`^mm-(?:[0-9a-f]{12}|c[0-9a-f]{11})$`)
	podImageRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)
	podIDRE    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// ErrBadBody is returned before any request when the create body breaks a rule of C392.2.
var ErrBadBody = errors.New("pods: invalid create body")

// Validate checks the body; the error names the field, never a value (the env carries the poll URL).
func (b CreateBody) Validate() error {
	switch {
	case !podNameRE.MatchString(b.Name):
		return fmt.Errorf("%w: name must be mm-<12 hex> or mm-c<11 hex>", ErrBadBody)
	case !podImageRE.MatchString(b.Image):
		return fmt.Errorf("%w: image must be pinned by digest", ErrBadBody)
	case strings.TrimSpace(b.GPUTypeID) == "" || len(b.GPUTypeID) > 64:
		return fmt.Errorf("%w: gpu type id", ErrBadBody)
	case b.DiskGB < 20 || b.DiskGB > 200:
		return fmt.Errorf("%w: disk must be 20..200 GB", ErrBadBody)
	}
	for k := range b.Env {
		if k == "" {
			return fmt.Errorf("%w: empty env name", ErrBadBody)
		}
	}
	return nil
}

type createGPU struct {
	ID             string `json:"id"`
	Count          int    `json:"count"`
	MinCudaVersion string `json:"minCudaVersion"`
}

// createWire is the v2 `CreatePodRequest` (golden testdata/create_body.golden.json); field order is the golden's order.
type createWire struct {
	Name             string            `json:"name"`
	Image            string            `json:"image"`
	GPU              createGPU         `json:"gpu"`
	Cloud            string            `json:"cloud"`
	Disk             int               `json:"disk"`
	Ports            []string          `json:"ports"`
	StartSSH         bool              `json:"startSsh"`
	StartJupyter     bool              `json:"startJupyter"`
	GlobalNetworking bool              `json:"globalNetworking"`
	Env              map[string]string `json:"env"`
}

// MarshalJSON validates and encodes the v2 create body.
func (b CreateBody) MarshalJSON() ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	env := b.Env
	if env == nil {
		env = map[string]string{}
	}
	return json.Marshal(createWire{
		Name: b.Name, Image: b.Image,
		GPU:   createGPU{ID: b.GPUTypeID, Count: 1, MinCudaVersion: MinCudaVersion},
		Cloud: "SECURE", Disk: b.DiskGB, Ports: []string{}, Env: env,
	})
}

// podWire is the part of the v2 `Pod` that is read. cost stays a json.Number: it is parsed without a float.
type podWire struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Status    string      `json:"status"`
	Image     string      `json:"image"`
	Cost      json.Number `json:"cost"`
	CreatedAt *time.Time  `json:"createdAt"`
	StartedAt *time.Time  `json:"startedAt"`
}

func (w podWire) pod() (Pod, error) {
	p := Pod{ID: w.ID, Name: w.Name, Status: w.Status, Image: w.Image, CreatedAt: w.CreatedAt, StartedAt: w.StartedAt}
	if w.Cost != "" {
		c, err := money.ParseUSD(w.Cost.String())
		if err != nil {
			return Pod{}, errors.New("pods: unreadable pod cost")
		}
		p.CostPPH = c
	}
	return p, nil
}

// decodeJSON parses a 2xx body with numbers kept as json.Number.
func decodeJSON(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	return dec.Decode(v)
}
