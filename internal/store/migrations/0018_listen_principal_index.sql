-- 0018_listen_principal_index.sql: U1-R2 (C306.7). Listening readers count the Owner side's events only (the Owner's
-- principal key, or a device the Owner owns), and viewer stats filter by the viewer's own key, so look listen events up by
-- principal. Additive: an index, no rows or triggers change (listen_events rows stay immutable).
CREATE INDEX listen_events_principal ON listen_events(principal_id, track_id);
