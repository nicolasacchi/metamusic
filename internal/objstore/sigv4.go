package objstore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	algorithm       = "AWS4-HMAC-SHA256"
	unsignedPayload = "UNSIGNED-PAYLOAD"
	timeFormat      = "20060102T150405Z"
	dateFormat      = "20060102"

	// MinTTL and MaxTTL bound a presigned URL (SigV4 allows 1 s to 7 days).
	MinTTL = time.Second
	MaxTTL = 7 * 24 * time.Hour
)

// SignQuery turns r into a presigned request (query authentication): X-Amz-* parameters are added to r.URL and
// X-Amz-Signature is appended. Only the host header is signed and the payload is UNSIGNED-PAYLOAD, so any body can be
// sent by whoever holds the URL. It is pure: the only inputs are r.Method, r.URL, c.Region and the credentials.
func SignQuery(r *http.Request, c Config, t time.Time, ttl time.Duration) error {
	if ttl < MinTTL || ttl > MaxTTL {
		return errors.New("objstore: presign ttl must be between 1 second and 7 days")
	}
	t = t.UTC()
	scope := t.Format(dateFormat) + "/" + c.Region + "/s3/aws4_request"
	q := r.URL.Query()
	q.Set("X-Amz-Algorithm", algorithm)
	q.Set("X-Amz-Credential", c.AccessKeyID+"/"+scope)
	q.Set("X-Amz-Date", t.Format(timeFormat))
	q.Set("X-Amz-Expires", strconv.FormatInt(int64(ttl/time.Second), 10))
	q.Set("X-Amz-SignedHeaders", "host")
	q.Del("X-Amz-Signature")
	host := hostOf(r)
	cr := canonicalRequest(r.Method, r.URL.Path, canonicalQuery(q), "host:"+host+"\n", "host", unsignedPayload)
	sig := signature(c, t, scope, cr)
	r.URL.RawQuery = canonicalQuery(q) + "&X-Amz-Signature=" + sig
	return nil
}

// SignHeader signs r with an Authorization header. It sets x-amz-date and x-amz-content-sha256 (payloadSHA, or
// UNSIGNED-PAYLOAD when empty) and signs host, range, content-md5, content-type and every x-amz-* header present.
func SignHeader(r *http.Request, c Config, t time.Time, payloadSHA string) error {
	if payloadSHA == "" {
		payloadSHA = unsignedPayload
	}
	t = t.UTC()
	scope := t.Format(dateFormat) + "/" + c.Region + "/s3/aws4_request"
	r.Header.Set("X-Amz-Date", t.Format(timeFormat))
	r.Header.Set("X-Amz-Content-Sha256", payloadSHA)
	r.Header.Del("Authorization")

	hdr := map[string]string{"host": hostOf(r)}
	for k, v := range r.Header {
		lk := strings.ToLower(k)
		if lk == "range" || lk == "content-md5" || lk == "content-type" || strings.HasPrefix(lk, "x-amz-") {
			hdr[lk] = strings.Join(trimAll(v), ",")
		}
	}
	names := make([]string, 0, len(hdr))
	for k := range hdr {
		names = append(names, k)
	}
	sort.Strings(names)
	var canon strings.Builder
	for _, k := range names {
		canon.WriteString(k + ":" + hdr[k] + "\n")
	}
	signed := strings.Join(names, ";")
	cr := canonicalRequest(r.Method, r.URL.Path, canonicalQuery(r.URL.Query()), canon.String(), signed, payloadSHA)
	sig := signature(c, t, scope, cr)
	r.Header.Set("Authorization", algorithm+" Credential="+c.AccessKeyID+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
	return nil
}

func hostOf(r *http.Request) string {
	if r.URL.Host != "" {
		return r.URL.Host
	}
	return r.Host
}

func trimAll(v []string) []string {
	out := make([]string, len(v))
	for i, s := range v {
		out[i] = strings.Join(strings.Fields(s), " ")
	}
	return out
}

func canonicalRequest(method, path, query, headers, signedHeaders, payload string) string {
	if path == "" {
		path = "/"
	}
	return method + "\n" + uriEncode(path, false) + "\n" + query + "\n" + headers + "\n" + signedHeaders + "\n" + payload
}

func canonicalQuery(q url.Values) string {
	type kv struct{ k, v string }
	var all []kv
	for k, vs := range q {
		for _, v := range vs {
			all = append(all, kv{uriEncode(k, true), uriEncode(v, true)})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].k != all[j].k {
			return all[i].k < all[j].k
		}
		return all[i].v < all[j].v
	})
	parts := make([]string, len(all))
	for i, p := range all {
		parts[i] = p.k + "=" + p.v
	}
	return strings.Join(parts, "&")
}

// uriEncode is the SigV4 encoding: everything but A-Z a-z 0-9 - _ . ~ is percent-encoded (upper-case hex); '/' stays
// unless encodeSlash.
func uriEncode(s string, encodeSlash bool) string {
	const hexd = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteByte(ch)
		case ch == '/' && !encodeSlash:
			b.WriteByte(ch)
		default:
			b.WriteByte('%')
			b.WriteByte(hexd[ch>>4])
			b.WriteByte(hexd[ch&15])
		}
	}
	return b.String()
}

func signature(c Config, t time.Time, scope, canonReq string) string {
	h := sha256.Sum256([]byte(canonReq))
	sts := algorithm + "\n" + t.Format(timeFormat) + "\n" + scope + "\n" + hex.EncodeToString(h[:])
	k := hmacSHA([]byte("AWS4"+c.SecretAccessKey), t.Format(dateFormat))
	k = hmacSHA(k, c.Region)
	k = hmacSHA(k, "s3")
	k = hmacSHA(k, "aws4_request")
	return hex.EncodeToString(hmacSHA(k, sts))
}

func hmacSHA(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}
