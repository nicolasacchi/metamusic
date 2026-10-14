package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/redact"
)

// preflightReserve is the free space kept beyond the open uploads (D-M3-33).
const preflightReserve = 1 << 30

// PreflightOptions builds the claim preflight (D-M3-33).
type PreflightOptions struct {
	DataDir   string
	PublicURL string
	HTTP      *http.Client // nil => a 5 s client
	NewID     func() string
	// Free reports the bytes available under dir; nil => the file system's (statfs).
	Free func(dir string) (uint64, error)
}

// NewPreflight returns the Runner's Preflight func: free disk under DataDir must cover every open upload
// of the live attempts plus the new one plus 1 GiB, and the public upload route must answer the expected
// 403 problem body for an unknown upload id, so the worker's PUT will reach this process.
func NewPreflight(o PreflightOptions) func(ctx context.Context, inFlightMaxBytes int64) error {
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 5 * time.Second}
	}
	if o.Free == nil {
		o.Free = freeBytes
	}
	if o.NewID == nil {
		o.NewID = newUUID
	}
	return func(ctx context.Context, inFlight int64) error {
		free, err := o.Free(o.DataDir)
		if err != nil {
			return &PreflightError{Check: "disk", Err: err}
		}
		need := uint64(inFlight) + preflightReserve //nolint:gosec // inFlight is a sum of positive limits
		if free < need {
			return &PreflightError{Check: "disk", Err: fmt.Errorf("%d bytes free, %d needed", free, need)}
		}
		return checkRoute(ctx, o)
	}
}

// checkRoute PUTs nothing to a random upload id through the public URL. The upload handler answers a link
// without a signature with 403 UPLOAD_LINK_INVALID; anything else means the route is not reaching us.
func checkRoute(ctx context.Context, o PreflightOptions) error {
	u := strings.TrimRight(o.PublicURL, "/") + "/worker/v1/uploads/" + o.NewID()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, http.NoBody)
	if err != nil {
		return &PreflightError{Check: "route", Err: errors.New("invalid public URL")}
	}
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return &PreflightError{Check: "route", Err: errors.New(redact.Error(err))}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	var p struct {
		Code string `json:"code"`
	}
	if resp.StatusCode != http.StatusForbidden || json.Unmarshal(body, &p) != nil || p.Code != "UPLOAD_LINK_INVALID" {
		return &PreflightError{Check: "route", Err: fmt.Errorf("upload route answered %d, not the expected 403 problem", resp.StatusCode)}
	}
	return nil
}
