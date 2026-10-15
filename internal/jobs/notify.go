package jobs

import (
	"fmt"
	"strings"

	"github.com/nicolasacchi/metamusic/internal/money"
	"github.com/nicolasacchi/metamusic/internal/notify"
)

// The notification rows of C82. Every builder returns a message whose body and links contain no token,
// no URL query string, no endpoint id, no key and no provider ref; page links are <PublicURL>/<path>.

func page(publicURL, path string) string { return strings.TrimRight(publicURL, "/") + path }

// NoteApprovalRequested is "approval requested" (priority 4): an agent asks a human to approve a batch.
func NoteApprovalRequested(publicURL, tokenName string, takes int, ceiling money.MicroUSD, batchID string) notify.Message {
	url := page(publicURL, "/human/confirm/"+batchID)
	return notify.Message{Title: "Approval needed", Priority: 4,
		Body:    fmt.Sprintf("Agent %s asks to spend up to %s for %d takes", tokenName, ceiling.DisplayCeil(), takes),
		Click:   url,
		Actions: []notify.Action{{Action: "view", Label: "Review", URL: url}}}
}

// NoteBatchDone is "batch finished" (priority 3).
func NoteBatchDone(publicURL, batchID, summary string) notify.Message {
	return notify.Message{Title: "Batch done", Priority: 3, Body: summary, Click: page(publicURL, "/batches/"+batchID)}
}

// NoteProviderTripped is "provider tripped" (priority 5): reason is the trip reason, actions what was done.
func NoteProviderTripped(publicURL, reason, actions string) notify.Message {
	return notify.Message{Title: "Generation stopped: " + reason, Priority: 5, Body: actions, Click: page(publicURL, "/jobs")}
}

// NoteManualScaleToZero is "manual scale-to-zero needed" (priority 5, rotating_light): the app cannot change
// the endpoint, so the operator does it in the console.
//
// maxWorkers is the endpoint's workers.max as the last sweep saw it (nil: unknown) and live the number of
// workers still up. When max is already 0 the alert says so: the remaining workers have to be stopped
// (terminated) in the console, setting max again changes nothing.
func NoteManualScaleToZero(consoleURL string, maxWorkers *int, live int) notify.Message {
	body := "The app cannot change the endpoint; open the console"
	if maxWorkers != nil && *maxWorkers == 0 {
		body += ". workers.max is already 0"
		if live > 0 {
			body += fmt.Sprintf(" but %d worker(s) are still up: terminate them", live)
		}
	} else if live > 0 {
		body += fmt.Sprintf(" (%d worker(s) are up)", live)
	}
	return notify.Message{Title: "Set max workers to 0 now", Priority: 5, Tags: []string{"rotating_light"},
		Body: body, Click: consoleURL,
		Actions: []notify.Action{{Action: "view", Label: "Open console", URL: consoleURL}}}
}

// NoteProviderAccessLost is "provider access lost" (priority 5, rotating_light).
func NoteProviderAccessLost(publicURL string) notify.Message {
	return notify.Message{Title: "RunPod keys refused", Priority: 5, Tags: []string{"rotating_light"},
		Body:  "Polling/cancel/sweep refused (401/403/1010); jobs settled by their time bound",
		Click: page(publicURL, "/jobs")}
}

// NoteMeterExceeds is "meter above reservations" (priority 5).
func NoteMeterExceeds(publicURL string) notify.Message {
	return notify.Message{Title: "Worker time above reservation", Priority: 5,
		Body: "Metered worker seconds exceed what the open batches reserved.", Click: page(publicURL, "/jobs")}
}

// NoteWorkersNotZero is "workers not at zero after a batch" (priority 4).
func NoteWorkersNotZero(publicURL string) notify.Message {
	return notify.Message{Title: "Workers still running", Priority: 4,
		Body: "A worker is still up after the last batch finished and no attempt is live.", Click: page(publicURL, "/jobs")}
}

// NoteInitializingSlow is "a worker has been INITIALIZING for 30 min" (priority 3, warning only: an
// initializing worker is not billed and never trips the provider).
func NoteInitializingSlow(publicURL string, minutes int) notify.Message {
	return notify.Message{Title: "Worker still initializing", Priority: 3,
		Body:  fmt.Sprintf("A worker has been INITIALIZING for %d min (image pull or model download). It is not billed.", minutes),
		Click: page(publicURL, "/jobs")}
}

// NoteCancelNotConfirmed is "cancel not confirmed after 5 min" (priority 4).
func NoteCancelNotConfirmed(publicURL, jobID string) notify.Message {
	return notify.Message{Title: "Cancel not confirmed yet", Priority: 4,
		Body: "The provider has not confirmed a cancel for five minutes.", Click: page(publicURL, "/jobs/"+jobID)}
}

// NoteConfigDrift is "config drift" (priority 4); reverted says whether the endpoint was put back.
func NoteConfigDrift(publicURL string, fields []string, reverted bool) notify.Message {
	body := "Not reverted; fix it in the console."
	if reverted {
		body = "Reverted to the desired configuration."
	}
	return notify.Message{Title: "Endpoint drift: " + strings.Join(fields, ", "), Priority: 4, Body: body, Click: page(publicURL, "/jobs")}
}

// NoteClockSkew is "clock skew above 30 s" (priority 4).
func NoteClockSkew(publicURL string, seconds int) notify.Message {
	return notify.Message{Title: fmt.Sprintf("Clock skew %d s - billing releases paused", seconds), Priority: 4,
		Body: "The server clock differs from the provider's by more than 30 seconds.", Click: page(publicURL, "/jobs")}
}

// NoteBillingStalled is "no quiet window for 72 h" (priority 4).
func NoteBillingStalled(publicURL string, batchIDs []string) notify.Message {
	return notify.Message{Title: "Billing reconciliation stalled", Priority: 4,
		Body: "No quiet billing window for 72 hours. Blocking batches: " + strings.Join(batchIDs, ", "), Click: page(publicURL, "/jobs")}
}

// NotePreflightFailing is "claim preflight failing" (priority 4); why is "disk low" or "upload route down".
func NotePreflightFailing(publicURL, why string) notify.Message {
	return notify.Message{Title: "Generation paused: " + why, Priority: 4, Body: "Queued takes wait until the check passes.", Click: page(publicURL, "/jobs")}
}

// NoteBillingUnreadable is the weekly "billing not reconciled" reminder (priority 2).
func NoteBillingUnreadable(publicURL string, released int) notify.Message {
	return notify.Message{Title: "Billing not reconciled", Priority: 2,
		Body: fmt.Sprintf("%d batches released after the hold", released), Click: page(publicURL, "/jobs")}
}

// NoteIdleDrift names an idle timeout above a running batch's frozen value (handled at claim time).
func NoteIdleDrift(publicURL string, liveIdle, frozen int) notify.Message {
	return notify.Message{Title: "Endpoint drift: idle timeout", Priority: 4,
		Body:  fmt.Sprintf("The endpoint idle timeout is %d s, above the %d s of a running batch; its reserves use the larger value.", liveIdle, frozen),
		Click: page(publicURL, "/jobs")}
}
