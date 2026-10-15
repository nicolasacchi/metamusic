package subsonic

import (
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// maxFormBytes bounds a formPost body (C108.1).
const maxFormBytes = 64 << 10

var (
	endpointRE = regexp.MustCompile(`^[A-Za-z0-9]+$`)
	versionRE  = regexp.MustCompile(`^([0-9]+)\.[0-9]+(\.[0-9]+)?$`)
)

// endpointName extracts <name> from /rest/<name> or /rest/<name>.view; ok is false for anything else.
func endpointName(path string) (string, bool) {
	name, found := strings.CutPrefix(path, "/rest/")
	if !found {
		return "", false
	}
	name = strings.TrimSuffix(name, ".view")
	return name, endpointRE.MatchString(name)
}

// parseParams returns the query values followed by the form values of a POST (C108.1). A POST whose body is not
// application/x-www-form-urlencoded, or is larger than 64 KiB, is an error 0.
func parseParams(w http.ResponseWriter, r *http.Request) (url.Values, *apiError) {
	params := r.URL.Query()
	if r.Method != http.MethodPost {
		return params, nil
	}
	if r.Body == nil || r.Body == http.NoBody {
		return params, nil
	}
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/x-www-form-urlencoded" {
		if r.ContentLength == 0 {
			return params, nil
		}
		return nil, fail(errGeneric, "unsupported content type")
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFormBytes))
	if err != nil {
		return nil, fail(errGeneric, "request body too large")
	}
	form, err := url.ParseQuery(string(raw))
	if err != nil {
		return nil, fail(errGeneric, "malformed request body")
	}
	for k, vs := range form {
		params[k] = append(params[k], vs...)
	}
	return params, nil
}

// queryFormat reads `f` from the query alone (for errors raised before the body is parsed).
func queryFormat(r *http.Request) format {
	if r.URL.Query().Get("f") == "json" {
		return fmtJSON
	}
	return fmtXML
}

// paramFormat resolves `f` (C108.2): xml (default) | json; anything else, jsonp included, is an error answered in JSON.
func paramFormat(params url.Values) (format, *apiError) {
	switch params.Get("f") {
	case "", "xml":
		return fmtXML, nil
	case "json":
		return fmtJSON, nil
	}
	return fmtJSON, fail(errGeneric, "format not supported")
}

// checkVersion validates `v` (C108.2): X.Y[.Z], a major version above 1 is error 30.
func checkVersion(v string) *apiError {
	m := versionRE.FindStringSubmatch(v)
	if m == nil {
		return fail(errMissingParam, "required parameter is missing: v")
	}
	if major, err := strconv.Atoi(m[1]); err != nil || major > 1 {
		return fail(errServerUpgrade, "incompatible protocol version: the server must be upgraded")
	}
	return nil
}
