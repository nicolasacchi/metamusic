package subsonic

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"strconv"
	"time"
)

// Protocol error codes (C108).
const (
	errGeneric       = 0
	errMissingParam  = 10
	errServerUpgrade = 30
	errBadCreds      = 40
	errAuthMechanism = 42
	errAuthConflict  = 43
	errBadAPIKey     = 44
	errNotAuthorized = 50
	errNotFound      = 70
)

// format is the response encoding chosen by the `f` parameter.
type format int

const (
	fmtXML format = iota
	fmtJSON
)

// apiError is a protocol error. status is the HTTP status (0 => 200); retryAfter is set with 429.
type apiError struct {
	code       int
	msg        string
	status     int
	retryAfter time.Duration
}

func (e *apiError) Error() string { return "subsonic error " + strconv.Itoa(e.code) + ": " + e.msg }

func fail(code int, msg string) *apiError { return &apiError{code: code, msg: msg} }

func tooMany(msg string, retry time.Duration) *apiError {
	if retry < time.Second {
		retry = time.Second
	}
	return &apiError{code: errGeneric, msg: msg, status: http.StatusTooManyRequests, retryAfter: retry}
}

// newResponse returns an ok envelope for the server.
func (s *server) newResponse() Response {
	return Response{XMLNS: xmlNamespace, Status: "ok", Version: APIVersion, Type: "metamusic", ServerVersion: s.o.Version, OpenSubsonic: true}
}

// writeError answers a failed envelope. Format jsonp or unknown never reaches here: those answer JSON.
func (s *server) writeError(w http.ResponseWriter, f format, e *apiError) {
	resp := s.newResponse()
	resp.Status = "failed"
	resp.Error = &ErrorBody{Code: e.code, Message: e.msg}
	status := e.status
	if status == 0 {
		status = http.StatusOK
	}
	if e.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(int64((e.retryAfter+time.Second-1)/time.Second), 10))
	}
	s.writeEnvelope(w, f, status, resp)
}

// writeEnvelope encodes resp (C108.4). Non-binary responses are never cached and never sniffed.
func (s *server) writeEnvelope(w http.ResponseWriter, f format, status int, resp Response) {
	var buf bytes.Buffer
	ct := "text/xml; charset=utf-8"
	if f == fmtJSON {
		ct = "application/json"
		if err := json.NewEncoder(&buf).Encode(map[string]Response{"subsonic-response": resp}); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	} else {
		buf.WriteString(xml.Header)
		if err := xml.NewEncoder(&buf).Encode(resp); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
