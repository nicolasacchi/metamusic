-- 0008_remote.sql — M4a: connector grants, Subsonic clients, salts, bookmarks, play queue,
-- the offline-set playlist, status timestamps.

-- Status timestamp (Subsonic `starred`, D-M4a-23). Mutable; set with every status change.
ALTER TABLE tracks ADD COLUMN status_set_at TEXT;
UPDATE tracks SET status_set_at = created_at;           -- names no frozen column (M1/M3 triggers)

CREATE TABLE connector_grants (                         -- D-M4a-1; one row per (OAuth client, subject)
  id                     TEXT PRIMARY KEY,              -- UUIDv7 = Principal.ID; Principal.Key() = 'agent:<id>'
  client_id              TEXT NOT NULL,                 -- OAuth client_id (allow-listed by MM_OIDC_CLIENTS)
  subject                TEXT NOT NULL CHECK (length(subject) BETWEEN 1 AND 255),   -- token sub (provider-opaque)
  username               TEXT NOT NULL CHECK (length(username) BETWEEN 1 AND 255),  -- mm_user at creation (human-only display)
  label                  TEXT NOT NULL CHECK (length(label) BETWEEN 1 AND 80),      -- client label at creation
  scopes                 TEXT NOT NULL CHECK (scopes IN ('read','read,write','read,generate','read,write,generate')),
  daily_budget_micro_usd INTEGER CHECK (daily_budget_micro_usd IS NULL OR daily_budget_micro_usd BETWEEN 0 AND 1000000000),
  auto_approve_takes     INTEGER CHECK (auto_approve_takes IS NULL OR auto_approve_takes BETWEEN 0 AND 100),
  auto_approve_micro_usd INTEGER CHECK (auto_approve_micro_usd IS NULL OR auto_approve_micro_usd BETWEEN 0 AND 1000000000),
  created_at             TEXT NOT NULL,
  last_used_at           TEXT,
  updated_at             TEXT NOT NULL,
  updated_by             TEXT NOT NULL,                 -- Principal.Key(); 'system:oauth' at creation
  revoked_at             TEXT,
  revoked_by             TEXT,
  UNIQUE (client_id, subject),
  CHECK ((revoked_at IS NULL) = (revoked_by IS NULL))
);
CREATE TRIGGER connector_grants_no_delete BEFORE DELETE ON connector_grants
BEGIN SELECT RAISE(ABORT, 'connector_grants: no physical delete'); END;
CREATE TRIGGER connector_grants_frozen BEFORE UPDATE OF id, client_id, subject, username, label, created_at ON connector_grants
BEGIN SELECT RAISE(ABORT, 'connector_grants: frozen column'); END;

CREATE TABLE subsonic_clients (                         -- D-M4a-8; one row per native app
  id             TEXT PRIMARY KEY,                      -- UUIDv7 = Principal.ID (Kind device, Via subsonic)
  name           TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
  username       TEXT NOT NULL CHECK (length(username) BETWEEN 3 AND 32),   -- Go: ^[a-z0-9][a-z0-9._-]{2,31}$
  scopes         TEXT NOT NULL CHECK (scopes IN ('read','read,write')),     -- never generate or admin
  default_format TEXT NOT NULL,                         -- Go-validated: 'raw' | 'mp3'
  api_key_prefix TEXT,                                  -- first 8 chars after 'mms_'
  api_key_hmac   TEXT UNIQUE,                           -- hex HMAC (C102); NULL when the client has no API key
  password_nonce BLOB,                                  -- AES-256-GCM nonce (12 bytes)
  password_ct    BLOB,                                  -- ciphertext + tag; AAD = 'subsonic-password\n' || id
  created_by     TEXT NOT NULL,
  created_at     TEXT NOT NULL,
  last_seen_at   TEXT,
  last_client    TEXT CHECK (last_client IS NULL OR length(last_client) <= 120),  -- sanitised `c` parameter
  revoked_at     TEXT,
  revoked_by     TEXT,
  CHECK ((api_key_hmac IS NULL) = (api_key_prefix IS NULL)),
  CHECK ((password_nonce IS NULL) = (password_ct IS NULL)),
  CHECK (api_key_hmac IS NOT NULL OR password_ct IS NOT NULL),
  CHECK ((revoked_at IS NULL) = (revoked_by IS NULL))
);
CREATE UNIQUE INDEX subsonic_clients_username_live ON subsonic_clients(username) WHERE revoked_at IS NULL;
CREATE TRIGGER subsonic_clients_no_delete BEFORE DELETE ON subsonic_clients
BEGIN SELECT RAISE(ABORT, 'subsonic_clients: no physical delete'); END;
CREATE TRIGGER subsonic_clients_frozen BEFORE UPDATE OF
  id, name, username, scopes, default_format, api_key_prefix, api_key_hmac, password_nonce, password_ct,
  created_by, created_at ON subsonic_clients
BEGIN SELECT RAISE(ABORT, 'subsonic_clients: frozen column'); END;

CREATE TABLE subsonic_salts (                           -- cache table (D-M4a-9): pruned, physical DELETE allowed
  client_id     TEXT NOT NULL,
  salt_sha256   TEXT NOT NULL,
  first_seen_at TEXT NOT NULL,                          -- store.FormatTime (SQL-compared)
  PRIMARY KEY (client_id, salt_sha256)
) WITHOUT ROWID;
CREATE INDEX subsonic_salts_seen ON subsonic_salts(first_seen_at);

CREATE TABLE subsonic_bookmarks (                       -- one resume position per track (one human)
  track_id    TEXT PRIMARY KEY REFERENCES tracks(id),
  position_ms INTEGER NOT NULL CHECK (position_ms >= 0),
  comment     TEXT CHECK (comment IS NULL OR length(comment) BETWEEN 1 AND 4000),
  comment_id  TEXT REFERENCES comments(id),             -- the comment the last non-empty comment created (D-M4a-12)
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  updated_by  TEXT NOT NULL,
  deleted_at  TEXT
) WITHOUT ROWID;
CREATE TRIGGER subsonic_bookmarks_no_delete BEFORE DELETE ON subsonic_bookmarks
BEGIN SELECT RAISE(ABORT, 'subsonic_bookmarks: no physical delete'); END;
CREATE TRIGGER subsonic_bookmarks_frozen BEFORE UPDATE OF track_id, created_at ON subsonic_bookmarks
BEGIN SELECT RAISE(ABORT, 'subsonic_bookmarks: frozen column'); END;

CREATE TABLE subsonic_play_queue (                      -- singleton
  id               INTEGER PRIMARY KEY CHECK (id = 1),
  track_ids_json   TEXT NOT NULL CHECK (json_valid(track_ids_json) AND json_type(track_ids_json) = 'array'),
  current_track_id TEXT,
  position_ms      INTEGER NOT NULL CHECK (position_ms >= 0),
  changed_at       TEXT NOT NULL,
  changed_by       TEXT NOT NULL                        -- sanitised client name
);
CREATE TRIGGER subsonic_play_queue_no_delete BEFORE DELETE ON subsonic_play_queue
BEGIN SELECT RAISE(ABORT, 'subsonic_play_queue: no physical delete'); END;

-- The offline set (D-M4a-19): a protected playlist with a fixed id.
ALTER TABLE playlists ADD COLUMN role TEXT;             -- NULL | 'offline' (Go-validated)
CREATE UNIQUE INDEX playlists_role ON playlists(role) WHERE role IS NOT NULL;
CREATE TRIGGER playlists_role_frozen BEFORE UPDATE OF role ON playlists
BEGIN SELECT RAISE(ABORT, 'playlists: frozen column'); END;
INSERT INTO playlists(id, name, description, items_json, items_count, revision, created_by, updated_by,
                      created_at, updated_at, hidden_at, role)
VALUES ('00000000-0000-7000-8000-0000000000f1', 'Offline set', 'Tracks every device keeps for offline listening.',
        '[]', 0, 1, 'system:migration', 'system:migration',
        '1970-01-01T00:00:00Z', '1970-01-01T00:00:00Z', NULL, 'offline');
