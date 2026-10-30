# Subsonic clients (Amperfy, Symfonium, Tempus)

metamusic speaks a read/write subset of the Subsonic API under `/rest/` so native players can browse, stream,
rate and sync it. Examples use `mm.example.com`.

## Creating a client

Open `/human/subsonic` (or call the `create_subsonic_client` operation with an `admin`-scope token) and create
one client per app. The response shows once:

- an **API key** beginning `mms_` for apps that support API-key login, and
- an **app password** for apps that use the token login (`u`, `t`, `s`).

Neither is shown again; revoke and create a new client to rotate. Personal access tokens are refused on `/rest/`.
A Subsonic client is a `device` principal with `read,write` scopes and never `generate`. Plaintext passwords
(`p=`) are refused with error 42.

Creating, listing and revoking clients is audited and sends a push naming the caller.

## Apps

- **Amperfy (iPhone):** server `https://mm.example.com`, the client's user name and app password (token login).
  Set the stream format to **Raw** so the FLAC master plays unchanged with seeking.
- **Symfonium (Android):** choose API key login and paste the `mms_` key. A full sync (`search3`) should finish;
  offline downloads use the `download` endpoint, which serves the file with current tags.
- **Tempus:** token login with the app password.

## Behaviour worth knowing

- **Salt window:** a token/salt pair is accepted again for 12 hours on `stream`, `download` and `getCoverArt`
  (players reuse one stream URL for every seek, also after a long pause) and for 10 minutes elsewhere. Error 40
  after a pause is a bug; report it.
- **Folders:** 1 "Library" (final or to-relisten), 2 "Drafts", 3 "Discarded"; without a folder id, 1 and 2.
- **Albums** are lineages (a root track and its descendants); there is one synthetic artist.
- **Star and rating:** `star` marks the track final; `unstar` of a final track sets it to relisten. `setRating`
  writes a human rating attributed to the device; `0` clears it.
- **Scrobbles and bookmarks:** plays are recorded as listen events (deduplicated on retry). A bookmark with a new
  comment creates one timestamped comment; comment-less bookmarks only store a resume position.
- **Lyrics** are always unsynced.
- **Downloads** carry current tags (title, album, track number, cover); the master file itself is never rewritten.

## Record albums and auto-albums (L2)

Lineage albums (`al-<root id>`) and the `albumId` of existing songs are **unchanged**. L2 adds albums to the same
`getAlbumList2` types, `getIndexes`, `search3`, `getAlbum` and `getAlbumInfo`; with no published Record and no
auto-album nothing differs from the lineage-only output.

- **Ids.** `rec-<record id>` is a published, non-hidden Record. `mus-<muse id>` is the auto-album of a Muse and `unf`
  the single "Unfiled" auto-album of Songs that belong to no Muse. An unknown, unpublished, hidden, empty (under the
  folder rule) or malformed id is **error 70** (data not found), as is a cover request for one.
- **Hidden Records.** A Record that is published but hidden is not a catalog album (error 70). It does not take its Songs
  out of the auto-albums: a Song leaves them only through a published, non-hidden Record.
- **Names are neutral.** An auto-album is named `Inspiration NNN` (a frozen per-Muse ordinal, never reshuffled) or
  `Unfiled`, so no inspiration text reaches a client. The human-only setting `subsonic_muse_names`, off by default,
  replaces the neutral name with the Muse's name. Curate a Record to get a real album name. A Record's own title is
  operator text shown on the publish page.
- **Tracks and discs.** `track` is contiguous over the listed songs (1..N, not the slot position), so an empty slot
  leaves no hole. `discNumber` is the slot's side (A to D as 1 to 4, 1 when no sides are used); auto-albums are disc 1.
  Under a restrictive music folder a Record keeps contiguous numbers over what the folder shows.
- **ReplayGain.** Songs carry `replayGain.trackGain` and `replayGain.albumGain` computed with the tape planner
  (automatic target, true-peak guard): `albumGain = -18.0 - target` and `trackGain = gain + albumGain`, in whole
  centi-dB. An unmeasured Take has no gain. Tagged downloads keep their own tag block.
- **`created`.** A Record album's `created` is its first publication time. An auto-album has none, so its `created` is
  that of its newest Song.
- **`lastModified` (getIndexes).** It derives from the catalog change token, which moves on publish, unpublish, a Pick
  event, a Song hide, a flag change, a title edit or a Take discard, and not on an unrelated rating. The stamp is the
  moment this process saw the token change, never earlier than the newest Record update. **Limitation:** the Muse-driven
  part is an in-process observation (the subsonic package never reads Muse tables), so after a restart a Muse rename that
  happened while the server was down can leave a stale client cache until some other change moves the token.
- **Unchanged operations.** `getStarred`, `getStarred2` and `getSimilarSongs` with a `rec-`, `mus-` or `unf` id behave as
  before; they are not album-aware. `getAlbumInfo` accepts the new ids.
- **A published Record changes under its listeners.** Floating slots follow Picks, so a Record's songs can change; clients
  that cache tracks see the change when the token moves.
- **Cost.** A list request over published Records costs 1 + 2N statements (fine for tens of Records).
