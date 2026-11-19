-- 0019_device_owner.sql: U1-D1 (C301.11, D-U1-14). A paired device, a Subsonic client and a pairing code belong to the user who
-- paired them: owner_key is 'owner' for the Owner and the lowercase username for any other user. The server alone writes it (from
-- the calling principal, never from a request field). Additive: every existing row defaults to 'owner', so the Owner's phones and
-- clients behave exactly as before. Rows are never deleted (revocation is a flag), so joins from history stay valid.
ALTER TABLE devices ADD COLUMN owner_key TEXT NOT NULL DEFAULT 'owner' CHECK (length(owner_key) > 0);
ALTER TABLE subsonic_clients ADD COLUMN owner_key TEXT NOT NULL DEFAULT 'owner' CHECK (length(owner_key) > 0);
ALTER TABLE pairing_codes ADD COLUMN owner_key TEXT NOT NULL DEFAULT 'owner' CHECK (length(owner_key) > 0);

CREATE TRIGGER devices_owner_frozen BEFORE UPDATE OF owner_key ON devices
BEGIN SELECT RAISE(ABORT, 'devices: frozen column'); END;
CREATE TRIGGER subsonic_clients_owner_frozen BEFORE UPDATE OF owner_key ON subsonic_clients
BEGIN SELECT RAISE(ABORT, 'subsonic_clients: frozen column'); END;
CREATE TRIGGER pairing_codes_owner_frozen BEFORE UPDATE OF owner_key ON pairing_codes
BEGIN SELECT RAISE(ABORT, 'pairing_codes: frozen column'); END;

CREATE INDEX devices_owner ON devices(owner_key, revoked_at);
CREATE INDEX subsonic_clients_owner ON subsonic_clients(owner_key, revoked_at);
