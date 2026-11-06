package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nicolasacchi/metamusic/internal/clock"
	"github.com/nicolasacchi/metamusic/internal/media/serve"
	"github.com/nicolasacchi/metamusic/internal/provider"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Errors of input minting. None of them carries a URL.
var (
	// ErrInputChanged means the source master or the reference clip is no longer the object the request pinned.
	ErrInputChanged = errors.New("jobs: an input audio object changed since the estimate")
	// ErrInputHorizon means the attempt's window ends beyond the longest link the media route accepts.
	ErrInputHorizon = errors.New("jobs: the attempt window outlives the longest media link")
)

// inputRefs is the part of an engine-native request that names input audio (ACE-Step src and ref); the other fields are
// the engine's own business.
type inputRefs struct {
	Src *struct {
		TrackID      string `json:"track_id"`
		MasterSHA256 string `json:"master_sha256"`
	} `json:"src"`
	Ref *struct {
		TrackID    string `json:"track_id"`
		ClipSHA256 string `json:"clip_sha256"`
	} `json:"ref"`
}

func parseInputRefs(request []byte) (inputRefs, bool) {
	var in inputRefs
	if json.Unmarshal(request, &in) != nil {
		return inputRefs{}, false
	}
	return in, in.Src != nil || in.Ref != nil
}

func requestHasInputs(request string) bool {
	_, ok := parseInputRefs([]byte(request))
	return ok
}

// linkBucket is the expiry bucket of an input link; the expiry is rounded up, never down, so the link outlives the window.
const linkBucket = time.Minute

// maxLinkHorizon mirrors the 14 h bound serve.Signer.Verify enforces on an expiry (TestInputMinter_HorizonMatchesVerifier
// keeps the two in step).
const maxLinkHorizon = 14 * time.Hour

// InputMinterOptions builds the signed-URL input minter.
type InputMinterOptions struct {
	Signer    *serve.Signer
	Store     *store.Store
	Data      *os.Root
	Clips     serve.Resolver // resolves a reference clip by its sha256 (the clip service); nil refuses reference inputs
	PublicURL string
	Clock     clock.Clock
}

type signedInputs struct{ o InputMinterOptions }

// NewInputMinter returns the InputMinter over the media signer (C153.3, D-E2-9). A source is the frozen master of the
// request's src track, a reference the cached 30 s clip addressed by ref.clip_sha256; both are served by the signed
// /media/ route. Every link expires at or after expires, rounded up to the next minute.
func NewInputMinter(o InputMinterOptions) InputMinter {
	if o.Clock == nil {
		o.Clock = clock.Real
	}
	return &signedInputs{o: o}
}

// MintInputs implements InputMinter. A request with neither src nor ref yields a nil map.
func (m *signedInputs) MintInputs(ctx context.Context, request json.RawMessage, expires time.Time) (map[string]provider.InputRef, error) {
	refs, ok := parseInputRefs(request)
	if !ok {
		return nil, nil
	}
	if m.o.Signer == nil || m.o.Store == nil || m.o.PublicURL == "" {
		return nil, errors.New("jobs: input minter is not wired")
	}
	ttl := expires.Sub(m.o.Clock.Now())
	if ttl < linkBucket {
		ttl = linkBucket
	}
	out := map[string]provider.InputRef{}
	if refs.Src != nil {
		var sha string
		var bytes int64
		err := m.o.Store.R.QueryRowContext(ctx, `SELECT master_sha256, master_bytes FROM tracks WHERE id = ? AND lossy = 0`, refs.Src.TrackID).Scan(&sha, &bytes)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, fmt.Errorf("%w: source track %s has no lossless master", ErrInputChanged, refs.Src.TrackID)
		case err != nil:
			return nil, fmt.Errorf("jobs: read source master: %w", err)
		case sha != refs.Src.MasterSHA256:
			return nil, fmt.Errorf("%w: source track %s", ErrInputChanged, refs.Src.TrackID)
		}
		ref, err := m.sign(sha, bytes, ttl)
		if err != nil {
			return nil, err
		}
		out[provider.InputSrcAudio] = ref
	}
	if refs.Ref != nil {
		if m.o.Clips == nil || m.o.Data == nil {
			return nil, errors.New("jobs: no reference clip resolver is wired")
		}
		obj, err := m.o.Clips.Resolve(ctx, refs.Ref.ClipSHA256, "flac")
		if errors.Is(err, serve.ErrNotFound) {
			return nil, fmt.Errorf("%w: reference clip of track %s", ErrInputChanged, refs.Ref.TrackID)
		}
		if err != nil {
			return nil, fmt.Errorf("jobs: resolve reference clip: %w", err)
		}
		st, err := m.o.Data.Stat(obj.RelPath)
		if err != nil || !st.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: reference clip of track %s is not on disk", ErrInputChanged, refs.Ref.TrackID)
		}
		ref, err := m.sign(refs.Ref.ClipSHA256, st.Size(), ttl)
		if err != nil {
			return nil, err
		}
		out[provider.InputReferenceAudio] = ref
	}
	return out, nil
}

// sign builds the absolute link of a flac object. The signer refuses nothing itself, so the horizon is checked here against
// what its verifier accepts (14 h ahead).
func (m *signedInputs) sign(sha string, bytes int64, ttl time.Duration) (provider.InputRef, error) {
	if ttl+linkBucket > maxLinkHorizon {
		return provider.InputRef{}, ErrInputHorizon
	}
	l := m.o.Signer.Sign(sha, "flac", ttl, linkBucket)
	return provider.InputRef{URL: strings.TrimRight(m.o.PublicURL, "/") + l.URL, SHA256: sha, Bytes: bytes}, nil
}
