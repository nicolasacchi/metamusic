-- 0006_listen_detail.sql: client playback diagnostics ride the listen-events op. `detail` is a short
-- Go-validated token string ([a-z0-9_:=.,-]{0,64}, e.g. "code=4,fmt=flac"), only on diagnostic kinds
-- (autoplay_blocked|stall_end|advance|error). Never a title, URL or free text.
ALTER TABLE listen_events ADD COLUMN detail TEXT CHECK (detail IS NULL OR length(detail) <= 64);
