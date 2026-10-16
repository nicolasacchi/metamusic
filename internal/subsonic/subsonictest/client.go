package subsonictest

import (
	"bytes"
	"crypto/md5" //nolint:gosec // G501: the OpenSubsonic token is md5(password+salt) by protocol definition
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

var credKeys = []string{"u", "p", "t", "s", "apiKey"}

// Call sends one request and parses the answer (JSON or XML). Credentials: with CallOpts.APIKey the client's API key
// is added unless the parameters already hold an apiKey; otherwise, unless the parameters already carry any of
// u, p, t, s or apiKey (the caller then controls the credentials), a token (u, t, s) is added from the app password,
// or the API key when the client has no password. A client without credentials sends none. c and v default to
// "subsonictest" and "1.16.1"; f=json is added with CallOpts.JSON.
func (c *Client) Call(tb testing.TB, endpoint string, params url.Values, o CallOpts) *Response {
	tb.Helper()
	q := url.Values{}
	for k, vs := range params {
		q[k] = append([]string(nil), vs...)
	}
	setDefault := func(k, v string) {
		if !q.Has(k) {
			q.Set(k, v)
		}
	}
	setDefault("c", "subsonictest")
	setDefault("v", "1.16.1")
	if o.JSON {
		setDefault("f", "json")
	}
	hasCred := false
	for _, k := range credKeys {
		hasCred = hasCred || q.Has(k)
	}
	switch {
	case o.APIKey:
		if !q.Has("apiKey") && c.APIKey != "" {
			q.Set("apiKey", c.APIKey)
		}
	case hasCred:
	case c.Password != "":
		salt := o.Salt
		if salt == "" {
			salt = randomSalt(tb)
		}
		sum := md5.Sum([]byte(c.Password + salt)) //nolint:gosec // protocol-mandated
		q.Set("u", c.Username)
		q.Set("t", hex.EncodeToString(sum[:]))
		q.Set("s", salt)
	case c.APIKey != "":
		q.Set("apiKey", c.APIKey)
	}
	path := "/rest/" + endpoint
	if o.View {
		path += ".view"
	}
	method := http.MethodGet
	var body io.Reader
	target := c.BaseURL + path
	if o.POST {
		method = http.MethodPost
		body = strings.NewReader(q.Encode())
	} else {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		tb.Fatal(err)
	}
	if o.POST {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if o.Range != "" {
		req.Header.Set("Range", o.Range)
	}
	if o.UserAgent != "" {
		req.Header.Set("User-Agent", o.UserAgent)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := hc.Do(req)
	if err != nil {
		tb.Fatalf("subsonictest: %s %s: %v", method, endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		tb.Fatalf("subsonictest: reading %s: %v", endpoint, err)
	}
	return parseResponse(resp, raw)
}

func randomSalt(tb testing.TB) string {
	tb.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		tb.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// parseResponse fills Status, Code and Doc from a JSON or XML envelope; a binary answer leaves them empty.
func parseResponse(resp *http.Response, raw []byte) *Response {
	out := &Response{HTTP: resp.StatusCode, Header: resp.Header.Clone(), Body: raw}
	ct := resp.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "application/json"):
		var env map[string]map[string]any
		if json.Unmarshal(raw, &env) == nil {
			out.Doc = env["subsonic-response"]
		}
	case strings.HasPrefix(ct, "text/xml") || strings.HasPrefix(ct, "application/xml"):
		if doc, err := xmlToMap(raw); err == nil {
			out.Doc = doc
		}
	}
	if out.Doc != nil {
		out.Status, _ = out.Doc["status"].(string)
		if e, ok := out.Doc["error"].(map[string]any); ok {
			switch v := e["code"].(type) {
			case float64:
				out.Code = int(v)
			case string:
				out.Code, _ = strconv.Atoi(v)
			}
		}
	}
	return out
}

// xmlToMap decodes an XML document into the shape of its JSON twin as far as tests need it: attributes become
// string entries, child elements become nested maps (repeated children become slices), text becomes "value".
func xmlToMap(raw []byte) (map[string]any, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok {
			return decodeElement(dec, se)
		}
	}
}

func decodeElement(dec *xml.Decoder, se xml.StartElement) (map[string]any, error) {
	m := map[string]any{}
	for _, a := range se.Attr {
		m[a.Name.Local] = a.Value
	}
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			child, err := decodeElement(dec, t)
			if err != nil {
				return nil, err
			}
			switch prev := m[t.Name.Local].(type) {
			case nil:
				m[t.Name.Local] = child
			case []any:
				m[t.Name.Local] = append(prev, child)
			default:
				m[t.Name.Local] = []any{prev, child}
			}
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			if s := strings.TrimSpace(text.String()); s != "" {
				m["value"] = s
			}
			return m, nil
		}
	}
}
