package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // G501: the OpenSubsonic token is md5(password+salt) by protocol definition
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"uuid"

	"github.com/nicolasacchi/metamusic/internal/reqinfo"
	"github.com/nicolasacchi/metamusic/internal/store"
)

// Subsonic credential constants (C107, D-M4a-9).
const (
	SubsonicAPIKeyPrefix = "mms_"              // + 52 lowercase base32 chars of 32 random bytes
	SaltWindowMedia      = 12 * time.Hour      // stream, download, getCoverArt
	SaltWindowAPI        = 10 * time.Minute    // every other endpoint
	MinSaltLen           = 6                   // printable ASCII, no space
	MaxSaltLen           = 64                  //
	subsonicTouchEvery   = time.Minute         // at most one last_seen write per client per minute
	saltRetention        = 30 * 24 * time.Hour // PruneSalts keeps rows far past every replay window, so an expired salt cannot restart as a first sighting
	subsonicKeyContext   = "metamusic/subsonic-password/v1"
)

// SaltWindowFor returns how long a (client, salt) pair stays valid after its first use on endpoint.
func SaltWindowFor(endpoint string) time.Duration {
	switch endpoint {
	case "stream", "download", "getCoverArt":
		return SaltWindowMedia
	}
	return SaltWindowAPI
}

// SubsonicClient is one native-app credential row; it never carries key material.
type SubsonicClient struct {
	ID, Name, Username, DefaultFormat, APIKeyPrefix, CreatedBy, LastClient, RevokedBy string
	Scopes                                                                            []Scope
	HasAPIKey, HasPassword                                                            bool
	CreatedAt                                                                         time.Time
	LastSeenAt, RevokedAt                                                             *time.Time
}

// NewSubsonicClient is the input of Create.
type NewSubsonicClient struct {
	Name, Username, DefaultFormat string // Username default: a slug of Name; DefaultFormat "raw" (default) | "mp3"
	Write, APIKey, Password       bool   // at least one of APIKey, Password
}

// SubsonicSecrets are the plaintext credentials, returned exactly once by Create.
type SubsonicSecrets struct{ APIKey, Password string }

var (
	ErrSubsonicCredentials = errors.New("subsonic: wrong username or password") // protocol error 40
	ErrSubsonicAPIKey      = errors.New("subsonic: invalid api key")            // protocol error 44
	ErrSaltExpired         = errors.New("subsonic: salt outside its window")    // protocol error 40, own message
	ErrUsernameTaken       = errors.New("subsonic: username in use")            // 409 USERNAME_TAKEN
	ErrSubsonicInput       = errors.New("subsonic: invalid client")             // 422; the wrapped text names the field
)

var (
	subsonicUserRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,31}$`)
	subsonicKeyRE   = regexp.MustCompile(`^mms_[a-z2-7]{52}$`)
	subsonicTokenRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
	subsonicFormats = []string{"raw", "mp3"}
)

// SubsonicStore owns the subsonic_clients and subsonic_salts tables.
type SubsonicStore struct {
	st     *store.Store
	key    []byte // Secrets.Key: HMAC key of API keys
	aead   cipher.AEAD
	now    func() time.Time
	newID  func() string
	rnd    io.Reader
	dummyN []byte // a fixed ciphertext decrypted for unknown users, so timing does not reveal usernames
	dummyC []byte

	decrypts atomic.Int64 // test hook: number of password decryptions attempted
}

// NewSubsonicStore builds the store. secretKey is Secrets.Key; the AES-256-GCM key of app passwords is
// HMAC-SHA256(secretKey, "metamusic/subsonic-password/v1"). A nil now, newID or rnd means time.Now, a
// UUIDv7 and crypto/rand.
func NewSubsonicStore(st *store.Store, secretKey []byte, now func() time.Time, newID func() string, rnd io.Reader) *SubsonicStore {
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = func() string { return uuid.NewV7().String() }
	}
	if rnd == nil {
		rnd = rand.Reader
	}
	m := hmac.New(sha256.New, secretKey)
	m.Write([]byte(subsonicKeyContext))
	block, err := aes.NewCipher(m.Sum(nil))
	if err != nil {
		panic(err) // a 32-byte key is always valid
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	s := &SubsonicStore{st: st, key: secretKey, aead: aead, now: now, newID: newID, rnd: rnd}
	s.dummyN = make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rnd, s.dummyN); err != nil {
		panic(err)
	}
	s.dummyC = aead.Seal(nil, s.dummyN, []byte("dummy-password-0123456789"), s.aad("dummy"))
	return s
}

func (s *SubsonicStore) aad(id string) []byte { return []byte("subsonic-password\n" + id) }

func (s *SubsonicStore) apiKeyHMAC(k string) string { return mac(s.key, "subsonic\n"+k) }

// randBase32 returns n lowercase base32 characters from 5n/8 random bytes (n must be a multiple of 8).
func (s *SubsonicStore) randBase32(n int) (string, error) {
	raw := make([]byte, n*5/8)
	if _, err := io.ReadFull(s.rnd, raw); err != nil {
		return "", fmt.Errorf("subsonic: random: %w", err)
	}
	return strings.ToLower(b32.EncodeToString(raw)), nil
}

func subsonicInput(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrSubsonicInput, fmt.Sprintf(format, a...))
}

// slugUsername derives a default username from a client name.
func slugUsername(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '_' || r == '-':
			if b.Len() > 0 {
				b.WriteRune(r)
			}
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 32 {
		out = strings.TrimRight(out[:32], "-._")
	}
	for len(out) < 3 {
		if out == "" {
			out = "client"
		} else {
			out += "-app"
		}
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}

// Create stores a new client and returns its plaintext credentials once. by holds scope admin (the gate
// enforces it before this is called).
func (s *SubsonicStore) Create(ctx context.Context, by Principal, in NewSubsonicClient) (SubsonicClient, SubsonicSecrets, error) {
	name := strings.TrimSpace(in.Name)
	if n := len([]rune(name)); n < 1 || n > 80 {
		return SubsonicClient{}, SubsonicSecrets{}, subsonicInput("name must be 1 to 80 characters")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return SubsonicClient{}, SubsonicSecrets{}, subsonicInput("name must not contain control characters")
		}
	}
	if !in.APIKey && !in.Password {
		return SubsonicClient{}, SubsonicSecrets{}, subsonicInput("choose an API key, an app password or both")
	}
	format := in.DefaultFormat
	if format == "" {
		format = "raw"
	}
	if !slices.Contains(subsonicFormats, format) {
		return SubsonicClient{}, SubsonicSecrets{}, subsonicInput("default_format must be raw or mp3")
	}
	user := in.Username
	if user == "" {
		user = slugUsername(name)
	}
	if !subsonicUserRE.MatchString(user) {
		return SubsonicClient{}, SubsonicSecrets{}, subsonicInput("username must match ^[a-z0-9][a-z0-9._-]{2,31}$")
	}
	scopes := "read"
	if in.Write {
		scopes = "read,write"
	}
	id := s.newID()
	var sec SubsonicSecrets
	var prefix, keyMAC any
	var nonce, ct any
	if in.APIKey {
		b, err := s.randBase32(52) // 32 random bytes
		if err != nil {
			return SubsonicClient{}, SubsonicSecrets{}, err
		}
		sec.APIKey = SubsonicAPIKeyPrefix + b
		prefix, keyMAC = b[:8], s.apiKeyHMAC(sec.APIKey)
	}
	if in.Password {
		pw, err := s.randBase32(24)
		if err != nil {
			return SubsonicClient{}, SubsonicSecrets{}, err
		}
		n := make([]byte, s.aead.NonceSize())
		if _, err := io.ReadFull(s.rnd, n); err != nil {
			return SubsonicClient{}, SubsonicSecrets{}, fmt.Errorf("subsonic: random: %w", err)
		}
		sec.Password = pw
		nonce, ct = n, s.aead.Seal(nil, n, []byte(pw), s.aad(id))
	}
	_, err := s.st.W.ExecContext(ctx, `INSERT INTO subsonic_clients(id, name, username, scopes, default_format, api_key_prefix, api_key_hmac,
		password_nonce, password_ct, created_by, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		id, name, user, scopes, format, prefix, keyMAC, nonce, ct, by.Key(), store.FormatTime(s.now()))
	if err != nil {
		if strings.Contains(err.Error(), "subsonic_clients.username") {
			return SubsonicClient{}, SubsonicSecrets{}, ErrUsernameTaken
		}
		return SubsonicClient{}, SubsonicSecrets{}, fmt.Errorf("subsonic: create client: %w", err)
	}
	c, err := s.get(ctx, id)
	return c, sec, err
}

const subsonicCols = `id, name, username, scopes, default_format, api_key_prefix, api_key_hmac IS NOT NULL, password_ct IS NOT NULL,
	created_by, created_at, last_seen_at, last_client, revoked_at, revoked_by`

func scanSubsonic(r rowScanner) (SubsonicClient, error) {
	var c SubsonicClient
	var scopes, created string
	var prefix, lastSeen, lastClient, revoked, revokedBy sql.NullString
	if err := r.Scan(&c.ID, &c.Name, &c.Username, &scopes, &c.DefaultFormat, &prefix, &c.HasAPIKey, &c.HasPassword,
		&c.CreatedBy, &created, &lastSeen, &lastClient, &revoked, &revokedBy); err != nil {
		return SubsonicClient{}, err
	}
	c.Scopes, c.APIKeyPrefix, c.LastClient, c.RevokedBy = splitScopes(scopes), prefix.String, lastClient.String, revokedBy.String
	c.CreatedAt, _ = store.ParseTime(created)
	if lastSeen.Valid {
		t, _ := store.ParseTime(lastSeen.String)
		c.LastSeenAt = &t
	}
	if revoked.Valid {
		t, _ := store.ParseTime(revoked.String)
		c.RevokedAt = &t
	}
	return c, nil
}

func (s *SubsonicStore) get(ctx context.Context, id string) (SubsonicClient, error) {
	c, err := scanSubsonic(s.st.W.QueryRowContext(ctx, `SELECT `+subsonicCols+` FROM subsonic_clients WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return SubsonicClient{}, ErrNotFound
	}
	return c, err
}

// List returns every client, oldest first; it never returns key material.
func (s *SubsonicStore) List(ctx context.Context) ([]SubsonicClient, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT `+subsonicCols+` FROM subsonic_clients ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SubsonicClient{}
	for rows.Next() {
		c, err := scanSubsonic(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Revoke retires a client (idempotent); ErrNotFound for an unknown id.
func (s *SubsonicStore) Revoke(ctx context.Context, by Principal, id string) (SubsonicClient, error) {
	if _, err := s.st.W.ExecContext(ctx, `UPDATE subsonic_clients SET revoked_at = ?, revoked_by = ? WHERE id = ? AND revoked_at IS NULL`,
		store.FormatTime(s.now()), by.Key(), id); err != nil {
		return SubsonicClient{}, fmt.Errorf("subsonic: revoke client: %w", err)
	}
	return s.get(ctx, id)
}

type liveClient struct {
	id, name, scopes string
	nonce, ct        []byte
}

func (c liveClient) principal(clientParam string) Principal {
	return Principal{Kind: KindDevice, ID: c.id, Name: c.name, Scopes: splitScopes(c.scopes), Via: "subsonic",
		Client: reqinfo.SanitizeClient(clientParam)}
}

// VerifyAPIKey resolves an `apiKey` parameter to its client's principal.
func (s *SubsonicStore) VerifyAPIKey(ctx context.Context, key, clientParam string) (Principal, error) {
	if !subsonicKeyRE.MatchString(key) {
		return Principal{}, ErrSubsonicAPIKey
	}
	var c liveClient
	err := s.st.R.QueryRowContext(ctx, `SELECT id, name, scopes FROM subsonic_clients WHERE api_key_hmac = ? AND revoked_at IS NULL`,
		s.apiKeyHMAC(key)).Scan(&c.id, &c.name, &c.scopes)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrSubsonicAPIKey
	}
	if err != nil {
		return Principal{}, fmt.Errorf("subsonic: verify api key: %w", err)
	}
	p := c.principal(clientParam)
	s.touch(ctx, c.id, p.Client)
	return p, nil
}

func saltValid(salt string) bool {
	if len(salt) < MinSaltLen || len(salt) > MaxSaltLen {
		return false
	}
	for i := 0; i < len(salt); i++ {
		if salt[i] <= 0x20 || salt[i] >= 0x7f {
			return false
		}
	}
	return true
}

// VerifyToken checks token = hex(md5(password + salt)) for username. window is SaltWindowFor(<endpoint of
// this request>): a (client, salt) pair is accepted until window after its first use.
func (s *SubsonicStore) VerifyToken(ctx context.Context, username, token, salt, clientParam string, window time.Duration) (Principal, error) {
	if !subsonicTokenRE.MatchString(token) || !saltValid(salt) {
		return Principal{}, ErrSubsonicCredentials
	}
	var c liveClient
	err := s.st.R.QueryRowContext(ctx, `SELECT id, name, scopes, password_nonce, password_ct FROM subsonic_clients
		WHERE username = ? AND revoked_at IS NULL`, username).Scan(&c.id, &c.name, &c.scopes, &c.nonce, &c.ct)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Principal{}, fmt.Errorf("subsonic: verify token: %w", err)
	}
	known := err == nil && c.ct != nil
	nonce, ct, aad := s.dummyN, s.dummyC, s.aad("dummy")
	if known {
		nonce, ct, aad = c.nonce, c.ct, s.aad(c.id)
	}
	s.decrypts.Add(1)
	pw, oerr := s.aead.Open(nil, nonce, ct, aad)
	if oerr != nil {
		pw = []byte("unreadable")
		known = false
	}
	sum := md5.Sum(append(pw, salt...)) //nolint:gosec // G401: protocol-defined
	want := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(want), []byte(token)) != 1 || !known {
		return Principal{}, ErrSubsonicCredentials
	}
	if err := s.checkSalt(ctx, c.id, salt, window); err != nil {
		return Principal{}, err
	}
	p := c.principal(clientParam)
	s.touch(ctx, c.id, p.Client)
	return p, nil
}

// checkSalt records the sighting of a verified salt and refuses it past window since its first use.
func (s *SubsonicStore) checkSalt(ctx context.Context, clientID, salt string, window time.Duration) error {
	h := sha256.Sum256([]byte(salt))
	hash := hex.EncodeToString(h[:])
	now := s.now()
	if _, err := s.st.W.ExecContext(ctx, `INSERT INTO subsonic_salts(client_id, salt_sha256, first_seen_at) VALUES (?,?,?) ON CONFLICT DO NOTHING`,
		clientID, hash, store.FormatTime(now)); err != nil {
		return fmt.Errorf("subsonic: record salt: %w", err)
	}
	var first string
	if err := s.st.W.QueryRowContext(ctx, `SELECT first_seen_at FROM subsonic_salts WHERE client_id = ? AND salt_sha256 = ?`,
		clientID, hash).Scan(&first); err != nil {
		return fmt.Errorf("subsonic: read salt: %w", err)
	}
	t, err := store.ParseTime(first)
	if err != nil {
		return fmt.Errorf("subsonic: salt time: %w", err)
	}
	if now.Sub(t) > window {
		return ErrSaltExpired
	}
	return nil
}

// touch records last_seen_at and the client label at most once a minute (or when the label changes).
// Best effort: a failure never fails the request.
func (s *SubsonicStore) touch(ctx context.Context, id, client string) {
	now := s.now()
	var lc any
	if client != "" {
		lc = client
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 250*time.Millisecond)
	defer cancel()
	_, _ = s.st.W.ExecContext(wctx, `UPDATE subsonic_clients SET last_seen_at = ?, last_client = ? WHERE id = ?
		AND (last_seen_at IS NULL OR last_seen_at < ? OR last_client IS NOT ?)`,
		store.FormatTime(now), lc, id, store.FormatTime(now.Add(-subsonicTouchEvery)), lc)
}

// PruneSalts deletes salt sightings older than saltRetention (30 days, cache table). The retention must stay
// well above SaltWindowMedia: a pruned salt would count as a first sighting and be accepted for another window.
func (s *SubsonicStore) PruneSalts(ctx context.Context) (int, error) {
	res, err := s.st.W.ExecContext(ctx, `DELETE FROM subsonic_salts WHERE first_seen_at < ?`,
		store.FormatTime(s.now().Add(-saltRetention)))
	if err != nil {
		return 0, fmt.Errorf("subsonic: prune salts: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
