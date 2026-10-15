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
