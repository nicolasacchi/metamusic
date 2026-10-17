// Package api holds the operation registry, the error model and the M0 operations.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/redact"
)

// Problem is the single error body: application/problem+json {"code","message","details"}.
type Problem struct {
	Status  int            `json:"-"`
	Code    string         `json:"code" doc:"Stable UPPER_SNAKE error code" example:"NOT_FOUND"`
	Message string         `json:"message" doc:"Human-readable message"`
	Details map[string]any `json:"details" doc:"Structured error details"`
}

func (p *Problem) Error() string  { return p.Code + ": " + p.Message }
func (p *Problem) GetStatus() int { return p.Status }

// ContentType makes Huma serve Problem as application/problem+json.
func (p *Problem) ContentType(ct string) string {
	if ct == "application/json" {
		return "application/problem+json"
	}
	return ct
}

// Err builds a Problem error usable as a handler return value.
func Err(status int, code, msg string, details map[string]any) error {
	if details == nil {
		details = map[string]any{}
	}
	return &Problem{Status: status, Code: code, Message: msg, Details: details}
}

func codeFor(status int) string {
	switch status {
	case 400:
		return "BAD_REQUEST"
	case 401:
		return "UNAUTHENTICATED"
	case 403:
		return "FORBIDDEN"
	case 404:
		return "NOT_FOUND"
	case 405:
		return "METHOD_NOT_ALLOWED"
	case 409:
		return "CONFLICT"
	case 413:
		return "PAYLOAD_TOO_LARGE"
	case 415:
		return "UNSUPPORTED_MEDIA_TYPE"
	case 422:
		return "VALIDATION_FAILED"
	case 429:
		return "RATE_LIMITED"
	}
	if status >= 500 {
		return "INTERNAL"
	}
	return fmt.Sprintf("HTTP_%d", status)
}

type loggerKey struct{}

// WithLogger returns a context whose handler errors are logged to l instead of the process default. Tests inject a
// logger this way rather than mutating the process-global slog default (which races with parallel tests).
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

func loggerFrom(ctx huma.Context) *slog.Logger {
	if ctx != nil {
		if l, ok := ctx.Context().Value(loggerKey{}).(*slog.Logger); ok && l != nil {
			return l
		}
	}
	return slog.Default()
}

func init() {
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		return newProblem(nil, status, msg, errs...)
	}
	huma.NewErrorWithContext = func(ctx huma.Context, status int, msg string, errs ...error) huma.StatusError {
		return newProblem(ctx, status, msg, errs...)
	}
}

func newProblem(ctx huma.Context, status int, msg string, errs ...error) huma.StatusError {
	p := &Problem{Status: status, Code: codeFor(status), Message: msg, Details: map[string]any{}}
	if status >= 500 {
		// Never leak internals to clients.
		for _, e := range errs {
			if e != nil {
				attrs := []any{"status", status, "err", redact.Error(e)}
				if ctx != nil {
					if op := ctx.Operation(); op != nil {
						attrs = append(attrs, "operation", op.OperationID)
					}
					attrs = append(attrs, "path", ctx.URL().Path)
				}
				loggerFrom(ctx).Error("handler error", attrs...)
			}
		}
		if msg == "" || msg == "unexpected error occurred" {
			p.Message = "internal error"
		}
		return p
	}
	var list []any
	for _, e := range errs {
		if e == nil {
			continue
		}
		if d, ok := e.(huma.ErrorDetailer); ok {
			list = append(list, d.ErrorDetail())
		} else {
			list = append(list, &huma.ErrorDetail{Message: e.Error()})
		}
	}
	if list != nil {
		p.Details["errors"] = list
	}
	if status == http.StatusUnprocessableEntity {
		p.Code = "VALIDATION_FAILED"
	}
	return p
}

// CodeFor returns the stable UPPER_SNAKE problem code for an HTTP status.
func CodeFor(status int) string { return codeFor(status) }
