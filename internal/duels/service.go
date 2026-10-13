package duels

import (
	"crypto/rand"
	"fmt"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/problem"
)

const (
	defaultExpiry = 30 * 24 * time.Hour
	maxNoteRunes  = 2000
	maxExclude    = 50
	defaultLimit  = 50
	maxLimit      = 100
	ladderLimit   = 200
	maxLadder     = 500
	cacheEntries  = 32
)

// Service is the duels service (C48). It holds no per-request state; the only
// shared mutable state is the ladder fit cache.
type Service struct {
	o      Options
	now    func() time.Time
	newID  func() string
	coin   func() bool
	expiry time.Duration
	cache  *fitCache
}

// New builds the service, filling the defaults documented on Options.
func New(o Options) *Service {
	s := &Service{o: o, now: o.Now, newID: o.NewID, coin: o.Rand, expiry: o.Expiry, cache: newFitCache(cacheEntries)}
	if s.now == nil {
		s.now = time.Now
	}
	if s.newID == nil {
		s.newID = func() string { return uuid.NewV7().String() }
	}
	if s.coin == nil {
		s.coin = cryptoCoin
	}
	if s.expiry <= 0 {
		s.expiry = defaultExpiry
	}
	return s
}

// cryptoCoin is the production slot coin: one bit from crypto/rand.
func cryptoCoin() bool {
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("duels: crypto/rand: %v", err)) // the stdlib reader does not fail on supported platforms
	}
	return b[0]&1 == 1
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func unavailable(component string) error {
	return problem.E(503, "UNAVAILABLE", component+" is not available", map[string]any{"component": component})
}

func notFound(what, id string) error {
	return problem.E(404, "NOT_FOUND", what+" not found", map[string]any{what + "_id": id})
}

func invalid(field, msg string) error {
	return problem.E(422, "VALIDATION_FAILED", msg, map[string]any{"field": field})
}

// canJudge rejects principals that can never own a duel or a verdict.
func canJudge(p auth.Principal) (string, error) {
	src, err := SourceFor(p)
	if err != nil {
		return "", fmt.Errorf("duels: %s: %w", p.Kind, err)
	}
	return src, nil
}

// unconfigured is true for a Service built without its store or library (the zero
// Options): every method then answers errors.ErrUnsupported (501 at the API).
func (s *Service) unconfigured() bool { return s.o.Store == nil || s.o.Library == nil }
