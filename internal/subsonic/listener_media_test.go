package subsonic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/library"
	"github.com/nicolasacchi/metamusic/internal/media/clean"
	"github.com/nicolasacchi/metamusic/internal/media/clean/cleantest"
)

// roleVerifier makes the rig's client a Listener's (or Editor's) before lane D1 gives clients an owner: the principal the
// real verifier returns gets the role and user (the R2 pattern: principals placed in the context).
type roleVerifier struct {
	verifier
	role auth.Role
}

func (v roleVerifier) VerifyAPIKey(ctx context.Context, key, client string) (auth.Principal, error) {
	p, err := v.verifier.VerifyAPIKey(ctx, key, client)
	p.Role, p.User = v.role, "carol"
	return p, err
}

type errRenditions struct{ err error }

func (e errRenditions) Ensure(context.Context, string, string) (clean.Rendition, error) {
	return clean.Rendition{}, e.err
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// listenerRig is the five-track library with tainted masters and mp3_320 files, the real rendition service and a Listener's
// client (carol); owner is the unchanged Owner path of the same server (a second server over the same options).
func listenerRig(t *testing.T) (x *s3rig, taints map[string]cleantest.Taint) {
	t.Helper()
	x = newS3Rig(t)
	x.writeFixtureFiles(t)
	taints = cleantest.Rewrite(t, x.st, x.srv.o.Data, twinA, luna, citta)
	x.srv.rends = clean.New(clean.Options{Store: x.st, Data: x.srv.o.Data, Now: x.clk.now})
	return x, taints
}

func TestListenerStreamAndDownloadNeverTheMaster(t *testing.T) {
	t.Parallel()
	x, tn := listenerRig(t)
	body := func(ep string, kv ...string) (*bytes.Buffer, http.Header) {
		t.Helper()
		w := x.raw("GET", ep, x.rwq(kv...), nil)
		statusOK(t, w, 200)
		return w.Body, w.Header()
	}
	var masterFLAC, ownerMP3 *bytes.Buffer
	t.Run("owner unchanged", func(t *testing.T) {
		masterFLAC, _ = body("stream", "id", twinA, "format", "raw")
		if got := sha(masterFLAC.Bytes()); got != tn[twinA].MasterSHA {
			t.Errorf("the Owner's raw stream is not the master: %s", got)
		}
		ownerMP3, _ = body("stream", "id", twinA, "format", "mp3")
		if !bytes.Contains(ownerMP3.Bytes(), []byte(cleantest.Marker)) {
			t.Errorf("the Owner's mp3 stream is not the mp3_320 file as stored")
		}
	})
	x.srv.creds = roleVerifier{x.srv.creds, auth.RoleListener}
	for _, tc := range []struct {
		name, format, ext, ct string
		kv                    []string
	}{
		{"raw", "raw", "flac", "audio/flac", nil}, {"flac", "flac", "flac", "audio/flac", nil}, {"original", "original", "flac", "audio/flac", nil},
		{"mp3", "mp3", "mp3", "audio/mpeg", nil}, {"other format", "opus", "mp3", "audio/mpeg", nil},
		{"max bit rate", "", "mp3", "audio/mpeg", []string{"maxBitRate", "192"}},
	} {
		t.Run("stream "+tc.name, func(t *testing.T) {
			kv := append([]string{"id", twinA}, tc.kv...)
			if tc.format != "" {
				kv = append(kv, "format", tc.format)
			}
			b, h := body("stream", kv...)
			if !strings.HasPrefix(h.Get("Content-Type"), tc.ct) {
				t.Errorf("content type %q", h.Get("Content-Type"))
			}
			got := b.Bytes()
			if sha(got) == tn[twinA].MasterSHA || bytes.Contains(got, []byte(cleantest.Marker)) || bytes.Contains(got, []byte("MM_")) {
				t.Fatalf("a Listener stream carries the master or hidden tags")
			}
			if tc.ext == "flac" && !bytes.HasSuffix(got, tn[twinA].Frames) {
				t.Errorf("flac_clean audio differs from the master's")
			}
			if tc.ext == "mp3" && !bytes.HasSuffix(got, tn[twinA].MP3Frames) {
				t.Errorf("mp3_320_clean audio differs from mp3_320's")
			}
			if err := clean.Check(map[string]string{"flac": clean.KindFLAC, "mp3": clean.KindMP3}[tc.ext], bytes.NewReader(got), int64(len(got))); err != nil {
				t.Errorf("served file fails the allow-list: %v", err)
			}
		})
	}
	t.Run("download", func(t *testing.T) {
		b, h := body("download", "id", twinA)
		cd := h.Get("Content-Disposition")
		if !strings.Contains(cd, "attachment") || strings.Contains(cd, "__") || strings.Contains(cd, "104") || !strings.Contains(cd, ".flac") {
			t.Errorf("disposition %q", cd)
		}
		if sha(b.Bytes()) == tn[twinA].MasterSHA || bytes.Contains(b.Bytes(), []byte(cleantest.Marker)) {
			t.Errorf("a Listener download carries the master")
		}
	})
	t.Run("download of mp3 default client", func(t *testing.T) {
		_, h := body("download", "id", twinA, "format", "mp3")
		if cd := h.Get("Content-Disposition"); !strings.Contains(cd, ".mp3") {
			t.Errorf("disposition %q", cd)
		}
	})
}

func TestListenerAudioFailsClosed(t *testing.T) {
	t.Parallel()
	x, _ := listenerRig(t)
	x.srv.creds = roleVerifier{x.srv.creds, auth.RoleListener}
	code := func(ep string, kv ...string) int { return x.get(ep, x.rwq(kv...), libIP).code(t) }
	// a discarded take is not found, on both endpoints (visibility, as for every other path)
	x.setStatus(twinB, "discarded", x.clk.now())
	for _, ep := range []string{"stream", "download"} {
		if c := code(ep, "id", twinB); c != 70 {
			t.Errorf("%s of a discarded take: code %d", ep, c)
		}
		if c := code(ep, "id", "tr-nope"); c != 70 {
			t.Errorf("%s of a bad id: code %d", ep, c)
		}
	}
	// no rendition service: the master is never the fallback
	x.srv.rends = nil
	for _, ep := range []string{"stream", "download"} {
		if c := code(ep, "id", twinA, "format", "raw"); c != 0 {
			t.Errorf("%s without a rendition service: code %d", ep, c)
		}
	}
	// an unbuildable rendition and a busy builder
	x.srv.rends = errRenditions{&clean.ErrUnavailable{Err: errors.New("x")}}
	if c := code("stream", "id", twinA); c != 0 {
		t.Errorf("unavailable: code %d", c)
	}
	x.srv.rends = errRenditions{library.ErrMediaUnavailable}
	if c := code("stream", "id", twinA); c != 70 {
		t.Errorf("media unavailable: code %d", c)
	}
	x.srv.rends = errRenditions{&clean.ErrBusy{RetryAfter: 7 * time.Second}}
	w := x.raw("GET", "stream", x.rwq("id", twinA), nil)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "7" {
		t.Errorf("busy: %d retry-after %q", w.Code, w.Header().Get("Retry-After"))
	}
}
