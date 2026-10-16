# M4a — Remote Agents & Native Mobile — Implementation Plan (DRAFT, revision 3)

> **Revision 2** (review fixes): connector limits on top-ups (now `spend.Service.TokenLimitsFor` with the connector branch, rev 3); Subsonic global
> auth-failure class only throttles failures; endpoint-dependent salt window (12 h media); protected-resource
> metadata only at `/.well-known/oauth-protected-resource/mcp` with an own handler; default scopes without
> `offline_access`; Authelia `consent_mode: explicit`; rig-clock token source for the lifecycle test;
> loopback `PublicURL` for OIDC rigs; eager JWKS fetch and a server round-trip in `oidc probe`; allow-list
> before scope and a resource-metadata 401 for malformed bearers; scrobble/reportPlayback dedupe; Subsonic
> mapping gaps; Stage-1 ownership; Stage-0 sign-off (M4a-X0); recorder, ES256 and `resources/list`
> dispatch withdrawn; parity face D on a fixed subset; CI-time and Playwright/service-worker fixes;
> contract-number reconciliation with the M5/M6 drafts.

> **Revision 3** (aligned with the merged code, 2026-10-15; M4a builds from `main` after M3, the X2 probe and the
> auto-advance hotfix). Every symbol, file, table, column, operation id, Makefile target, script, CI job, test helper,
> config field and contract number this plan names was checked against the tree; the corrections are indexed in
> "Baseline reconciliation" below and applied in place, each marked "(rev 3)". The operator signed off M4a-X0 on
> 2026-10-15: **D-M4a-24 is changed** (connector and Subsonic-client management are registry operations an
> admin-scope agent may call, audited and pushed; only the global caps stay human-only) and **D-M4a-8 is approved**.
> Numbering: contracts move from C76–C91 to **C102–C117** (after M3's C68–C86 and L1's C87–C101), the migration
> from `0006` to **`0009_remote.sql`** (L1 takes 0008), and every citation of an M3 contract follows the M3
> renumbering (+11). Decisions stay D-M4a-1…26 (22 withdrawn; 25 and 26 are new).

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this
> plan task-by-task (TDD per task: write the named tests first, watch them fail, implement, review
> between tasks). Read `CLAUDE.md`, `docs/plans/2026-10-12-m0-skeleton.md` (C1–C9 incl. addenda), the
> M1 plan (C10–C24, D-M1-1…10), the M2a plan (C25–C42, D-M2a-1…22), the M2b plan (C43–C56,
> D-M2b-1…21), the M2c plan (C57–C67, D-M2c-1…14) and the M3 plan (C68–C86, D-M3-1…37) first, and note
> that L1 (C87–C101, migration 0008) is built in parallel. M4a builds on those exact contracts and
> continues the numbering: **M4a contracts are C102–C117**, decisions **D-M4a-1…26** (D-M4a-22 withdrawn
> in revision 2; the number stays reserved). The later L2, L3, M5 and M6 drafts were written against older
> numbers; each renumbers after the highest number in use when its own X1 task runs (see M4a-X1). Where M4a
> changes an earlier contract, the task that makes the change records it in that plan document in the same commit
> (named in each task's **Files**). Amended contracts:
> - **M0:** C1 (config, via the M2a C25 / M3 C68 form; `config_m4a.go`), C3 (`Classify` gains `/sw.js`; middleware row 1
>   gains OAuth access tokens on `/mcp`; the `Middleware` signature takes a `Verifiers` struct), C4
>   (`api.Deps`; `Meta` validation: `secret_fields` no longer require a human-only op, D-M4a-24), C6 (`httpserver.Options`: `/rest/`, `/.well-known/oauth-protected-resource/mcp`,
>   `/sw.js`), C7 (CLI `oidc probe`).
> - **M1:** C11 (`tracks.status_set_at`), C16.7 (`library_orphans`, i.e. `Importer.libraryOrphans`, excludes `derived/covers/` and
>   `derived/dl/`), C17 (`InsertTake` sets `status_set_at`), C18 (media ext `png`; `serve.ServeObject`
>   extracted), C19 (`get_media_links` gains `cover`), C20 (`seed-synthetic`: offline set and one connector
>   grant), C21 (e2e synthetic profile: offline set and one connector grant), C22.1 and C22.3 (local
>   source selection before minting links; a local-source media error never calls `put_device_profile`),
>   C22.2 (Media Session artwork from cover links), C22.5 (service worker), C24 (routing: `/sw.js` on
>   `mm-public`).
> - **M2a:** C25 (`reqinfo` transports `subsonic`/`oauth` and `ClientIP`; `redact` patterns), C26
>   (`Principal.Client`; Via `oauth`/`subsonic`), C27 (`TestNoDeleteSQL` allowlist gains `subsonic_salts`;
>   `playlists.role`), C29 (rate-limit classes, `Limiter.Peek`), C30 (client label for OAuth
>   principals; MCP resources and prompts), C31 (`UpdateTrack` writes `status_set_at`; comment source
>   `subsonic`), C33 (playlists: offline-set rules; media handler ext `png`; resolver chain), C36 (listen
>   source `subsonic`; the new `listening.Session`), C38 (`revoke_own_token` refuses OAuth principals;
>   `list_playlists`/`get_playlist` gain `role`), C39 (`app.Build`, `httpserver.Options`, sweeps), C40
>   (web routes, ESLint bans `caches`), C41 (routing, coverage), C42 (`servertest` OIDC/Subsonic
>   helpers; parity runner's fourth face).
> - **M2b:** C45 (`serve.Chain` order gains covers and downloads), C53 (`TestGETNeverWritesDB` pattern
>   extended to M4a GETs), D-M2b-13 (`web/src/api/media.ts` is no longer "the one place fetch is
>   allowed": `web/src/api/offline.ts` and the service worker under `web/src/api/sw/` also fetch; the rule
>   becomes "non-registry fetches live only in `media.ts`, `offline.ts` and `sw/`").
> - **M2c (rev 3):** C59/C63 (lossy and reference tracks in the Subsonic mapping, downloads, the offline set and
>   prompts, D-M4a-25), C67 (the media-tool CI tier gains `internal/media/tagged` and `internal/subsonic`).
> - **M3:** C69 (`spend_cap_changes.target` comment gains `'connector:<id>'`), C72.3/C72.6 (the existing
>   `(*spend.Service).TokenLimitsFor` resolves token **or connector-grant** limits from a principal key —
>   `ReserveTopUp` already calls it with the batch owner's key, rev 3), C81 (`CapsView` gains `connectors`;
>   five management operations leave the human-only set, D-M4a-24), C82 (ntfy "Connector connected").
> - **L1 (rev 3):** nothing amended; M4a only follows L1's numbering rule (migration 0008 is L1's).
>
> **Public/private split.** Everything above the line that consists solely of the HTML comment
> `<!-- PRIVATE APPENDIX -->` is public-safe (no real hostnames, IP addresses, usernames, titles,
> artists, lyrics, request-id prefixes, archive counts, client secrets, device names or personal data;
> `mm.example.com`, `auth.example.com`, `ntfy.example.com` and synthetic examples only; the hosted
> clients' own callback URLs are public vendor documentation). Only that part is copied to
> `docs/plans/<date>-m4a-remote-agents-mobile.md`
> (`sed '/^<!-- PRIVATE APPENDIX -->$/,$d' <this file> > <copy>`), `git add`ed and checked with
> `scripts/public-gate.sh --staged` before it is committed. The appendix is never copied.
>
> **Secrets (binding for every agent executing this plan).** No agent reads the secrets directory,
> an Authelia configuration file on the host, a client secret, a Subsonic app password or API key, or an
> OAuth access/refresh token. OIDC client secrets (if any, D-M4a-5) are generated and stored by the
> operator and never reach this repository or the app. Every step that touches the host's Authelia, a
> real claude.ai/ChatGPT account or a real phone is **operator-assisted** (M4a-X0, M4a-X2, M4a-X3) and
> marked so.
>
> **Task ids** carry the `M4a-` prefix. Lane labels (`MM_LANE`): `m4a-found`, `m4a-conn`, `m4a-sub`,
> `m4a-media`, `m4a-agent`, `m4a-web`, `m4a-infra`, `m4a-int`.

**Goal:** the library leaves the browser tab. (1) **Hosted MCP clients** — claude.ai on web, desktop and
mobile, and ChatGPT — connect to `https://mm.example.com/mcp` with OAuth: the forward-auth SSO provider
(Authelia 4.39) is the authorization server with **pre-registered clients**, and the app is a pure
resource server that verifies RFC 9068 JWT access tokens against the provider's JWKS with `iss`, `aud`
(RFC 8707 resource = the MCP URL), `exp`, scope and algorithm pinned, publishes RFC 9728
protected-resource metadata, and maps every (client, subject) pair to a **connector grant** whose
scopes, daily budget and auto-approve limits the human sets on `/human/connectors` (generate is off by
default). (2) **Native players** — Amperfy on iOS, Symfonium or Tempus on Android — use an
**OpenSubsonic bridge** at `/rest/` (XML and JSON, `.view` and bare paths, GET and form POST, API keys
and token/salt with per-client AES-GCM app passwords, an endpoint-dependent salt replay window, throttled
auth failures)
that streams the untouched 24-bit/48 kHz FLAC master with Range, downloads a copy with the current tags,
and maps the domain (album = lineage; folders Library/Drafts/Discarded; `userRating` = human stars;
`star` = final; bookmarks with comments = timestamped comments; scrobbles = listen events). (3) The
**PWA works offline**: a service worker, an explicit Download action and a server-side **offline set**
playlist that agents can edit, stored in the Cache API as 2 MiB chunks and served as 206 responses of at
most 4 MiB, with `navigator.storage.persist()`. (4) Every track gets **deterministic cover art** from
its seed and lineage (lock screens, CarPlay, Media Session, Subsonic). (5) MCP gains **resources**
(`metamusic://tracks/{id}/context`, `metamusic://tracks/{id}/spectrogram`) and **prompts**
(`critique_track`, `write_variant`, `session_review`), each a presentation of a registry operation.

**Architecture:** new packages behind small interfaces so lanes build in parallel:
- `internal/oidc` — resource-server side of OIDC: discovery, JWKS cache, `Verifier.Verify` (RFC 9068
  profile, RS256 only). `internal/oidc/oidctest` (**test-only**) is a stub OIDC provider that mimics the
  Authelia 4.39 endpoints, PKCE, RFC 8707 audience, RFC 9207 `iss`, refresh rotation and JWT access
  tokens, with fault injection.
- `internal/connectors` — connector grants (store, `ConnectorVerifier` used by the C3 middleware,
  protected-resource metadata handler, limits for M3's spend checks).
- `internal/subsonic` — the OpenSubsonic protocol adapter over the same **services** (library,
  judgments, playlists, listening, covers, downloads). Not part of the registry (design §4.1: its shape
  is mandated by the protocol); its writes go through service-level audit. Credentials live in
  `internal/auth/subsonic.go`. `internal/subsonic/subsonictest` (**test-only**) signs requests like the
  three reference clients and replays the synthetic client fixtures.
- `internal/media/cover` — deterministic cover renderer + input-addressed PNG cache.
- `internal/media/tagged` — Go-written FLAC copies with current tags (frames byte-identical to the
  frozen master) + input-addressed cache, for Subsonic `download` and the SPA's "Download FLAC".
- `internal/prompts` — the prompt catalog and renderer behind `get_prompt`.
- `internal/mcpgen` learns resources and prompts; both dispatch registry operations through M2a's gate.
- The SPA gains a service worker (`web/src/api/sw/`, the network layer), an offline store, `/offline`,
  `/human/connectors`, `/human/subsonic`, cover art, "Download FLAC", "Download for offline" and
  prompt buttons. Everything the SPA does is still a registry op (8 new, 8 amended — C113).

**Tech stack:** one Go module promoted to a direct dependency, `github.com/golang-jwt/jwt/v5` (in the module graph and `go.sum` through
`github.com/modelcontextprotocol/go-sdk` v1.8.0 but **not yet in `go.mod`**: M4a-C1 adds `github.com/golang-jwt/jwt/v5
v5.3.1` as a direct requirement; D-M4a-3). JWKS parsing,
AES-GCM, PNG covers, FLAC block writing, XML/JSON envelopes and the service worker are stdlib Go / web
platform. go-sdk v1.8.0's client-side `auth.AuthorizationCodeHandler` (an existing dependency; it brings
`golang.org/x/oauth2`, already an indirect requirement in `go.mod`; verified: `PreregisteredClient`, `RequestRefreshToken`,
`AuthorizationCodeFetcher`, `NewTokenSource`, `oauthex.GetProtectedResourceMetadata`) drives the OAuth flow in tests and in the operator
CLI `oidc probe`. Test-only: `github.com/google/jsonschema-go` (already test-only since M2b) for the
OpenSubsonic contract tests.
Vendored data (not code): `third_party/opensubsonic/openapi.json` (Apache-2.0, attribution). Web: no
new runtime dependency (no Workbox: the service worker is ~300 lines and range handling must be ours).
Pin versions at implementation time (context7 for Authelia, Vite, Playwright; `go list -m -versions`).

**Out of scope (YAGNI, with owner):** the in-app OAuth authorization server, CIMD and DCR (M4b, only if
a required client cannot pre-register); OAuth tokens on REST `/api/` (D-M4a-2); MCP Apps (in-chat player
widgets, M5); on-the-fly transcoding of any kind (pre-encoded MP3 only); Subsonic jukebox, shares,
podcasts, internet radio, videos, chat, user management, avatars, `startScan`, `transcodeOffset`,
`sonicSimilarity`, real artist/album info and top songs (stubs); synced lyrics (D-M4a-13); offline
judging (ratings/comments queued offline — M2a's listen-event outbox stays the only offline queue);
background sync and push to devices; per-section timed covers or animated art; `listen.flac` (D11);
automated revocation inside Authelia; multi-user anything; any JWS algorithm other than RS256 (the
provider already signs with RS256; D-M4a-3); a Subsonic request recorder and recorded client fixtures
(withdrawn D-M4a-22 — revisit only if the device checklist finds client quirks the synthetic fixtures
miss); listing resources through `resources/list` (D-M4a-20); L1's Muse/Song/Take vocabulary in the Subsonic mapping
(albums stay lineages; L1 guarantees no Muse text reaches a Subsonic-bound package, L1 C99 — nothing to do here).

---

## Dependencies and sequencing

- **Hard:** M0, M1, M2a, M2b, M2c and M3 contracts exactly as written, merged to `main` (done: the build starts from
  `main` at the M3 / X2-probe merge); the **Release 1 gate** (roadmap) is green. Branch `m4a`, in its own root worktree.
  Schema version after M4a: **9** (`0009_remote.sql`; L1 takes 0008). Production is at schema 7. `loadMigrations`
  refuses numbering gaps, so **while L1 was unmerged the file was committed as `0008_remote.sql`** (schema 8, upgrade
  from 7) and the **renumber-on-merge step** (M4a-X1, step 0) renames it to `0009_remote.sql`, sets the single test
  constant `remoteMigration` (`internal/store/schema_m4a_test.go`) to 9, regenerates the generated files (never hand-merged) and runs the
  upgrade test from schema 8 on L1's fixture. Earlier schema assertions: most compare with `len(migrations)`, but
  `TestMigration0007_UpgradeFromM2d` (`internal/store/upgrade_m3_test.go`) hard-codes `v != 7` after `store.Open`;
  M4a-B1 changes it to `v != len(migrations)` (rev 3).
- **What M4a uses (binding; nothing below is conditional):**
  - **M0/M1:** C3 path classes and middleware, the registry, `mcpgen` dispatch, `serve.Signer` and the
    `/media/` handler, `library.Service` (search, tracks, sections, FTS rules), `flacmeta` (block
    offsets, frames offset), `yue2.LyricsSections`, `yue2.Slug`, `internal/synth`, `internal/problem`.
  - **M2a:** `Principal.Key()`/`ActsAsHuman()`, the gate (audit → authorize → rate limit →
    idempotency), `reqinfo`, `redact`, `ratelimit`, `audit`, `notify`, `judgments` (Rate, UpdateTrack,
    AddComment), `playlists`, `listening`, `trackctx`, `app.Build`, `servertest` and the parity runner,
    `/human/` page rules (full-page navigation, step-up), `api.RefFor`, `api.IdemHeader`.
  - **M2b:** `get_track_spectrogram` (`mcp_image`), `serve.Chain`, `library.LineageTracks`/`TrackRefs`,
    `internal/textfold`, the `web/src/api/media.ts` precedent for non-registry fetches.
  - **M2c (rev 3):** `tracks.lossy`, `library.Service.RecipeKnown/RequireRecipeKnown` and
    `LosslessMaster/RequireLosslessMaster`, `get_media_links` (`flac: null`, `lossy`), `archivetest.SyntheticArchive(t, "reference")`.
  - **M3:** `spend.TokenLimits`, `(*spend.Service).TokenLimitsFor`, `NeedsApproval`, `CheckCaps`, `ReserveTopUp`,
    `get_budget`, `generation.CapsView`, `spend_cap_changes`, `clocktest.Manual`, `store.FormatTime`, `notify.Message`,
    `internal/archtest` (import-graph rules).
- **Operator inputs (tracked in M4a-X0/X2/X3):** sign-off of D-M4a-24 (changed) and D-M4a-8 (approved) — **recorded 2026-10-15** (M4a-X0, done); the **second SSO restart** (Authelia OIDC clients, scopes, claims policy,
  lifespans) in a window scheduled in advance; access to claude.ai (web + mobile) custom connectors and,
  if available, ChatGPT connectors; Amperfy on the iPhone and Symfonium or Tempus on the Android phone.

---

## Baseline reconciliation (revision 3: merged M0 + M1 + M2a + M2b + M2c + M3 code, `main` at the X2-probe merge)

Verified as the plan assumes (no change): the principal model (`Principal.Key()` = `agent:<id>`, `ActsAsHuman()` true for
humans and devices, scopes `read|write|generate|admin`); `auth.Middleware(cfg, sec, tokens, sessions, devices)`; the
`ClassPublic/ClassMachine/ClassUI` classes (`/rest/`, `/media/`, `/mcp`, `/.well-known/` are machine paths); the gate order
(audit → authorize → rate limit → idempotency) and `idem.Redact` of `secret_fields`; `reqinfo.SanitizeClient`; the
`mcpgen` dispatch client label; `serve.Signer/TTLFor/Resolver/Chain(resolvers ...)`; `flacmeta.Info` (`Blocks`, `FramesOffset`,
`SeekPoints`); `library.Service.Media/LineageTracks/TrackRefs/SearchTracks`; `judgments.Service.Rate/Ratings/AddComment`;
`playlists.Service.Create/Update/Get/List`; `listening.Service.Log` and the Go-validated `kinds`; `store.FormatTime`;
`clocktest.Manual`; `servertest.Normalize`, `Rig.Human/Agent/MCP/Stdio`, `SeededReader`; the backup's `mediaOf`
(copies only files that `tracks`, `artifacts` and `renditions` reference, so neither new cache is ever backed up); the
`mm-media` router already carries `PathPrefix(/rest/)` and `mm-agent` already carries `PathPrefix(/.well-known/)`; the
acceptance synthetic archive (9 takes, 2 sibling edges ⇒ 7 lineages, 1 artist: the Symfonium numbers hold).

| Area | The plan assumed | The code is | Resolution (where) |
|---|---|---|---|
| Contract numbers | M4a = C76–C91; M3 = C57–C75 | M3 = C68–C86 (M2c took C57–C67), L1 = C87–C101 | M4a = **C102–C117** (every old number +26); every M3 citation +11 (C57→C68, C58→C69, C61.x→C72.x, C70→C81, C71→C82, C73→C84, C74→C85, C75→C86); D-M3 = 1…37; D-M4a ids unchanged. The L2/L3 drafts (C102–C130) and M5/M6 collide with the new range; each renumbers in its own X1 after the highest number then in use |
| Migration | `0006_remote.sql`, schema 6 | production schema 7; L1 takes 0008; loader refuses gaps | `0009_remote.sql` (interim `0008` until L1 merges, "Dependencies" above; D-M4a-26) |
| Schema assertions | "already compare with `len(migrations)`" | `TestMigration0007_UpgradeFromM2d` (`upgrade_m3_test.go`) hard-codes 7 | B1 changes it to `len(migrations)` |
| Upgrade-test helper | `storetest.OpenAtVersion(5)` | no such package; `store.OpenRaw` + `store.MigrateTo` (`internal/store/export_test.go`, package `store_test`) with `librarytest.FixtureM3` | C103/B1 tests rewritten; files `schema_m4a_test.go`, `triggers_m4a_test.go`, `upgrade_m4a_test.go` (suffix convention) |
| Spend limits | new `spend.LimitsFor(p)` / `LimitsForKey(key)` and edits at every M3 call site | one method, `(*spend.Service).TokenLimitsFor(ctx, q, principalKey)`, already called with `p.Key()` (generation create, estimate, `get_budget`) and with the batch owner's key (`spend.ReserveTopUp`); `PrincipalDaily` and `ReserveTopUp` already treat every `agent:` key as an agent | extend that one method with the connector branch; no new function, no call-site edit; tests renamed (C106) |
| `CapsView` | `spend` type | `generation.CapsView` (+ `capsView`) and `api.CapsView` (`types_m3.go`) | both gain `connectors` (C106) |
| Parity exceptions (operator, 2026-10-15) | five management ops human-only under `/human/api/` | `api.Meta` validation: `secret_fields` require a human-only create op (`registry.go`); `human_only` ⇔ `/human/api/` | D-M4a-24 changed: the five ops move to `/api/`, scope `admin`, MCP generic; B2 relaxes the `secret_fields` rule; C113 |
| Scripted-agent tests | not considered | `internal/acceptance/every_ui_action_test.go` derives the required set from the registry (not human-only, MCP not excluded, UI route); `TestParityEveryOperation` (not `…HasACase`) needs a case per non-excluded op; `TestRegistry_OpCount` pins 81 | B2 adds the eight scripted calls and the cases (management cases with masks) and re-pins the count to 89 |
| Track status | `UpdateTrack` may return `ErrInvalidTransition` | `ErrInvalidTransition` belongs to `update_comment`; `UpdateTrack` returns `*library.ValidationError` or `library.ErrNotFound`, and already writes `status_set_by` | C109 mapping and `TestSubsonic_StatusErrorsAreError0` use `*library.ValidationError`; B1 adds only `status_set_at` |
| Go-validated sources | "comment source `subsonic`", "listen source `subsonic`" | `judgments.commentSrcs` and `listening.sources` are separate Go slices (DB columns are free text) | B1 adds `subsonic` to both (files listed in B1) |
| Timestamp widths | not considered | pre-M3 columns and `judgments.stamp()` are RFC 3339 (20 chars); M3 columns use `store.FormatTime` (24 chars) | `tracks.status_set_at` and the offline-set row use the 20-char form; the new M4a tables use `store.FormatTime`; C103 |
| Rate limiter | `Limiter.Allow(...)` (bool) and classes in one table | `Allow(key, classes...)` returns `(ok, retryAfter, bucketClass)`; an **unknown class is skipped** (always allowed); `servertest`'s unlimited config lists the five old classes only | B1 adds the classes to `ratelimit.Default` and `Peek` (unknown class ⇒ ok); B2 lists them in the rig's unlimited config; throttling tests pass `Options.Limits` explicitly (C102, C108) |
| `/mcp` challenge | the 401 is new | `httpserver` already answers an anonymous `/mcp` call with `Bearer resource_metadata="<PublicURL>/.well-known/oauth-protected-resource/mcp"`; `/.well-known/*` and `/sw.js` otherwise fall through to `spaHandler` (the SPA shell, 200) | row 1b only adds the `error=` variants; B2 mounts the PRM handler and an explicit 404 problem for every other `/.well-known/` path; `/sw.js` gets its headers (C114) |
| App wiring | flat `app.Options.OIDCClient`, `SubsonicSleep`; constructors in `app.go` | M3 precedent: nested `app.Options.M3 M3Options`, constructors in `internal/app/m3.go`; the resolver list is `mediaserve.Chain(resolvers...)` in `Build` | `app.Options.M4a M4aOptions` and `internal/app/m4a.go`; `Build` appends covers and downloads to the same list (C114) |
| CLI | new `oidc` command | `main.go` owns usage text and the dispatch switch; `seed-synthetic` returns early ("already seeded") | B2 adds the `oidc` case (stub until C3); the e2e additions run idempotently after both paths, profile `e2e` only; tracks resolved by (request id, seed): `syn-song-03`/1003 (12 s, timed) then `syn-song-01`/1001 (6 s) (C114) |
| Fixtures and rigs | `librarytest.Fixture` gains rows; `librarytest.SyntheticArchive` | layered fixtures `Fixture` → `FixtureM2c` → `FixtureM3` (+ `Apply…`), `servertest.Options` flags with "requires" rules, `TestWriteFixtureDB_M3`; the archive helper is `archivetest.SyntheticArchive(t, profile)`; ntfy assertions need `Options.Canary` | `librarytest.FixtureM4a/ApplyM4a`, `servertest.Options.M4a` (requires Fixture, M2c, M3), `TestWriteFixtureDB_M4a` and the `fixture-db` Makefile target; `archivetest` in all fixtures-from-archive text (C117) |
| Playlists | the offline set is one more playlist | the migration row appears in every database: `playlists_test.go:154` (`List(false)` empty), `librarytest/fixture_test.go:84` (row counts), parity `list_playlists` and the store schema lists see it; `playlists.PlaylistPatch.Revision` is required; `playlists.Error` has `Details`; `playlists.MaxItems` = 1000; no `offline` package | B1 updates those tests by name; constant `playlists.OfflineMaxItems` = 100 (C102, C111) |
| Lossy and reference tracks | not considered (M2c deferred "OpenSubsonic handling of lossy masters" to M4a) | `tracks.lossy`, MP3 masters, no FLAC and no `mp3_320` rendition, `get_media_links` `flac: null`; reference tracks have stub recipes (seed 0, `MM_REFERENCE=1`) | D-M4a-25 (Subsonic, downloads, offline set, prompts) |
| Orphan scan | `library_orphans` | `Importer.libraryOrphans` in `internal/importer/report.go` skips `derived/probe`, `derived/clips`, `derived/exports` | M1 (media lane) adds `derived/covers`, `derived/dl`; test `orphans_m4a_test.go` |
| Redact | new patterns | `credRE` (`mm[pdc]_…`), `bearRE`, `runRE` (≥ 32 chars), `queryRE` exist | B1 adds `mms` to `credRE`, JWS compact strings and form `key=value` pairs |
| Acceptance helpers | `TestNoSecretLeak` extension and `TestGETNeverWritesDB_M4a` in `internal/httpserver` | `internal/acceptance` holds `TestNoSecretLeak`, `_M3`, `TestGETNeverWritesDB_M2b/_M2c` | `TestNoSecretLeak_M4a`, `TestGETNeverWritesDB_M4a` in `internal/acceptance/m4a_registry_test.go` (B3) |
| Import graph | "connectors never imports spend" as prose | `internal/archtest` (`TestImportGraph_M3`) enforces edges | B2 adds `TestImportGraph_M4a` (C116) |
| CI | jobs `go`, `e2e`; no CI edit | jobs `go-fast`, `go-media`, `web`, `e2e`, `coverage`; `scripts/ci-go-test.sh` hard-codes the media tier; `scripts/ci-selftest.sh` asserts it | I1 adds `./internal/media/tagged`, `./internal/subsonic` to the media list and the selftest (C116) |
| Provider example test | "parses the YAML" | no YAML dependency in `go.mod`; the routing contract reads `labels.example.yml` line by line | `TestAutheliaExample_Shape` reads the example with the same text approach (no new dependency); it runs in `make check` (C116) |
| Web | routes in `App.svelte`; ESLint bans gain `caches` and `indexedDB`; Playwright `chrome`/`chrome-pixel7` | `APP_ROUTES` in `web/src/lib/router.ts` (with `router.test.ts`); `banned` already holds `indexedDB`, and `src/api/**` is exempt directory-wide; Playwright runs phases (phase 1, `listen-*`, M3) with project lists in `playwright.config.ts` | W1 edits `router.ts`; only `caches` is added; the M4a specs form their own serial phase (C115, C117) |
| Tools list | 40 000 B ceiling | `toolsCeilingBytes = 87300` (`internal/mcpgen/m2a_contract_test.go`), golden `internal/mcpgen/testdata/tools_list.golden.json`, relative budget `toolsListBaselineBytes` | unchanged mechanics; the eight new operations are MCP generic, so the golden grows only through `get_media_links` (C113) |
| `whoami` | reports `via` | doc strings list "proxy, local, token or device"; `kind` enum human/agent/device | B1 updates the docs (`oauth`, `subsonic`); no new field |
| Step-up | human-only ⇒ `/human/api/` ⇒ step-up | non-human-only ops under `/api/` skip the local-mode step-up (the `/human/*` page load keeps it) | accepted with D-M4a-24; recorded in Risks |

---

## Decisions recorded by this plan

These refine the design of record; M4a-X1 copies them into the private design document.

| Id | Decision | Why |
|---|---|---|
| D-M4a-1 | A hosted connector is an **agent** principal (`Kind agent`, `Via "oauth"`, `ID` = connector-grant id, `Client` = OAuth `client_id`). Its app scopes come from the **grant**, never from the token's scopes. The grant is created automatically on the first valid token of a (client, subject) pair with scopes `read,write`; `generate` and every spend limit are set by the human on `/human/connectors` (or by an admin-scope agent, D-M4a-24). | The human already passed two-factor consent at the provider for this client; an extra approval step would block the first `search_tracks`. Mapping to `agent` keeps every M2a/M3 invariant (rating isolation CHECKs, D-M2a-19 masking, agent rate limits, critic verdicts, approval rules) without a new principal kind. |
| D-M4a-2 | OAuth access tokens are accepted **only on `/mcp` and `/mcp/…`**. A JWT bearer on `/api/*` is refused like any unknown bearer (401). REST bearers stay PAT-only. | The token's audience is the MCP resource (RFC 8707); hosted clients only speak MCP; widening it to REST would add a second credential type to every REST test for no client. |
| D-M4a-3 | JWT verification uses `github.com/golang-jwt/jwt/v5` (promoted from the module graph) with `WithValidMethods(["RS256"])`, `WithStrictDecoding`, `WithIssuer`, `WithAudience`, `WithExpirationRequired`, `WithIssuedAt`, `WithLeeway(30s)` and an injected clock; the app additionally requires header `typ` = `at+jwt` (RFC 9068 §4), rejects any `crit` header and never uses `jku`/`x5u`/`jwk`/`x5c`. **RS256 is a constant** (`oidc.Alg`), not configuration. JWKS parsing is our own stdlib code (RSA ≥ 2048 bits with e = 65537; every non-RSA key is skipped). | A JWS library avoids hand-rolled parsing bugs; it is already pinned in `go.sum` by the MCP SDK, so the supply chain does not grow. A JWKS library would be a new module for ~60 lines of stdlib code. One pinned alg makes alg-confusion structurally impossible; the provider already holds an RS256 key, so a configurable alg and EC parsing would be speculative (YAGNI). Strict decoding refuses non-canonical base64url. |
| D-M4a-4 | **Provider contract (Authelia 4.39):** each connector client issues RFC 9068 JWT access tokens (`access_token_signed_response_alg: RS256`), has `audience: [<PublicURL>/mcp]` with `requested_audience_mode: implicit`, PKCE S256, and the claims policy `metamusic` that defines the custom scope `metamusic` with the custom claim `mm_user` (= username) **copied into the access token**. The app requires scope `metamusic`, requires `mm_user` ∈ `MM_TRUSTED_USERS`, and advertises `scopes_supported: ["openid","metamusic"]` in its protected-resource metadata (default `MM_OIDC_SCOPES`; the override stays). | Implicit audience puts the resource in `aud` even if a client omits `resource`; the custom scope is both the consent purpose and the carrier of the username (Authelia 4.39 custom scopes/claims). `offline_access` is **not** advertised: the MCP spec says resource servers SHOULD NOT list it, and go-sdk's client (SEP-2207, `auth/authorization_code.go`) already adds it when the **authorization server's** metadata lists it and `RequestRefreshToken` is set — the protected-resource metadata makes no difference (`TestConnector_RefreshWithoutOfflineAccessInPRM`). Whether claude.ai needs `MM_OIDC_SCOPES="openid offline_access metamusic"` is decided by DC-M4a-2 (next day, no new login) and recorded in the private notes. |
| D-M4a-5 | The claude.ai connector is a **public** client (`token_endpoint_auth_method: none`, PKCE S256, exact redirect `https://claude.ai/api/mcp/auth_callback`). A ChatGPT client is confidential (`client_secret_post`) only if its connector UI requires a secret. No client secret ever reaches the app or this repository. | Claude's connector dialog takes an optional secret; a public client removes the basic-vs-post ambiguity that would cost an SSO restart per attempt. The secret would protect nothing extra: the redirect target is the vendor's, and only the trusted user can complete the two-factor login. |
| D-M4a-6 | **Two kinds of scope refusal.** A token of an **allow-listed** client without scope `metamusic` gets **HTTP 403** `WWW-Authenticate: Bearer error="insufficient_scope", scope="metamusic", resource_metadata=…` (MCP step-up); an unlisted client is refused with 403 `CONNECTOR_FORBIDDEN` before the scope is looked at (C105 step 6), so it is never sent a step-up challenge. A tool the **grant** does not allow (e.g. `generate` while off) is a normal tool result `IsError` with problem `FORBIDDEN_SCOPE` and `details.hint` naming `/human/connectors` — never an HTTP step-up. | Re-running OAuth can fix a missing token scope but never an app-side grant; an HTTP 403 for grant scopes would make clients loop through the browser login. |
| D-M4a-7 | Grant creation is audited (op `auth:connector_grant`, transport `oauth`) and pushed via ntfy ("Connector connected: <label>"). Revocation is app-side and immediate (`set_connector_grant revoked:true` ⇒ 403 `CONNECTOR_REVOKED` on the next request); `revoked:false` restores it. Nothing is automated inside the provider. | The human must notice a new remote agent; an app-side switch works even while the provider's refresh tokens live on. |
| D-M4a-8 | **Subsonic credentials are their own kind** (amends design §4.2 "apiKey = a PAT restricted to read/write"): one `subsonic_clients` row per app with an API key `mms_…` (HMAC-hashed) and/or an app password (AES-256-GCM, key derived from `secret_key`, AAD = client id). A Subsonic client resolves to `Kind device`, `Via "subsonic"`, scopes `read` or `read,write`, never `generate`/`admin`. PATs are never accepted on `/rest/`; plaintext `p=` (and `enc:`) is refused with error 42. The §4.2 change was **approved by the operator on 2026-10-15** (M4a-X0). | Subsonic puts credentials in query strings (logged by players, crash reporters, proxies); a leaked Subsonic key must not open REST or MCP. `device` is the M2a kind that "counts as human" for ratings, comments and listens — exactly what `setRating` = human stars needs — and is structurally refused on `/human/`. |
| D-M4a-9 | **Salt replay window, by endpoint:** a (client, salt) pair with a valid token is accepted after its first use for **12 hours** on the media endpoints `stream`, `download` and `getCoverArt`, and for **10 minutes** on every other endpoint (`auth.SaltWindowFor`), then refused (error 40). The window is chosen by the endpoint of the current request. Salts are 6–64 printable ASCII characters. Salt sightings are recorded only after a token matched, in the cache table `subsonic_salts` (pruned hourly), so a restart does not reopen old URLs. | AVPlayer and ExoPlayer keep one `stream` URL (same `t`/`s`) for the whole item and re-request it for every Range and seek, also after a long pause, so single-use salts or a 10-minute window would turn "pause 15 minutes, seek" into error 40; 12 h matches design §5.2's media-link TTL. A leaked media URL therefore only reads media of that client; writes and catalog reads keep the 10-minute bound. Recording only verified salts stops table flooding. |
| D-M4a-10 | **Folders, stars, status:** folder 1 "Library" = status `final` or `to_relisten`; 2 "Drafts" = `draft`; 3 "Discarded" = `discarded`; no `musicFolderId` = folders 1 + 2. `star` sets `final`; `unstar` of a `final` track sets `to_relisten` (else no-op); `starred` = `tracks.status_set_at` (new column, written by every status change). | Folders partition the library so no song appears twice; D-M2a-18 already hides discarded takes by default; "un-finalising" is a judgement, not a discard. |
| D-M4a-11 | **Album = lineage** (root + members ordered by `recorded_at`, `id`; track number = position); album name = the root's current title; one synthetic artist `metamusic` (`ar-metamusic`); genres = style terms. Ids: songs = track id; albums `al-<root id>`; cover art `al-<root id>` / `tr-<track id>`. | Design §5.6; one artist keeps ID3 browsing flat; prefixed ids cannot collide with UUIDs. |
| D-M4a-12 | **Listens and bookmarks:** `scrobble` and `reportPlayback` write M2a listen events (source `subsonic`) whose ids are **deterministic UUIDv8** (sha256 of client id, track id, kind, client time), so retries dedupe through M2a's `ON CONFLICT(id) DO NOTHING`. A client that reports playback must not count a play twice: `scrobble` events for a (client, track) pair are dropped while a `reportPlayback` session for that pair was seen in the last **30 minutes** (in-memory, C109). `createBookmark` with a non-empty comment that differs from that bookmark's previous comment creates one timestamped comment (kind `note`, source `subsonic`); comment-less bookmarks are stored only as resume positions. The play queue is one row. | Players re-send scrobbles after reconnecting; comment-less resume bookmarks would spam the comment list (research); one human means one queue. |
| D-M4a-13 | Subsonic lyrics are always **unsynced** (`synced: false`), section headers dropped, one blank line between sections. | Only section-level times exist (plan sections); per-line timing would be invented (D-M2b-9). |
| D-M4a-14 | **Downloads with current tags** are written in Go: `fLaC` + STREAMINFO and SEEKTABLE copied verbatim + a new VORBIS_COMMENT (current title, album = lineage, track number, date, genre, ReplayGain, `MM_*`) + a PICTURE (512 px cover) + the master's audio frames byte for byte. Files are input-addressed (`derived/dl/<aa>/<key>.flac`), 0444, swept after 7 days, never backed up. A reference FLAC carries `MM_REFERENCE=1` in place of `MM_SEED` (D-M2c-12); a lossy (MP3) master is never tagged or rewritten and is served unchanged (D-M4a-25). | Masters stay frozen (rule 7); the frames region and STREAMINFO MD5 are provably identical; no `metaflac` argv built from titles. |
| D-M4a-15 | **Cover art** is a pure-Go deterministic renderer: palette from `sha256("mm-cover/1|" + root id)` (siblings share it), shapes from a splitmix64 stream seeded by the take's seed; a 16-colour `image.Paletted` PNG; input-addressed cache `derived/covers/<aa>/<key>.png`; served as signed `/media/<key>.png` and by Subsonic `getCoverArt`. | Lock screens and CarPlay look broken without art (research); a paletted PNG of flat shapes stays small; an own PRNG keeps the bytes stable across Go releases. |
| D-M4a-16 | Subsonic protocol errors are **HTTP 200** with `status="failed"` (the protocol's convention); only auth throttling answers **429** (with `Retry-After`) and other methods **405**. Unknown endpoints are error 0 ("unsupported endpoint"). | Clients branch on the envelope, not on HTTP status; a 429 is the only case where a client must back off. |
| D-M4a-17 | Subsonic **writes** are audited at the service level (transport `subsonic`, op `subsonic:<endpoint>`, client `subsonic:<c>`); reads are not (devices' reads are not audited in M2a either). Rate limits: class `subsonic` per client (1200/min, burst 300); auth failures per client IP (10/min, burst 5, checked before verification) and globally (60/min, burst 20, consumed and checked **only after a failure** — it never refuses valid credentials); the client IP comes from `X-Forwarded-For` only when the peer is a trusted proxy. | Design §4.1 ("its writes still go through service-level audit"); a library sync fetches hundreds of covers quickly; brute force must be bounded per source and in total, but a flood of bad requests from anywhere must not lock the owner's phones out. |
| D-M4a-18 | **Service worker** lives in `web/src/api/sw/` (the network layer, rule 2) and is built to `/sw.js` as a classic script. It **never handles `/api/`, `/human/`, `/auth/`, `/mcp`, `/rest/`, `/__e2e/`**; it precaches the app shell, serves navigations network-first with the cached shell as fallback, and answers `/media/<sha>.<ext>` only for files that are **completely** in the offline cache. It calls `clients.claim()` on activate. It is registered, and every offline feature is shown, **only when `isSecureContext`** (service workers and `crypto.subtle` require it); plain-HTTP LAN origins (which M1 keeps working) simply have no offline UI. `/sw.js` is public (ClassPublic, `mm-public`). | Session-loss detection (M1 C22.4) and served-operation recording must see every API call; a public script lets the browser update the worker after the SSO session expired; incomplete files would stall playback. |
| D-M4a-19 | **Offline set** = one protected M2a playlist (`role = 'offline'`, fixed id, created by migration 0009), ≤ 100 items, never hidden or deleted; agents edit it with `update_playlist`. Each device downloads every item in **its own** effective format (M1 C22.1), in 2 MiB chunks, only on an explicit "Sync" or "Download" tap (no automatic downloads); `navigator.storage.persist()` is requested on the first download. | Design §5.3; a server-side set keeps agent parity; iOS cannot detect cellular, so automatic downloads could burn a data plan. |
| D-M4a-20 | MCP **resources and prompts are presentations of registry operations**: reading `metamusic://tracks/{id}/context` dispatches `get_track_context`, the spectrogram resource dispatches `get_track_spectrogram`, every prompt dispatches `get_prompt` — all through the gate, so audit, rate limits and principal rules apply. `resources/list` returns an **empty list** without dispatching anything; the two templates are the discovery surface (design §4.1). | Rule 1 (a capability exists iff it is a Huma op); tools stay the only primitive every client supports. Hosted clients call `resources/list` on every connect: dispatching `search_tracks` there would write an agent audit row and spend rate-limit budget each time for a list nobody asked for. |
| D-M4a-21 | `metamusic oidc probe` (operator CLI) runs the real authorization-code flow with go-sdk's client against the live protected-resource metadata and provider (paste-the-redirect, no browser automation), prints a **redacted claims report** (iss, aud, typ, alg, kid, scopes, client_id, `mm_user` present, lifetime) — never a token — and ends with one `POST /mcp` carrying its token, which must answer 403 `CONNECTOR_FORBIDDEN` (the probe client is deliberately not allow-listed). | De-risks the SSO restart and the first real connector test; uses the same client code path as the CI acceptance test; the final round trip proves the **server** (not only the operator's machine) reaches the provider's keys and verifies signature, `iss`, `aud` and `exp` — without creating a grant. |
| D-M4a-22 | *Withdrawn in revision 2* (was: an opt-in Subsonic request recorder and recorded client fixtures). The synthetic fixtures (C117.5) stay; a recorder is reconsidered only if the device checklist finds client quirks they miss. | Not a roadmap or design §9 item; it wrote query-string-derived data destined for a public repository, needed an unspecified container mount and made the definition of done depend on operator recordings (YAGNI and privacy). |
| D-M4a-23 | `tracks.status_set_at` is a new mutable column (backfilled with `created_at`), set by `library.InsertTake` and by every status change in `judgments.UpdateTrack` (RFC 3339, the width of `created_at` and `judgments.stamp()`; `UpdateTrack` already writes `status_set_by`). | Subsonic `starred` needs a timestamp; recomputing it from the audit log would couple the bridge to audit retention. |
| D-M4a-24 | **Changed by the operator (M4a-X0, 2026-10-15; replaces "human-only parity exceptions").** Connector and Subsonic-client management are **not** human-only: `create_subsonic_client`, `revoke_subsonic_client`, `list_subsonic_clients`, `list_connector_grants` and `set_connector_grant` are ordinary registry operations (`/api/…`, `human_only = false`, MCP `generic`) that need scope **`admin`**. Humans have it; a PAT agent has it only when the operator gave that token `admin`; a paired device never has it (read, write); an OAuth connector never has it (grant scopes are `read`/`write`/`generate`, CHECK in C103). Every call is audited by the gate and sends one ntfy push naming the caller (and, for changes, the target label; **assumption A-1:** a human's own `list_*` call sends no push — confirm at the next sign-off). Agents never see `username`, `subject_hint` or unmasked principal keys (D-M2a-19). The **global caps stay human-only** — `set_spend_caps`, `create_token`, `revoke_token` keep `/human/api/` — so a connector budget can never exceed the human cap: `CheckCaps` always checks the global batch ceiling and month before any per-principal day. Agent counterparts unchanged: `whoami` (own grant scopes) and `get_budget` (own grant limits, `CapsView.connectors`). | Design D19 made these human-only; the operator chose agent reach with notification instead, while the money limits stay where an agent cannot move them. |
| D-M4a-25 | **Lossy and reference tracks (M2c; rev 3).** `tracks.lossy = 1` has an MP3 master, no FLAC and no `mp3_320` rendition (D-M2c-3/7). Subsonic: `suffix mp3`, `contentType audio/mpeg`, no `transcodedSuffix`, no `bitDepth`, `bitRate` = round(bytes·8 / duration / 1000); **every** `stream` request (`raw`, `flac`, `original`, `mp3`, any `maxBitRate`) answers the master MP3 unchanged — never a FLAC, never a transcode; `download` serves the master unchanged under `<slug>__<seed>.mp3`; `getCoverArt` works (seed 0 of the stub recipe). `get_download_link` returns a signed link to the master (its `sha256`/`bytes`, tags as stored); `get_offline_set` items carry `flac: null`, `lossy: true`, `mp3` = the master, and the web download manager takes the MP3 whenever `flac` is null; scrobbles record format `mp3`. The `write_variant` prompt refuses a track with `recipe_known = false` (422 `RECIPE_UNKNOWN`, `library.Service.RequireRecipeKnown`); `critique_track` works for every track (the context bundle already carries `lossy` and `recipe_known`). | M2c deferred "OpenSubsonic handling of lossy masters (M4a must serve `master` MP3 as the raw stream)"; the plan did not yet cover it, and `tagged` would otherwise try to write FLAC blocks into an MP3. |
| D-M4a-26 | **Numbering and migration (rev 3).** Contracts C102–C117; migration `0009_remote.sql` (committed as `0008_remote.sql` until L1 merges because the loader refuses gaps; M4a-X1 step 0 renames it); decisions D-M4a-1…26. | M3 took C68–C86, M2c C57–C67, L1 C87–C101 and migration 0008; the draft's numbers were written before them. |

---

## Contracts (all lanes build against these — do not change without updating this file)

### C102. Config, derived keys, data layout, leaf additions

New env (added to `config.Config` by M4a-B1 in `internal/config/config_m4a.go`: `loadM4a`, called from `load` next to
`loadM3`; `Load` and `LoadOffline` parse them; errors name the variable, never a value):
```go
OIDCIssuer        string       // MM_OIDC_ISSUER: "" (default) ⇒ hosted connectors disabled. Absolute https URL without
                               // query, fragment or userinfo; http only when the host is loopback (tests). Compared
                               // byte-for-byte with every token's iss (no trailing-slash normalisation).
OIDCJWKSURL       string       // MM_OIDC_JWKS_URL: "" ⇒ jwks_uri from <issuer>/.well-known/openid-configuration; same URL rules
OIDCClients       []OIDCClient // MM_OIDC_CLIENTS: "<client_id>=<label>[,…]"; ≥ 1 required when OIDCIssuer is set
OIDCScopes        []string     // MM_OIDC_SCOPES: space-separated scopes advertised in the protected-resource
                               // metadata; default "openid metamusic" (D-M4a-4); must contain "metamusic"

type OIDCClient struct{ ID, Label string } // ID ^[A-Za-z0-9._~-]{1,100}$, unique; Label 1..80 runes, no ',', '=' or controls
```
(There is no `MM_OIDC_ALG` — RS256 is a constant, D-M4a-3 — and no `MM_SUBSONIC_RECORD_DIR`; D-M4a-22 is
withdrawn.)
`Load` additionally errors when `OIDCIssuer` is set and `MM_TRUSTED_USERS` is empty (the `mm_user` claim
must name one of them, in both auth modes) or `PublicURL` is not `https://` (unless loopback — the OIDC
test rigs run on `http://127.0.0.1:<port>`, C117.1).
Constants (not configurable): `oidc.Alg = "RS256"`, `oidc.RequiredScope = "metamusic"`,
`oidc.UserClaim = "mm_user"`, `oidc.Leeway = 30 * time.Second`, `auth.SaltWindowMedia = 12 * time.Hour`,
`auth.SaltWindowAPI = 10 * time.Minute` (C107), `playlists.OfflineMaxItems = 100` (rev 3: there is no `offline` package;
`playlists.MaxItems` is 1000).

**Derived keys** (no new secret file): `SubsonicKey = HMAC-SHA256(Secrets.Key,
"metamusic/subsonic-password/v1")` (AES-256-GCM key for app passwords). Subsonic API keys are stored as
`hex(HMAC-SHA256(Secrets.Key, "subsonic\n" + key))`, like PATs and pairing codes.

**Data layout additions** (all under the M1 `*os.Root` on `DataDir`):
```
derived/covers/<aa>/<key>.png   cover art cache (C110.1), input-addressed, 0444, swept 30 days after creation, never backed up
derived/dl/<aa>/<key>.flac      tagged download copies (C110.2), input-addressed, 0444, swept 7 days after creation, never backed up
incoming/cover-<uuid>/          cover work dir (0700), removed on return
incoming/dl-<uuid>/             tagged-copy work dir (0700), removed on return
```
M1 C16.7 `library_orphans` additionally excludes `derived/covers/` and `derived/dl/` (caches); M4a-M1
changes the importer's orphan scan (`Importer.libraryOrphans`, `internal/importer/report.go`, which today skips
`derived/probe`, `derived/clips` and `derived/exports`), adds `TestOrphans_IgnoreCoverAndDownloadCaches`
(`internal/importer/orphans_m4a_test.go`) and records the amendment in the M1 plan. M2a backups copy only files referenced by `renditions` (D-M2a-5), so neither
cache is backed up (`TestBackup_ExcludesCoverAndDownloadCaches`).

**Leaf additions** (M4a-B1):
- `internal/reqinfo`: `Transport` gains `Subsonic = "subsonic"` and `OAuth = "oauth"` (the audit `transport` column is
  Go-validated, so no DDL);
  ```go
  func ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr
  // RemoteAddr's address unless it is inside a trusted prefix; then the right-most X-Forwarded-For entry
  // that is not itself trusted (all trusted or unparsable ⇒ RemoteAddr). Never trusts XFF from an untrusted peer.
  ```
- `internal/redact`: `String` additionally masks `mms_…` keys (`credRE` gains `s`: today `mm[pdc]_…`; `bearRE`, `runRE` and
  `queryRE` stay), JWS-compact strings
  (`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*` ⇒ `<jwt>`) and `key=value` pairs whose key is
  `t`, `s`, `p`, `apiKey`, `code`, `code_verifier`, `refresh_token`, `access_token` or `client_secret`
  in form-encoded text (`TestRedact_M4aPatterns`).
- `internal/listening` (M2a C36): the Go-validated listen-event `source` set (`listening.sources`) gains `subsonic` (B1 owns
  this one-line change; A1 owns the rest of the package in Stage 2). `internal/judgments` (M2a C31): `commentSrcs` gains
  `subsonic` the same way (B1).
- `internal/ratelimit`: classes `subsonic` {1200, 300}, `subsonic_auth_fail` {10, 5},
  `subsonic_auth_fail_global` {60, 20} join `ratelimit.Default` (all scaled by `MM_RATE_SCALE`); `Allow` skips a class missing
  from its `Config`, so `servertest`'s unlimited config (B2) lists them too and the throttling tests pass `Options.Limits`
  explicitly; and
  ```go
  func (l *Limiter) Peek(key string, class Class) (ok bool, retryAfter time.Duration) // ≥ 1 token available; consumes nothing; a class missing from the config ⇒ ok
```

### C103. Migration `internal/store/migrations/0009_remote.sql` (full DDL) and write SQL

As before, `CHECK` constraints encode only invariants that never change; growing enumerations (formats,
playlist roles, listen/comment sources, audit transports) are validated in Go.

Timestamps (rev 3): `tracks.status_set_at` and the offline-set row use the RFC 3339 second-precision form of `created_at` and
`judgments.stamp()` (`2026-10-15T10:00:00Z`); the new M4a tables are written with `store.FormatTime` (the M3 form,
`….000Z`). The two widths are never compared as strings across columns; `starred` orders by `status_set_at` alone.

```sql
-- 0009_remote.sql — M4a: connector grants, Subsonic clients, salts, bookmarks, play queue,
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
```

**Write SQL (binding):**
- Grant upsert (`connectors.Store.Ensure`, C106): `SELECT … WHERE client_id=? AND subject=?` first (a hit
  ⇒ done, no id generated); on a miss `INSERT INTO connector_grants(id, client_id, subject,
  username, label, scopes, created_at, updated_at, updated_by) VALUES(?,?,?,?,?,'read,write',?,?,'system:oauth') ON
  CONFLICT(client_id, subject) DO NOTHING RETURNING id` (a returned row ⇒ created), then the `SELECT`
  again (a lost race ⇒ the other writer's row, `created=false`).
- Grant touch: `UPDATE connector_grants SET last_used_at=? WHERE id=? AND (last_used_at IS NULL OR
  last_used_at < ?)` (at most once a minute).
- Grant set (C106): `UPDATE connector_grants SET scopes=?, daily_budget_micro_usd=?, auto_approve_takes=?,
  auto_approve_micro_usd=?, revoked_at=?, revoked_by=?, updated_at=?, updated_by=? WHERE id=?` plus one
  M3 `spend_cap_changes` row (`target = 'connector:<id>'`, before/after JSON of the limits) when a limit
  changed, in the same transaction. M3 C69's comment on `spend_cap_changes.target` becomes
  `-- 'global' | 'token:<id>' | 'connector:<id>'` (comment only; no DDL change, M4a-C2 records it).
- Subsonic client insert; revoke `UPDATE subsonic_clients SET revoked_at=?, revoked_by=? WHERE id=? AND
  revoked_at IS NULL`; touch `UPDATE subsonic_clients SET last_seen_at=?, last_client=? WHERE id=? AND
  (last_seen_at IS NULL OR last_seen_at < ? OR last_client IS NOT ?)`.
- Salt sighting: `INSERT INTO subsonic_salts(client_id, salt_sha256, first_seen_at) VALUES(?,?,?) ON
  CONFLICT DO NOTHING`, then `SELECT first_seen_at …`; prune `DELETE FROM subsonic_salts WHERE
  first_seen_at < ?` with `now − SaltWindowMedia − 1 h` (hourly, `serve` only).
- Bookmark upsert: `INSERT INTO subsonic_bookmarks(track_id, position_ms, comment, comment_id, created_at,
  updated_at, updated_by, deleted_at) VALUES(?,?,?,?,?,?,?,NULL) ON CONFLICT(track_id) DO UPDATE SET
  position_ms=excluded.position_ms, comment=excluded.comment, comment_id=COALESCE(excluded.comment_id,
  subsonic_bookmarks.comment_id), updated_at=excluded.updated_at, updated_by=excluded.updated_by,
  deleted_at=NULL`; delete `UPDATE subsonic_bookmarks SET deleted_at=?, updated_at=?, updated_by=? WHERE
  track_id=? AND deleted_at IS NULL`.
- Play queue: `INSERT INTO subsonic_play_queue(id, …) VALUES(1, …) ON CONFLICT(id) DO UPDATE SET
  track_ids_json=excluded.track_ids_json, current_track_id=excluded.current_track_id,
  position_ms=excluded.position_ms, changed_at=excluded.changed_at, changed_by=excluded.changed_by`.
- Status timestamp (amends M2a C31 and M1 C17): `judgments.UpdateTrack` writes `status_set_at=?` in the
  same `UPDATE` whenever `status` changes; `library.InsertTake` writes `status_set_at = created_at`.
- `TestNoDeleteSQL` (M2a; `deleteAllowlist` in `internal/store/nodelete_test.go`) allowlist becomes `{idempotency, human_sessions, subsonic_salts}`;
  `TestNoReplaceSQL` and the run-it-twice rule cover every new write.

### C104. Principals, middleware and path classes (amends M0 C3, M2a C26)

```go
type Principal struct {
    // … M2a fields (Kind, ID, Name, Scopes, Via, StepUpAt) …
    Client string // Via "oauth": the OAuth client_id; Via "subsonic": the sanitised `c` parameter; else ""
}
// Via gains "oauth" (Kind agent, D-M4a-1) and "subsonic" (Kind device, D-M4a-8).

type ConnectorVerifier interface {
    VerifyConnector(ctx context.Context, bearer string) (Principal, error)
}
type Verifiers struct {
    Tokens     TokenVerifier
    Sessions   SessionVerifier
    Devices    DeviceVerifier    // M2a; nil interface ⇒ device auth off
    Connectors ConnectorVerifier // nil interface ⇒ OAuth connectors off (MM_OIDC_ISSUER unset)
}
func Middleware(cfg config.Config, sec config.Secrets, v Verifiers) func(http.Handler) http.Handler
// (replaces the M2a signature Middleware(cfg, sec, tokens, sessions, devices); httpserver is the only caller)

var (
    ErrConnectorForbidden = errors.New("auth: connector not allowed")   // client not allow-listed, user claim missing/untrusted
    ErrConnectorRevoked   = errors.New("auth: connector grant revoked")
    ErrUnavailable        = errors.New("auth: verifier dependency unavailable")
)
type ScopeError struct{ Scope string } // Is(ErrInsufficientScope)
var ErrInsufficientScope = errors.New("auth: insufficient token scope")
```

**Row 1 of the C3 table is amended** (rows 2–7 unchanged):

| # | Condition | Result |
|---|---|---|
| 1a | `Authorization: Bearer mmp_…` | PAT, unchanged (M0) |
| 1b | Any other bearer **and** path is `/mcp` or under `/mcp/` **and** `v.Connectors != nil` | **Shape test first:** the value must be ≤ 8192 bytes with exactly two `.`; failing it (an opaque token, a garbled or truncated JWT) ⇒ **401** problem `UNAUTHENTICATED`, `WWW-Authenticate: Bearer error="invalid_token", resource_metadata="<PublicURL>/.well-known/oauth-protected-resource/mcp"` without calling the verifier — never a bare `Bearer`, so a client holding a bad token restarts OAuth. Otherwise `VerifyConnector`: success ⇒ the agent principal (`Via "oauth"`). `ErrInvalidToken` ⇒ **401** with the same `invalid_token` + `resource_metadata` challenge. `*ScopeError` ⇒ **403** problem `INSUFFICIENT_SCOPE`, `WWW-Authenticate: Bearer error="insufficient_scope", scope="metamusic", resource_metadata="…"`. `ErrConnectorForbidden` ⇒ **403** `CONNECTOR_FORBIDDEN` (no `WWW-Authenticate`, so clients do not loop). `ErrConnectorRevoked` ⇒ **403** `CONNECTOR_REVOKED`. `ErrUnavailable` ⇒ **503** `UNAVAILABLE` `{component:"oidc"}`. Anything else ⇒ 500 `INTERNAL` (logged through `redact.Error`). Every refusal logs one line with status and problem code (never the token), so the operator can confirm e.g. `CONNECTOR_REVOKED` in the app logs (DC-M4a-8; refusals at the middleware write no audit row). |
| 1c | Anything else with an `Authorization` header | 401 `UNAUTHENTICATED` + `WWW-Authenticate: Bearer`, unchanged — includes a JWT on `/api/*` (D-M4a-2), any bearer on `/rest/`, and any non-PAT bearer on `/mcp` while connectors are off |

(rev 3) Baseline: `httpserver` already answers an **anonymous** `/mcp` request with 401 `UNAUTHENTICATED` and
`WWW-Authenticate: Bearer resource_metadata="<PublicURL>/.well-known/oauth-protected-resource/mcp"`; the middleware's row 1 today
answers every non-`mmp_` bearer with a bare `Bearer` challenge. M4a changes only the latter, on `/mcp`.

`TestTrustMatrix_M4a` (`internal/auth/middleware_m4a_test.go`) extends M0's programmatic matrix (`TestTrustMatrix`) with: {PAT, valid JWT, expired JWT, JWT missing
`metamusic`, JWT of an unlisted client, JWT of an unlisted client missing `metamusic`, revoked grant,
non-canonical base64url JWT, opaque non-JWT bearer, 3-segment garbage, 9000-byte JWT} × {`/mcp`, `/mcp/x`,
`/api/tracks`, `/rest/ping`, `/media/x`} × {connectors on/off} ⇒ expected status, problem code and
`WWW-Authenticate`. Binding rows: with connectors on, every non-PAT bearer on `/mcp` that is not a valid
token of a listed client answers 401 `invalid_token` **with** `resource_metadata` (shape failures
included) or one of the 403s above; the unlisted client missing `metamusic` gets 403
`CONNECTOR_FORBIDDEN`, never `insufficient_scope`.

**Classify:** `/sw.js` ⇒ `ClassPublic` (exact path; D-M4a-18). `/.well-known/*` and `/mcp*` stay
`ClassMachine`. `TestClassify_M4a` (`/sw.js` public; `/sw.jsx`, `/sw.js/x` UI;
`/.well-known/oauth-protected-resource/mcp` machine).

**Rendering and label rules:** `api.RefFor` needs no change (connectors are agents; their `Name` is the
client label, never personal data). M2a's dispatch client label (C30) for an OAuth principal is
`reqinfo.SanitizeClient("oauth:" + p.Client + "/" + <MCP client label's first product token>)`, so audit
rows read e.g. `oauth:metamusic-claude/claude-ai`. `revoke_own_token` (M2a C38) refuses every principal
whose `Via` is not `"token"` with 403 `NOT_A_TOKEN` (`TestRevokeOwnToken_RefusesOAuthPrincipal`; today the handler checks only
`Kind != agent`, which would let a connector reach `Tokens.Revoke` with a grant id). `whoami`'s `via` doc lists `oauth` and `subsonic`.

### C105. OIDC resource-server verification (`internal/oidc`) and the stub provider (`oidctest`)

```go
package oidc

const Alg = "RS256"                    // the only JWS alg accepted (D-M4a-3)
const RequiredScope, UserClaim = "metamusic", "mm_user"
const Leeway = 30 * time.Second
type Config struct {
    Issuer, JWKSURL string             // from C102
    Resource string                    // <PublicURL>/mcp (the only accepted audience)
    Clients  map[string]string         // client_id → label
}
type Claims struct {
    Subject, ClientID, User, JTI string
    Scopes   []string
    Audience []string
    IssuedAt, Expires time.Time
}
var (
    ErrInvalid       = errors.New("oidc: invalid token")        // mapped to auth.ErrInvalidToken
    ErrClientUnknown = errors.New("oidc: client not allowed")   // mapped to auth.ErrConnectorForbidden
    ErrUnavailable   = errors.New("oidc: keys unavailable")     // mapped to auth.ErrUnavailable
)
type MissingScopeError struct{ Scope string }                    // mapped to *auth.ScopeError
type Status struct{ Keys int; FetchedAt *time.Time; LastError string /* code only */ }
func NewVerifier(c Config, httpc *http.Client, now func() time.Time) (*Verifier, error) // never fetches
func (v *Verifier) Prefetch(ctx context.Context) // discovery + JWKS once; run in a goroutine at startup (below)
func (v *Verifier) Verify(ctx context.Context, token string) (Claims, error)
func (v *Verifier) Status() Status
```

**Verification, exact order** (`TestVerify_Table` has one row per step and failure; every failing row
asserts the error class, and that no JWKS fetch happened for rows that fail before step 3):
1. Shape: ≤ 8192 bytes; exactly three segments; each segment unpadded base64url ⇒ else `ErrInvalid`.
2. Header (JSON object): `alg` equals `Alg` (`RS256`) byte-exact (`none`, `HS256`, `RS512`, `PS256`,
   `ES256` … ⇒ `ErrInvalid`); `typ` equals `at+jwt` or `application/at+jwt` case-insensitively
   (missing or `JWT` ⇒ `ErrInvalid`); `kid` non-empty; a `crit` member ⇒ `ErrInvalid`; `jku`, `x5u`,
   `jwk`, `x5c` are ignored (never fetched, never used — `TestVerify_HeaderKeyMaterialIgnored` serves a
   token whose embedded `jwk` holds the signing key and whose `kid` is unknown ⇒ `ErrInvalid`).
3. Key: JWKS entry with that `kid`, `kty` RSA, `use` = `sig` or absent, key `alg` absent or `RS256`,
   modulus ≥ 2048 bits and `e` = 65537 (any other `kty` is skipped at parse time). Unknown `kid` ⇒
   **one** JWKS refresh (at most one per 60 s, deduplicated across concurrent callers), then `ErrInvalid`.
4. Signature: `jwt.Parse` with `jwt.WithValidMethods([]string{Alg})` and `jwt.WithStrictDecoding()`
   (non-canonical base64url, e.g. non-zero trailing bits, ⇒ `ErrInvalid`).
5. Claims: `iss` == `Config.Issuer` byte-exact; `aud` (string or array) contains `Config.Resource`
   byte-exact; `exp` present and `now ≤ exp + Leeway`; `iat` present and `iat ≤ now + Leeway`; `nbf`, if
   present, `≤ now + Leeway`; `sub` 1–255 bytes; `client_id` non-empty.
6. `client_id` ∈ `Config.Clients` ⇒ else `ErrClientUnknown` (**before** the scope check, so an
   unlisted client is never sent a step-up challenge; D-M4a-6).
7. Scope: the union of `scope` (space-delimited string) and `scp` (array of strings) contains
   `RequiredScope` ⇒ else `*MissingScopeError{"metamusic"}`.
8. `mm_user`: a non-empty string ⇒ `Claims.User` (absent ⇒ `""`; the trusted-user check is C106's).

**Discovery and JWKS:** with `JWKSURL` empty, `GET <issuer>/.well-known/openid-configuration` once
(`issuer` in the document must equal `Config.Issuer`, else `ErrUnavailable` + log code
`issuer_mismatch`). Every fetch: the injected client, 5 s timeout, `Accept: application/json`, redirects
refused, HTTP 200 only, body ≤ 64 KiB, https unless loopback. Keys are cached; the cache refreshes on the
first `Verify` after 1 h, and on an unknown `kid` (step 3). A failed refresh keeps the previous keys for
up to 24 h after their fetch; with no usable keys ⇒ `ErrUnavailable` (503 at the edge of C104). Logs
carry error codes (`jwks_http_5xx`, `jwks_too_large`, `jwks_parse`, …), never tokens or claims.
**Eager fetch:** `serve` and `servertest` (whenever the verifier exists) run `go v.Prefetch(ctx)` with the
process lifetime context right after `app.Build`; it fetches discovery and JWKS once, logs only error
codes, never blocks startup and never fails it. `Status()` (shown as `OIDCStatus.keys` on
`/human/connectors`) reports the result before any client connected, so the operator sees "0 keys,
`jwks_http_5xx`" instead of discovering it through a failed hosted login (M4a-X2 step 6).
`TestJWKS_*`: refresh rate-limited (100 concurrent unknown-kid verifies ⇒ 1 fetch), rotation (new `kid`
⇒ verifies after one refresh), stale keys served ≤ 24 h, oversize ⇒ error, redirect refused, RSA 1024
and `e=3` keys skipped, EC/OKP/oct keys skipped; `TestVerifier_PrefetchStatus` (after `Prefetch`,
`Status().Keys ≥ 1` and `FetchedAt` set with no `Verify` call; a failing JWKS ⇒ `Keys 0`, `LastError`
a code, no panic, a later `Verify` retries).

**`internal/oidc/oidctest` (test-only; owner M4a-C1; added to `TestNoTestHelpersInBinary`):**
```go
type Client struct {
    ID           string
    Secret       string   // "" ⇒ public client (token_endpoint_auth_method none)
    AuthMethod   string   // "none" | "client_secret_post" | "client_secret_basic" — enforced like Authelia (one per client)
    RedirectURIs []string // exact match
    Audience     []string // allowed resources/audiences; requested_audience_mode implicit
    Scopes       []string // allowed scopes
}
type Options struct {
    Clients    []Client
    Users      map[string]string // username → opaque subject
    LoginAs    string            // the user the authorization endpoint authenticates (no UI); keys are RSA-2048, RS256
    AccessTTL  time.Duration     // default 1h
    RefreshTTL time.Duration     // default 720h
    Now        func() time.Time  // the rig clock
    ScopeClaim string            // "scope" (default) | "scp" | "both"
    UserClaim  bool              // emit mm_user when scope metamusic is granted (default true)
    IssParam   bool              // RFC 9207 iss on the authorization response (default true)
    Mutate     func(header, claims map[string]any) // per-token fault injection (nil ⇒ none)
}
type Provider struct{ URL string /* issuer */ }
func New(tb testing.TB, o Options) *Provider       // httptest server; Cleanup closes it
func (p *Provider) RotateKey()                      // new kid; old key stays published until DropOldKeys
func (p *Provider) DropOldKeys()
func (p *Provider) Mint(tb testing.TB, client, user string, scopes, aud []string) string // direct token for unit tests
func (p *Provider) Requests() []Recorded            // authorize/token parameters (secrets masked) for assertions
type Recorded struct{ Endpoint /* "authorize" | "token" */, GrantType, ClientID, Scope, Resource string; At time.Time }
func (p *Provider) Fetcher(tb testing.TB) auth.AuthorizationCodeFetcher // go-sdk fetcher: GET the URL without following redirects, parse code/state/iss
```
Endpoints mirror the provider's public layout: `/.well-known/openid-configuration` and
`/.well-known/oauth-authorization-server` (same document: `issuer`, `authorization_endpoint`
`/api/oidc/authorization`, `token_endpoint` `/api/oidc/token`, `jwks_uri` `/jwks.json`,
`response_types_supported [code]`, `grant_types_supported [authorization_code, refresh_token]`,
`code_challenge_methods_supported [S256]`, `token_endpoint_auth_methods_supported [client_secret_basic,
client_secret_post, none]`, `scopes_supported [openid, offline_access, profile, metamusic]` (the
**authorization server** lists `offline_access`, as the real provider does — that is what makes go-sdk
request it, D-M4a-4),
`authorization_response_iss_parameter_supported true`). Authorization: exact `redirect_uri`, `code`
only, PKCE S256 required, scopes ⊆ client scopes, `resource` (if sent) must equal or path-prefix-match an
allowed audience, else the RFC 6749 error redirect; success ⇒ 302 with `code`, `state` and `iss`. Token:
form-urlencoded only; code single-use, PKCE verified, client authenticated with **exactly** its
configured method (`invalid_client` otherwise); `refresh_token` only when `offline_access` was granted;
refresh rotation with reuse detection (reuse ⇒ `invalid_grant` and the whole family revoked). Access
tokens: header `{alg, kid, typ:"at+jwt"}`; claims `iss, sub, aud (array: the client's audience), client_id,
scope, iat, exp, jti` (+ `mm_user` when `metamusic` was granted). Its own tests (`TestStub_*`) pin each
rule so the acceptance tests cannot pass against a lenient stub.

### C106. Connector grants (`internal/connectors`), protected-resource metadata, spend limits

```go
package connectors

type Limits struct{ DailyBudgetMicro, AutoApproveMicro *money.MicroUSD; AutoApproveTakes *int } // nil ⇒ global default
type Grant struct {
    ID, ClientID, Subject, Username, Label string
    Scopes                                  []auth.Scope
    Limits                                  Limits
    CreatedAt, UpdatedAt                    time.Time
    LastUsedAt, RevokedAt                   *time.Time
    UpdatedBy, RevokedBy                    string
}
type SetIn struct{ Scopes []auth.Scope; Limits *Limits; Revoked *bool } // ≥ 1 non-nil field
func NewStore(st *store.Store, now func() time.Time, newID func() string) *Store
func (s *Store) Ensure(ctx context.Context, clientID, subject, username, label string) (g Grant, created bool, err error)
// SELECT by (client_id, subject) first; only on a miss newID() + the C103 upsert (a lost race ⇒ re-SELECT, created=false).
// The one insert path — used by the verifier (C106 step 3), seed-synthetic (C114), the librarytest fixture (C117.3)
// and the parity rig (C117.2). Implemented in M4a-B2 (not a stub).
func (s *Store) Get(ctx context.Context, id string) (Grant, error)          // ErrNotFound; implemented in M4a-B2
func (s *Store) List(ctx context.Context) ([]Grant, error)                  // newest last_used_at first, then created_at
func (s *Store) Set(ctx context.Context, by auth.Principal, id string, in SetIn) (Grant, error) // `by` must hold scope admin (the gate enforces it); C103 SQL
// (spend reads grant limits with its own SQL in `(*spend.Service).TokenLimitsFor`; connectors never imports spend's writers)

type VerifierOptions struct {
    OIDC         *oidc.Verifier
    Store        *Store
    TrustedUsers []string
    Labels       map[string]string // client_id → label (C102)
    Audit        *audit.Store
    Notify       notify.Notifier
    Now          func() time.Time
}
func NewVerifier(o VerifierOptions) *Verifier // implements auth.ConnectorVerifier
func PRMHandler(cfg config.Config) http.Handler
```

**`VerifyConnector(ctx, bearer)`**, exact order (`TestVerifyConnector_Order` with spies):
1. `oidc.Verify` — error mapping per C105 (`ErrInvalid` ⇒ `auth.ErrInvalidToken`, `MissingScopeError` ⇒
   `*auth.ScopeError`, `ErrClientUnknown` ⇒ `auth.ErrConnectorForbidden`, `ErrUnavailable` ⇒
   `auth.ErrUnavailable`).
2. `Claims.User` ∈ `TrustedUsers` (exact) ⇒ else `ErrConnectorForbidden` (log code `user_not_trusted` or
   `user_claim_missing`; never the value).
3. `Store.Ensure` (C103 upsert). A **created** row ⇒ one audit row (principal = the new connector principal,
   transport `oauth`, op `auth:connector_grant`, method `SYSTEM`, status 201, created ids `[grant id]`,
   `args_sha256` = sha256 of `{client_id}`) and `Notify` priority 3, title "Connector connected", body
   "<label> can now read and comment (generate is off)." (sanitised by M2a C37; no subject, no username).
4. `revoked_at` set ⇒ `ErrConnectorRevoked`.
5. Touch `last_used_at` (C103, best effort, 250 ms bounded context like M0's token touch).
6. Return `Principal{Kind: agent, ID: grant.ID, Name: Labels[client_id] (fallback grant.Label), Scopes:
   grant.Scopes, Via: "oauth", Client: client_id}`.

**Protected-resource metadata** (RFC 9728) is served **only** at
`GET /.well-known/oauth-protected-resource/mcp` (the path-suffixed form for the `/mcp` resource, RFC 9728
§3.1). The root `/.well-known/oauth-protected-resource` would describe the **origin** (§3.3), and go-sdk
(`protectedResourceMetadataURLs`, `oauthex.GetProtectedResourceMetadata`) rejects a root document whose
`resource` is `…/mcp`; the root path therefore answers 404 problem `NOT_FOUND` like every other
`/.well-known/*` path. The handler is our own `connectors.PRMHandler` (go-sdk's
`auth.ProtectedResourceMetadataHandler` answers non-GET with a `text/plain` 405 — breaking rule 5 — and
sets no `Cache-Control`); it marshals an `oauthex.ProtectedResourceMetadata` value once at construction:
GET/HEAD ⇒ 200 `application/json`, `Cache-Control: public, max-age=300`, `Access-Control-Allow-Origin: *`;
`OPTIONS` ⇒ 204 with `Access-Control-Allow-Origin: *`, `Access-Control-Allow-Methods: GET, HEAD, OPTIONS`,
`Access-Control-Allow-Headers: Content-Type`; any other method ⇒ 405 problem `METHOD_NOT_ALLOWED` with
`Allow: GET, HEAD, OPTIONS`.
```json
{"resource":"https://mm.example.com/mcp","authorization_servers":["https://auth.example.com"],
 "scopes_supported":["openid","metamusic"],"bearer_methods_supported":["header"],
 "resource_name":"metamusic"}
```
With `MM_OIDC_ISSUER` unset the document omits `authorization_servers` and `scopes_supported` (OAuth
clients stop with a clear "no authorization servers" error; PAT clients are unaffected) and M0's `/mcp`
401 is unchanged. `TestPRM_Golden` (the `/mcp` path in both modes; the root path ⇒ 404 problem; POST ⇒
405 problem with `Allow`; OPTIONS ⇒ 204 with the CORS headers; `Cache-Control` present),
`TestPRM_ResourceMatchesMCPURL` (go-sdk's `oauthex.GetProtectedResourceMetadata` accepts the document
for `<PublicURL>/mcp` when fetched from the URL go-sdk derives).

**Spend limits (amends M3 C69/C72.3/C72.6/C81; rev 3):**
```go
// internal/spend/caps.go (M4a-C2): the M3 method gains a connector branch — no new function, no new call site.
func (s *Service) TokenLimitsFor(ctx context.Context, q Querier, principalKey string) (TokenLimits, error)
// "agent:<id>" ⇒ the api_tokens row with that id, else the connector_grants row with that id (ids are UUIDv7 in disjoint
// tables); not found, or any other key prefix ⇒ TokenLimits{}. Never an error for "not found".
```
**"Zero" means all-nil:** `TokenLimits{}` has every field nil, which M3 already reads as "use the global
default" (`AgentDailyMicro`, the global auto-approve limits) — never $0. A limit of $0 is only ever an
explicit non-nil `0` stored on a token or grant.
Every M3 site already calls this one method with a principal key, inside the caller's transaction (M3 rule): the create
transaction (`generation/create.go`), estimates (`generation/estimate.go`), `get_budget` (`generation/budget.go`) with
`p.Key()`, and `spend.ReserveTopUp` (`spend/topup.go`) with the batch owner's `batches.principal_id` — every runner claim
`attempt_overhead`, `topup_ambiguous`, `topup_requeue` and `topup_price`. `PrincipalDaily` and `ReserveTopUp` already treat
every `agent:` key as an agent, and a connector principal is `Kind agent`. M4a-C2 therefore adds the connector branch and its
tests and edits no call site; without the branch a connector-owned batch's top-ups would be checked against the global agent
daily, not the grant's limit.
`generation.CapsView` (filled by `capsView`, which selects the grants with its own SQL) and `api.CapsView` (`types_m3.go`) gain `connectors: [{grant_id, label,
daily_budget_micro_usd|null, auto_approve_takes|null, auto_approve_micro_usd|null}]` (human callers: all;
an OAuth caller: its own grant only; a PAT caller: `[]`). The ntfy "Approval needed" message names the
connector by label (it already uses the principal's name). Tests: `TestTokenLimitsFor_Table` (PAT id ⇒ its token limits; grant id ⇒ its grant limits; unknown id, `human:…`
and `system:…` ⇒ all-nil; a connector principal and its key give the same limits),
`TestSpend_ConnectorDailyCap` (grant daily $0.20; a variant whose ceiling is $0.30 ⇒ 402 cap `daily`),
`TestSpend_ConnectorTopUpUsesGrantDaily` (grant daily $0.20, global agent daily higher: the create fits
under $0.20; a later `ReserveTopUp` (e.g. the next claim's `attempt_overhead`) that would take the
owner's day past $0.20 ⇒ `CapError` cap `daily` with `limit_micro_usd 200000` and the job fails
`BUDGET_EXCEEDED`; the same batch owned by a PAT with a nil daily passes),
`TestSpend_ConnectorAutoApproveZero` (grant auto-approve takes 0 ⇒ every connector batch needs
approval), `TestGetBudget_ConnectorOwnLimits`.

### C107. Subsonic credentials (`internal/auth/subsonic.go`)

```go
const SubsonicAPIKeyPrefix = "mms_"   // + 52 lowercase base32 chars of 32 random bytes (gitleaks rule, C116)
const SaltWindowMedia = 12 * time.Hour  // stream, download, getCoverArt (D-M4a-9)
const SaltWindowAPI = 10 * time.Minute  // every other endpoint
func SaltWindowFor(endpoint string) time.Duration // "stream" | "download" | "getCoverArt" ⇒ SaltWindowMedia; else SaltWindowAPI
const MinSaltLen, MaxSaltLen = 6, 64

type SubsonicClient struct {
    ID, Name, Username, DefaultFormat, APIKeyPrefix, CreatedBy, LastClient, RevokedBy string
    Scopes                 []Scope
    HasAPIKey, HasPassword bool
    CreatedAt              time.Time
    LastSeenAt, RevokedAt  *time.Time
}
type NewSubsonicClient struct {
    Name, Username, DefaultFormat string // DefaultFormat "raw" (default) | "mp3"
    Write, APIKey, Password       bool   // at least one of APIKey, Password
}
type SubsonicSecrets struct{ APIKey, Password string } // plaintext, returned exactly once

var (
    ErrSubsonicCredentials = errors.New("subsonic: wrong username or password") // protocol error 40
    ErrSubsonicAPIKey      = errors.New("subsonic: invalid api key")            // protocol error 44
    ErrSaltExpired         = errors.New("subsonic: salt outside its window")    // protocol error 40, own message
    ErrUsernameTaken       = errors.New("subsonic: username in use")            // 409 USERNAME_TAKEN
)
func NewSubsonicStore(st *store.Store, secretKey []byte, now func() time.Time, newID func() string, rnd io.Reader) *SubsonicStore
func (s *SubsonicStore) Create(ctx context.Context, by Principal, in NewSubsonicClient) (SubsonicClient, SubsonicSecrets, error) // by holds scope admin (the gate enforces it)
func (s *SubsonicStore) List(ctx context.Context) ([]SubsonicClient, error)            // never returns key material
func (s *SubsonicStore) Revoke(ctx context.Context, by Principal, id string) (SubsonicClient, error) // idempotent; ErrNotFound
func (s *SubsonicStore) VerifyAPIKey(ctx context.Context, key, clientParam string) (Principal, error)
func (s *SubsonicStore) VerifyToken(ctx context.Context, username, token, salt, clientParam string, window time.Duration) (Principal, error)
// window = SaltWindowFor(<endpoint of this request>), passed by the Subsonic handler
func (s *SubsonicStore) PruneSalts(ctx context.Context) (int, error)                   // first_seen_at < now − SaltWindowMedia − 1 h
```
- **Generation:** API key `mms_` + base32(32 random bytes); app password = 24 lowercase base32 chars
  (120 bits) — no look-alike handling needed because it is pasted, not typed. Username default = slug of
  the name (`[a-z0-9._-]`, 3–32, first char alphanumeric); a live duplicate ⇒ `ErrUsernameTaken`.
- **Storage:** API key ⇒ `api_key_hmac` + `api_key_prefix`; password ⇒ AES-256-GCM with `SubsonicKey`,
  random 12-byte nonce, AAD `"subsonic-password\n" + id` (a ciphertext copied to another row fails to
  open: `TestSubsonicPassword_AADBindsRow`).
- **`VerifyAPIKey`:** prefix and length check, then HMAC lookup of a live row ⇒ principal; else
  `ErrSubsonicAPIKey`.
- **`VerifyToken`:** `token` must be 32 lowercase hex; salt 6–64 printable ASCII ⇒ else
  `ErrSubsonicCredentials`. Look up the live client by username — when it is unknown or has no password,
  still decrypt a fixed dummy ciphertext so timing does not reveal usernames. Compute
  `hex(md5(password + salt))`, compare with `subtle.ConstantTimeCompare`. On match: the salt sighting
  (C103; `salt_sha256 = hex(sha256(salt))`); `now − first_seen_at > window` ⇒ `ErrSaltExpired`. The
  window belongs to the **current** request: a salt first seen on `stream` and replayed on `setRating`
  11 minutes later is expired, while the same salt on `stream` stays valid for 12 h.
- **Principal:** `{Kind: device, ID: client.ID, Name: client.Name, Scopes: read | read+write, Via:
  "subsonic", Client: reqinfo.SanitizeClient(clientParam)}`; touch `last_seen_at`/`last_client` at most
  once a minute (or when the `c` value changes).
Coverage: these paths count toward `internal/auth` ≥ 95 %.

### C108. OpenSubsonic protocol layer (`internal/subsonic`)

```go
package subsonic

const APIVersion = "1.16.1"
type Options struct {
    Cfg       config.Config
    Version   string               // serverVersion
    Store     *store.Store         // read pool for catalog SQL (C109.1)
    Data      *os.Root
    Lib       *library.Service
    Judgments *judgments.Service
    Playlists *playlists.Service
    Listening *listening.Service
    Covers    CoverSource          // *cover.Service in production (C110.1); fakes in lane tests
    Downloads DownloadSource       // *tagged.Service in production (C110.2); fakes in lane tests
    Creds     *auth.SubsonicStore
    Audit     *audit.Store
    Limiter   *ratelimit.Limiter
    Now       func() time.Time
    Rand      func() uint64        // getRandomSongs / getAlbumList type=random; tests inject a fixed sequence
    FailDelay time.Duration        // default 1 s
    Sleep     func(ctx context.Context, d time.Duration) error // nil ⇒ context-aware timer wait; tests inject a
                                   // no-op (servertest via app.Options.SubsonicSleep) or a blocking fake
}
func NewHandler(o Options) http.Handler // mounted by httpserver at /rest/ (ClassMachine; mm-media router)

type CoverSource interface {
    EnsureTrack(ctx context.Context, trackID string, size int) (serve.Object, string, error)
    EnsureAlbum(ctx context.Context, rootID string, size int) (serve.Object, string, error)
}
type DownloadSource interface {
    Ensure(ctx context.Context, trackID string) (tagged.Download, error)
}
```
(The interfaces let the Subsonic lane test media endpoints before the Media lane lands; Stage 3 wires
the real services.)

**C108.1 Routing.** Path `/rest/<name>` or `/rest/<name>.view`; `<name>` matches `^[A-Za-z0-9]+$` and is
case-sensitive (Subsonic names are camelCase). Methods: GET, HEAD (body discarded), POST with
`Content-Type: application/x-www-form-urlencoded` (the `formPost` extension; body ≤ 64 KiB, else error
0). Other methods ⇒ **405** with `Allow: GET, HEAD, POST` and an error envelope (D-M4a-16). Parameters =
query values followed by form values (repeatable parameters such as `id`, `songId` keep both, in that
order; single-valued getters take the first). Unknown endpoint ⇒ error 0 "unsupported endpoint".

**C108.2 Common parameters.** `f` ∈ `xml` (default) | `json`; `jsonp` (or any other) ⇒ error 0 "format
not supported" in JSON. `v` and `c` are required (error 10) except on `getOpenSubsonicExtensions`; `v`
parses as `X.Y[.Z]`; a major version above 1 ⇒ error 30; nothing else about `v` is checked.

**C108.3 Authentication, exact order** (`TestSubsonicAuth_Order` with spies):
1. `getOpenSubsonicExtensions` needs no credentials (the extension's rule).
2. `Limiter.Peek(clientIP, subsonic_auth_fail)` empty ⇒ **HTTP 429**, `Retry-After`, error 0 "too many
   failed attempts" (before any credential work). **Only the per-IP class is peeked here**; the global
   class is never consulted before verification.
3. `apiKey` present: any of `u`, `p`, `t`, `s` also present ⇒ error **43**; else `VerifyAPIKey` ⇒ error
   **44** on failure.
4. Else `u` + `t` + `s`: `VerifyToken(…, auth.SaltWindowFor(endpoint))` ⇒ error **40** on failure
   (`ErrSaltExpired` uses the message "salt expired: use a new salt").
5. Else `u` + `p` ⇒ error **42** "password authentication is not supported; use an API key or token
   authentication" (D-M4a-8).
6. Else ⇒ error **10**.
Every failure in 3–4 then consumes one token of `subsonic_auth_fail` (per `reqinfo.ClientIP`) and
`Allow`s one of `subsonic_auth_fail_global`: global class empty ⇒ this failing request gets **HTTP 429**
at once (no sleep); otherwise it sleeps `FailDelay` through `Options.Sleep` (context-aware; at most 16
failing requests sleep concurrently, the rest get the 429 at once) and answers its protocol error.
Valid credentials therefore **always** pass steps 3–4, however many failures arrive from other IPs.
Success ⇒ `Limiter.Allow(principal.Key(), subsonic)` (exhausted ⇒ 429) ⇒ the endpoint's scope check
(write endpoints without `write` ⇒ error **50**).
(rev 3) A request that carries an `Authorization` header never reaches this flow: the middleware (row 1) answers 401
`UNAUTHENTICATED` with a problem body — Subsonic clients never send one, so a PAT is refused on `/rest/` before any credential work.

**C108.4 Envelope.** XML: `<?xml version="1.0" encoding="UTF-8"?>` + `<subsonic-response
xmlns="http://subsonic.org/restapi" status="ok" version="1.16.1" type="metamusic" serverVersion="<ver>"
openSubsonic="true">…</subsonic-response>`, `Content-Type: text/xml; charset=utf-8`. JSON:
`{"subsonic-response":{"status":"ok","version":"1.16.1","type":"metamusic","serverVersion":"<ver>","openSubsonic":true,…}}`,
`Content-Type: application/json`. Errors: `status="failed"` + `error{code, message}` (fixed English
messages that never echo parameters). Non-binary responses carry `Cache-Control: no-store`,
`X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`.

**C108.5 Structs and attribution.** `internal/subsonic/types.go` holds every response type with both
`json` and `xml` tags (scalars as XML attributes, lists as repeated child elements, exactly as the
OpenSubsonic examples show). File header (binding text): "Field names and semantics follow the
OpenSubsonic API description (third_party/opensubsonic/openapi.json, commit <sha>, Apache-2.0,
© OpenSubsonic contributors). Written for this project from that description; no code from GPL-licensed
Subsonic servers or client libraries." `third_party/opensubsonic/` holds `openapi.json` (pinned commit),
`LICENSE` (Apache-2.0 text) and `NOTICE.md` (source URL, commit, date, what it is used for); the root
`THIRD_PARTY_NOTICES.md` lists it. Nothing is generated from the GPL clients' code; the client fixtures
(C117.5) describe request **shapes** only.

**C108.6 Advertised extensions** (`getOpenSubsonicExtensions`, sorted by name): `apiKeyAuthentication
[1]`, `formPost [1]`, `playbackReport [1]`, `songLyrics [1]`.

**C108.7 Audit and logging.** Write endpoints (C109, scope `write`) insert one audit row after the service
call: principal (device), transport `subsonic`, client `subsonic:<sanitised c>`, op
`subsonic:<endpoint>`, method, `args_sha256` = sha256 of the canonical sorted parameters **without**
`u, p, t, s, apiKey, c, v, f`, status 200, `error_code` = `""` or `SUBSONIC_<code>`, created ids from
M2a's `audit.WithCollector` (comments and listen events). Reads write nothing but the auth side effects
(salt sighting, `last_seen_at`). Every request logs one slog line: endpoint, protocol status/code, HTTP
status, principal kind, duration — never the query, the body or a header value.

**C108.8 Media responses** (`stream`, `download`, `getCoverArt`) go through `serve.ServeObject` (C110.3)
with `Cache-Control: private, no-cache` (stream/download) or `private, max-age=86400` (cover art), so
Range, If-Range, HEAD and `multipart/byteranges` behave exactly as `/media/`
(`TestSubsonic_StreamRangeMatrix` reuses M1's `TestAcceptance_M1_MediaRangeMatrix` table).

**C108.9** *Withdrawn in revision 2* (the request recorder, D-M4a-22). There is no recorder, no
`MM_SUBSONIC_RECORD_DIR` and no recorded fixture; the number stays reserved.

### C109. Subsonic domain mapping and endpoints

**C109.1 Catalog SQL** (`internal/subsonic/catalog.go`, read pool only, owned by the Subsonic lane): songs
= tracks; albums = lineages (`root_track_id`); the folder rule of D-M4a-10 is one `WHERE` fragment;
human stars from M2a `ratings` (`source='human'`); plays and last play from `listen_events` of
`human`/`device` principals (`start` events); `starred` from `status_set_at` of `final` tracks.
Deterministic orders everywhere (every `ORDER BY` ends with `id`).

**Child (song)** fields: `id, parent ("al-<root>"), isDir false, title, album (root's current title),
artist "metamusic", track (position in the lineage, 1-based), year (recorded_at year), genre (first style
term, omitted if none), coverArt ("tr-<id>"), size (master bytes, or MP3 bytes when the client's
default format is mp3), contentType ("audio/flac" | "audio/mpeg"), suffix ("flac" | "mp3"),
transcodedContentType/transcodedSuffix (only when the default format is mp3), duration (int seconds,
rounded), bitRate (kbps from size and duration; 320 for MP3), bitDepth, samplingRate, channelCount,
path ("<album slug>/<track slug>__<seed>.flac"), playCount, played (last start), created, starred (iff
final), albumId, artistId "ar-metamusic", type "music", mediaType "song", userRating (human stars, omitted
when unrated), bpm (bpm_declared, omitted if null), comment (first 200 runes of the style), sortName,
genres [{name}], artists [{id,name}], displayArtist, albumArtists [{id,name}], displayAlbumArtist,
replayGain {trackGain, trackPeak} (from the loudness analysis, −18 LUFS reference as in the master's
tags)`, plus the OpenSubsonic fields the schema marks required, empty (`musicBrainzId ""`, `isrc []`,
`moods []`, `contributors []`, `displayComposer ""`, `explicitStatus ""`). `TestSubsonic_JSONMatchesSchema`
(C117.4) decides what "required" means.

**Lossy and reference tracks (rev 3, D-M4a-25).** For `lossy = 1`: `size` = master bytes, `contentType audio/mpeg`, `suffix mp3`,
`bitRate` = round(bytes·8 / duration / 1000), `bitDepth` omitted, `samplingRate`/`channelCount` from the row, no
`transcodedContentType`/`transcodedSuffix` whatever the client's default format, `path` ends `.mp3`. Reference tracks (stub
recipe, seed 0) carry `bpm`/`genre` only when present. Albums are lineages for every track (D-M2c-8 groups `_p<N>` files).

**AlbumID3**: `id "al-<root>", name, artist "metamusic", artistId, coverArt "al-<root>", songCount,
duration (sum), playCount (sum), created (root created_at), starred (latest status_set_at among final
members, omitted if none), year, genre, genres, artists, displayArtist, sortName, musicBrainzId "",
isCompilation false, releaseTypes [], moods [], discTitles []` (schema-driven like Child).

**Endpoints** (scope `read` unless marked **W**; "default folders" = 1 + 2; counts and sizes are capped
at 500; `size`/`count` defaults per the protocol):

| Endpoint | Parameters used | Behaviour | Response element |
|---|---|---|---|
| `ping` | — | ok | — |
| `getLicense` | — | always valid | `license {valid:true}` |
| `getOpenSubsonicExtensions` | — | no auth (C108.3) | `openSubsonicExtensions[]` (C108.6) |
| `tokenInfo` | — | caller's username | `tokenInfo {username}` |
| `getUser` | `username` | must equal the caller's username (else 50) | `user {username, scrobblingEnabled, streamRole:true, downloadRole:true, coverArtRole:true, playlistRole, commentRole, adminRole:false, settingsRole:false, uploadRole:false, shareRole:false, podcastRole:false, jukeboxRole:false, videoConversionRole:false, folder:[1,2,3]}` (write-dependent roles = the client's write scope) |
| `getMusicFolders` | — | the three folders | `musicFolders {musicFolder:[{id:1,"Library"},{id:2,"Drafts"},{id:3,"Discarded"}]}` |
| `getIndexes` | `musicFolderId`, `ifModifiedSince` | albums as directories, indexed by the folded first letter of the album name (`#` for non-letters); `lastModified` = newest `created_at`/`status_set_at` in ms; `ifModifiedSince ≥ lastModified` ⇒ no index entries | `indexes {lastModified, ignoredArticles:"", index:[{name, artist:[{id:"al-…", name}]}]}` |
| `getMusicDirectory` | `id` (`al-…`) | the album's songs (folder rule applies to members) | `directory {id, name, child:[Child]}` |
| `getArtists` | `musicFolderId` | the one artist | `artists {ignoredArticles:"", index:[{name:"M", artist:[{id:"ar-metamusic", name:"metamusic", albumCount, coverArt}]}]}` |
| `getArtist` | `id` = `ar-metamusic` | all albums in the folders, by name | `artist {id, name, albumCount, album:[AlbumID3]}` |
| `getAlbum` | `id` | album + songs | `album {AlbumID3…, song:[Child]}` |
| `getSong` | `id` | — | `song` |
| `getGenres` | — | style terms with ≥ 1 song in the default folders, by songCount desc, term | `genres {genre:[{value, songCount, albumCount}]}` |
| `getAlbumList` / `getAlbumList2` | `type`, `size`, `offset`, `fromYear`, `toYear`, `genre`, `musicFolderId` | types `random` (seeded by `Rand`), `newest` (root created desc), `highest` (max human stars desc, unrated last), `frequent` (plays desc), `recent` (last play desc, unplayed excluded), `alphabeticalByName`, `alphabeticalByArtist` (= by name), `starred` (any final member), `byYear` (inclusive; reversed when from > to), `byGenre` (any member has the term); unknown type ⇒ 10 | `albumList {album:[Child isDir=true]}` / `albumList2 {album:[AlbumID3]}` |
| `getRandomSongs` | `size`, `genre`, `fromYear`, `toYear`, `musicFolderId` | random songs | `randomSongs {song:[]}` |
| `getStarred` / `getStarred2` | `musicFolderId` | final songs; albums with a final member; no artists | `starred {song, album}` / `starred2 {song, album}` |
| `getSongsByGenre` | `genre` (req.), `count`, `offset`, `musicFolderId` | songs with that style term | `songsByGenre {song:[]}` |
| `getSimilarSongs` / `getSimilarSongs2` | `id` (song or album), `count` (default 50) | lineage relatives in recorded order, the song itself excluded (D-M4a-11) | `similarSongs {song}` / `similarSongs2 {song}` |
| `getTopSongs`, `getNowPlaying` | — | stubs | `topSongs {}`, `nowPlaying {}` |
| `getScanStatus` | — | never scanning | `scanStatus {scanning:false, count:<songs in default folders>}` |
| `getArtistInfo`, `getArtistInfo2`, `getAlbumInfo`, `getAlbumInfo2` | `id` | stubs (`id` must exist, else 70) | `artistInfo {}`, `artistInfo2 {}`, `albumInfo {}` |
| `search2` / `search3` | `query`, `artistCount`, `artistOffset`, `albumCount`, `albumOffset`, `songCount`, `songOffset`, `musicFolderId` | `query` empty, `""` (two quote characters) or absent ⇒ **everything**, paged (songs by created_at, id; albums by root created_at, root id; the artist once); otherwise M1's FTS token rules over tracks for songs, albums = roots of matching songs or whose root title matches (by name), artist iff `metamusic` prefix-matches; counts default 20, max 500 | `searchResult2 {artist, album:[Child dir], song}` / `searchResult3 {artist:[ArtistID3], album:[AlbumID3], song:[Child]}` |
| `getPlaylists` | `username` | non-hidden playlists incl. the offline set; `username` other than the caller's ⇒ 50 | `playlists {playlist:[{id, name, comment, owner (caller), public:false, songCount, duration, created, changed, coverArt}]}` |
| `getPlaylist` | `id` | — | `playlist {…, entry:[Child]}` |
| `createPlaylist` **W** | `name` or `playlistId`, `songId[]` | new playlist (`playlists.Create`) or full replacement of an existing one (`Update` with `TrackIDs`) | `playlist` |
| `updatePlaylist` **W** | `playlistId`, `name`, `comment`, `public` (ignored), `songIdToAdd[]`, `songIndexToRemove[]` | read revision → `Update` → on `REVISION_CONFLICT` retry (3×) | — |
| `deletePlaylist` **W** | `id` | `hidden = true`; the offline set (`Playlist.Role == "offline"`, B2) ⇒ 50 | — |
| `stream` | `id`, `format`, `maxBitRate`, `timeOffset` (ignored), `estimateContentLength` (ignored) | `format` ∈ `raw`, `flac`, `original` ⇒ the master FLAC; `mp3` ⇒ `mp3_320`; absent ⇒ the client's `default_format`, except `maxBitRate` 1–320 ⇒ MP3; any other format ⇒ MP3 (never transcoded on the fly); a lossy track (D-M4a-25) answers its master MP3 for every `format` and `maxBitRate` | binary (C108.8) |
| `download` | `id` (song) | tagged copy (C110.2; a lossy track ⇒ its master unchanged), `Content-Disposition: attachment` | binary |
| `getCoverArt` | `id` (`al-…`, `tr-…` or a song id), `size` | cover (C110.1), size via `ClampSize` | `image/png` |
| `getLyrics` | `artist`, `title` | best FTS title match among default-folder songs; none ⇒ empty element | `lyrics {artist, title, value}` |
| `getLyricsBySongId` | `id`, `enhanced` (ignored) | D-M4a-13; `lang` = ISO 639-1 of M2a's detected language, `und` when unknown | `lyricsList {structuredLyrics:[{displayArtist, displayTitle, lang, offset:0, synced:false, line:[{value}]}]}` |
| `star` **W** | `id[]`, `albumId[]`, `artistId[]` | songs ⇒ `judgments.UpdateTrack(status=final)` (already final ⇒ no-op); album/artist ids accepted and ignored; a `*library.ValidationError` from `UpdateTrack` ⇒ error **0** "status change not allowed" (fixed text; `library.ErrNotFound` ⇒ 70) | — |
| `unstar` **W** | same | final songs ⇒ `to_relisten`; others no-op; `UpdateTrack` errors as for `star` | — |
| `setRating` **W** | `id` (song), `rating` 0–5 | reads `judgments.Ratings(id)` first and calls `judgments.Rate(…, stars, verdict)` with the **existing human verdict** (M2a's upsert overwrites `verdict` with whatever is passed, so a nil verdict would erase the human's text); 0 ⇒ `stars` nil (clear stars, verdict kept; no human row yet ⇒ no write at all); non-song id ⇒ 70; out of range ⇒ 0; a 422 from `Rate` ⇒ 0 | — |
| `scrobble` **W** | `id[]`, `time[]` (ms), `submission` (default true) | per id: `submission=false` ⇒ `start` event (`from_ms 0`), true ⇒ `complete`; `client_at` = `time` or now; ids per D-M4a-12; **dropped** (ok, no event) when a `reportPlayback` session for the same (client, track) was seen in the last 30 minutes (D-M4a-12) | — |
| `reportPlayback` **W** | `mediaId`, `mediaType`, `positionMs`, `state`, `playbackRate`, `ignoreScrobble` | per (client, mediaId) in-memory state (lost on restart): `starting` ⇒ `start` at `positionMs`; `playing` ⇒ remember the resume position; `paused`/`stopped` after `playing` ⇒ `segment` [resume, positionMs]; `stopped` at ≥ 90 % of the duration ⇒ also `complete`; `ignoreScrobble=true` ⇒ no events; `mediaType` other than `song` ⇒ ok, no-op. Every accepted `reportPlayback` (any state, also `ignoreScrobble=true`) stamps "seen" for (client, mediaId), which suppresses `scrobble` for that pair for 30 minutes | — |
| `getBookmarks` | — | non-deleted, newest `updated_at` first | `bookmarks {bookmark:[{position, username, comment, created, changed, entry:Child}]}` |
| `createBookmark` **W** | `id`, `position` (ms), `comment` | upsert (C103); comment rule D-M4a-12 (`at_second = position/1000` clamped to the duration, rounded to 0.1 s) | — |
| `deleteBookmark` **W** | `id` | soft delete; the comment stays | — |
| `getPlayQueue` | — | the singleton (empty element when none) | `playQueue {current, position, username, changed, changedBy, entry:[Child]}` |
| `savePlayQueue` **W** | `id[]` (≤ 1000), `current`, `position` | upsert; unknown ids dropped | — |

Unknown song or album ids ⇒ error **70**. Every song-returning endpoint applies the folder rule unless
`musicFolderId` selects folder 3 explicitly; `getSong`, `getAlbum`, `stream`, `download` and
`getCoverArt` serve any existing id (a discarded take stays playable by id).

**Listen-event mapping (binding, M2a C36 `listening.Event`):**
- `SessionID` (NOT NULL, 8–64 chars): `scrobble` ⇒ `"ss-" + first 16 hex of sha256(client id + "\n" +
  UTC date of client_at as YYYY-MM-DD)` (one session per client per day); `reportPlayback` ⇒ `"rp-" +
  first 16 hex of sha256(client id + "\n" + mediaId + "\n" + client time of the `starting` report)` (one
  session per playback; a `playing` with no remembered `starting` uses the current report's time).
- `Format`: the format the bridge last streamed for (client, track) in this process (`raw`/`flac`/
  `original` ⇒ `"flac"`, MP3 ⇒ `"mp3"`), else the client's `default_format` (`raw` ⇒ `"flac"`, `mp3` ⇒
  `"mp3"`). `DeviceID`: nil (a Subsonic client is not an M1 device profile).
- Source `subsonic` (added to the Go-validated set by M4a-B1), principal kind `device`.
- The in-memory per-client maps (playback state, last streamed format, reportPlayback "seen") are bounded
  (LRU, 4096 entries per map) and lost on restart.
- Service errors: any `*library.ValidationError` (`judgments`) or 422 `*playlists.Error` ⇒
  protocol error **0** with a fixed message per endpoint; `ErrNotFound` ⇒ 70; anything else ⇒ error 0
  "internal error" (logged through `redact.Error`).

### C110. Cover art, tagged downloads, media serving amendments

**C110.1 Cover art (`internal/media/cover`).**
```go
const Version = "mm-cover/1"
func Render(rootID string, seed int64, size int) *image.Paletted // pure, deterministic
func Key(rootID string, seed int64, size int) string              // hex sha256(Version + "\n" + rootID + "\n" + seed + "\n" + size)
func ClampSize(n int) int                                         // n ≤ 0 ⇒ 512; else clamp to [32, 1024], round up to a multiple of 32
type Service struct{ /* data *os.Root; lib *library.Service; now */ }
func New(data *os.Root, lib *library.Service, now func() time.Time) *Service
func (s *Service) EnsureTrack(ctx context.Context, trackID string, size int) (serve.Object, string, error) // (root, seed) of the track
func (s *Service) EnsureAlbum(ctx context.Context, rootID string, size int) (serve.Object, string, error)  // = the root track's cover
func (s *Service) Resolve(ctx context.Context, key, ext string) (serve.Object, error)  // ext "png"; existing files only (serve.Resolver)
func (s *Service) Sweep(ctx context.Context, olderThan time.Duration) (int, error)
```
**Algorithm (binding, so output stays identical across implementations and Go releases):**
- `h = sha256("mm-cover/1|" + rootID)`; a splitmix64 stream `r` seeded with `uint64(seed) ^
  binary.BigEndian.Uint64(h[8:16])` (our own 10-line PRNG; `math/rand` is not used).
- Palette from the root only: hue `H = (uint16(h[0])<<8 | uint16(h[1])) mod 360`; background HSL(H, 0.35,
  0.12); primary HSL(H+150, 0.65, 0.55); secondary HSL(H+210, 0.55, 0.70); ink HSL(H, 0.20, 0.92)
  (integer HSL→sRGB with round-half-up). 16 palette entries: the 4 base colours + for each of primary,
  secondary and ink 4 blends with the background at 20/40/60/80 % (= 4 + 12).
- Shapes from the seed (in this order, coordinates as fractions of `size`): 3 + `r%4` concentric rings
  centred at (0.30 + 0.40·u, 0.25 + 0.30·u) (`u` = next `r` as a fraction), outer radius 0.30–0.45,
  ring widths 0.02–0.06, colours alternating primary/secondary; then 12 + `r%13` equaliser bars across
  the bottom 35 % in ink, heights 0.05–0.30. Edge coverage is sampled 4× per pixel and mapped to the
  nearest blend entry (no dithering).
- Encoding: `png.Encoder{CompressionLevel: png.BestCompression}` of the `*image.Paletted`.
- Files: `derived/covers/<aa>/<key>.png`, written in `incoming/cover-<uuid>/`, chmod 0444, claimed with
  `Root.Link` (EEXIST ⇒ identical content already there ⇒ reuse).
Tests: `TestCover_DeterministicPixels` (two renders equal; sha256 of the raw pixel indices + palette
equals the golden in `testdata/cover.golden.json` for 3 (root, seed, size) cases; `-update`),
`TestCover_SiblingsSharePalette` (same root, seeds 1101 and 1102 ⇒ identical palettes, ≥ 10 % of pixels
differ), `TestCover_RootsDiffer` (different roots ⇒ different base hue), `TestCover_SizeBudget` (10 fixed
(root, seed) pairs — 4× supersampled 1024 px renders are slow under `-race`: 1024 px ≤ 96 KiB, 512 px
≤ 40 KiB), `TestClampSize` (0 ⇒ 512; 10 ⇒ 32; 300 ⇒ 320;
5000 ⇒ 1024), `TestCover_EnsureIdempotent` (two concurrent `EnsureTrack` ⇒ one file, same key),
`TestCover_ResolveMissingIs404`, `TestCover_ReferenceTrack` (a stub-recipe track, seed 0, renders deterministically).

**C110.2 Downloads with current tags (`internal/media/tagged`).**
```go
const Version = "mm-dl/1"
type Tags struct {
    Title, Artist, Album, Date, Genre string // Artist "metamusic"; Album = root's current title; Date YYYY-MM-DD (recorded_at)
    TrackNumber, TrackTotal           int    // position in the lineage / lineage size
    ReplayGainTrackGain, ReplayGainTrackPeak string // copied from the master's tags (written at ingest)
    MM                                [][2]string // MM_TAKE_ID, MM_RECIPE_ID, MM_REQUEST_ID, MM_SEED (MM_REFERENCE=1 for a reference track), MM_STATUS (current)
}
func Write(dst io.Writer, master io.ReaderAt, size int64, info flacmeta.Info, tags Tags, cover []byte) (int64, error) // pure Go; info from flacmeta.Read
type Download struct{ Object serve.Object; Key, Filename string; Bytes int64; Tags Tags }
func New(data *os.Root, lib *library.Service, covers *cover.Service, now func() time.Time) *Service
func (s *Service) Ensure(ctx context.Context, trackID string) (Download, error)
func (s *Service) Resolve(ctx context.Context, key, ext string) (serve.Object, error) // ext "flac", files under derived/dl/ only; DownloadName set
func (s *Service) Sweep(ctx context.Context, olderThan time.Duration) (int, error)
```
- **Layout** (D-M4a-14): `fLaC`; STREAMINFO copied verbatim; SEEKTABLE copied verbatim when present; a
  new VORBIS_COMMENT (vendor `metamusic <version>`; comments in the order TITLE, ARTIST, ALBUM,
  TRACKNUMBER, TRACKTOTAL, DATE, GENRE (omitted when empty), REPLAYGAIN_TRACK_GAIN, REPLAYGAIN_TRACK_PEAK,
  MM_TAKE_ID, MM_RECIPE_ID, MM_REQUEST_ID, MM_SEED or MM_REFERENCE, MM_STATUS); a PICTURE block (type 3 front cover,
  `image/png`, the 512 px cover, width/height 512, depth 8, colours 16); the last-metadata-block flag on
  the final block; then the master's bytes from `info.FramesOffset` to EOF, copied unchanged. Values are
  sanitised like M1 C15 step 7 (C0/C1 controls ⇒ space) and capped at 1024 bytes.
- **Key** = hex sha256(`Version`, master sha256, canonical JSON of `Tags`, cover key); file
  `derived/dl/<aa>/<key>.flac`, built in `incoming/dl-<uuid>/`, 0444, claimed with `Root.Link`.
  `Filename` = `<slug(title)>__<seed>.flac` (`[a-z0-9._-]`, ≤ 120). A title or status change produces a
  new key; the old file ages out (7 days).
- **Lossy master (rev 3, D-M4a-25).** For a track with `library.Service.LosslessMaster` false, `Ensure` returns the master itself —
  `Download{Object: <master mp3>, Key: master sha256, Filename: <slug>__<seed>.mp3, Bytes: master bytes, Tags: zero}` — and writes
  nothing under `derived/dl/`; `Resolve` never answers it (the library resolver serves the master by its own sha).
Tests: `TestTagged_FramesIdentical` (sha256 of the output's frames region == `tracks.frames_sha256`;
STREAMINFO MD5 equal; exactly one VORBIS_COMMENT; PICTURE present; SEEKTABLE copied),
`TestTagged_CurrentTitle` (`update_track` title "Neu" ⇒ new key, TITLE=Neu, ALBUM = the root's title),
`TestTagged_InputAddressed` (second `Ensure` ⇒ same key, no new write), `TestTagged_SanitisedValues`,
`TestTagged_MasterUntouched` (mode 0444, sha and mtime of the master unchanged),
`TestTagged_DecodesWithFlac` (heavy, `testtools.Require(t, "flac")`: `flac --test` passes and the
decoded MD5 equals STREAMINFO's), `TestTagged_ConcurrentSameKey`, `TestTagged_LossyMasterPassthrough` (reference archive: no file written, the
master's bytes and mode unchanged), `TestTagged_ReferenceFLACTags` (`MM_REFERENCE=1`, no `MM_SEED`).

**C110.3 Media serving amendments** (M1 C18 / M2a C33 / M2b C45):
- Path regex ext gains `png`: `^/media/([0-9a-f]{64})\.(flac|mp3|jsonl|zip|jpg|png)$`; content type
  `image/png`.
- `serve.Chain(library, exports, clips, covers, downloads)` (`app.Build` appends `covers` and `downloads` to its existing
  `resolvers` list; `Chain` is variadic); `covers` answers
  `png`, `downloads` answers `flac` keys that exist under `derived/dl/` (the library answers master
  shas first, so the three `flac` resolvers — library masters, M2b clips and tagged downloads — never
  shadow each other: their keys are disjoint sha256 values of different inputs).
- **`serve.ServeObject`** (extracted from the M1 handler by M4a-B2, behaviour unchanged: `Handler(s, data, res, probe)` keeps its signature and its
  method, path, signature and `downloadName` checks and passes the resolved `Object` on):
  ```go
  func ServeObject(w http.ResponseWriter, r *http.Request, data *os.Root, obj Object, cacheControl string)
  // Content-Type, Accept-Ranges, ETag, cacheControl, nosniff, Referrer-Policy no-referrer, CORP same-origin,
  // Content-Disposition when obj.DownloadName != "", then http.ServeContent; a missing file ⇒ 500 INTERNAL (logged with the rel path only)
  ```
  The `/media/` handler calls it with M1's `private, max-age=31536000, immutable`; M1's Range matrix and
  M2a's download tests stay green unmodified.
- Sweeps (M2a C39 `serve` daily ticker, next to the export sweep): `Covers.Sweep(30 days)`,
  `Downloads.Sweep(7 days)`; `mcp-stdio` and the CLI never sweep.

### C111. The offline set (server side)

- Migration 0009 creates the playlist (C103). `playlists.Service` (M2a C33) learns `role`:
  `Playlist`/`PlaylistSummary` gain `Role *string` (JSON `role: "offline"|null`) and every playlist
  `SELECT` reads the column — **landed by M4a-B2** (the S3 `deletePlaylist` and A1 both need it); A1
  implements the rules that follow. `List` always includes the offline set
  (even if a direct SQL edit hid it); `Update` on the offline set refuses `hidden: true` (a `*playlists.Error`, 422
  `VALIDATION_FAILED`, `details.reason = "offline_set_cannot_be_hidden"`) and more than **100** items
  (422 `OFFLINE_SET_TOO_LARGE`, `details: {items, limit: 100}`; `PlaylistPatch.Revision` is required as for every update); `Create` never sets a role.
- New op **`get_offline_set`** (C113): the set with per-item media facts so a device can plan downloads
  without N extra calls:
  ```
  OfflineSet {playlist_id, name, revision, updated_at, limit_items: 100,
    items: [{position, track_id, title, album, duration_s, status,
                          flac: {sha256, bytes, sample_rate, bits_per_sample}|null,   // null for a lossy reference track
             mp3: {sha256, bytes}|null,                                  // a lossy track's master (D-M4a-25)
             lossy: bool}],
    totals: {items, flac_bytes, mp3_bytes}}
  ```
  Agents edit the set with M2a's `update_playlist` (`revision` from this response), so there is one
  write path for humans, agents and Subsonic (`updatePlaylist` on the same id).
- Tests: `TestOfflineSet_MigrationRow` (row present, role offline, fixed id, revision 1),
  `TestOfflineSet_Limits` (101 items ⇒ 422 with details; hide ⇒ 422), `TestOfflineSet_AgentEdits` (PAT
  agent: `get_offline_set` ⇒ `update_playlist add_track_ids` ⇒ `get_offline_set` lists it; the same over
  MCP `call_operation`), `TestOfflineSet_TotalsAndMP3Null` (a track without an MP3 rendition ⇒ `mp3: null`; a lossy reference track ⇒ `flac: null`,
  `lossy: true`, `mp3` = the master, and `totals.mp3_bytes` counts it).

### C112. MCP resources and prompts

**C112.1 Resources** (`internal/mcpgen/resources.go`, both HTTP and stdio servers):
```go
type ResourceSpec struct {
    URITemplate, Name, Title, Description, MIMEType, Op string
    Blob bool // the op's response carries image.data_base64 (mcp_image)
}
var Resources = []ResourceSpec{
    {URITemplate: "metamusic://tracks/{id}/context", Name: "track_context", Title: "Track context bundle",
     MIMEType: "application/json", Op: "get_track_context"},
    {URITemplate: "metamusic://tracks/{id}/spectrogram", Name: "track_spectrogram", Title: "Track spectrogram",
     MIMEType: "image/jpeg", Op: "get_track_spectrogram", Blob: true},
}
```
- Registration validates every spec at startup: the op exists, is GET, is not `exclude:*`, and its only
  path parameter is `id` (else `NewServer` errors — `TestResources_SpecsValid`).
- `resources/templates/list` ⇒ both templates (descriptions mention the tool equivalents).
- `resources/read`: the URI must match a template exactly (`id` = 36-char lowercase UUID; anything else
  ⇒ the SDK's resource-not-found error) ⇒ dispatch the op through the **gated** inner handler with the
  session's principal (M2a C30 dispatch, transport `mcp`/`stdio`). 2xx ⇒ context: one text content with
  the REST body bytes (`application/json`); spectrogram: one blob content with the decoded JPEG
  (`image/jpeg`). 404 ⇒ resource-not-found; any other status ⇒ a JSON-RPC error whose message is the
  problem `code` (never the body).
- `resources/list` ⇒ an **empty list**, no dispatch, no audit row, no rate-limit token (D-M4a-20;
  hosted clients call it on every connect). The templates are the discovery surface. (rev 3: go-sdk v1.8.0's `AddResourceTemplate`
  registers no concrete resource, so its own `resources/list` is already empty; A2's `TestResources_ListEmpty` pins that.)
- Tests: `TestResources_ReadEqualsTool` (context text == `get_track_context` REST body for the same
  principal; spectrogram bytes == base64-decoded `get_track_spectrogram` REST `image.data_base64`), over
  HTTP (PAT and OAuth connector) and stdio; `TestResources_ListEmpty` (an agent's `resources/list` ⇒ `[]`;
  the audit table and the agent's rate-limit bucket are unchanged); `TestResources_UnknownIDNotFound`;
  `TestResources_ScopeRefusal` (a token without `read` ⇒ the error carries `FORBIDDEN_SCOPE`).

**C112.2 Prompts** (`internal/prompts`; rendered only by the `get_prompt` op):
```go
type Arg struct{ Name, Description string; Required bool }
type Spec struct{ Name, Title, Description, Version string; Args []Arg }
var Catalog = []Spec{
    {Name: "critique_track", Title: "Critique a take", Version: "1", Args: []Arg{{Name: "track_id", Required: true}}},
    {Name: "write_variant", Title: "Write a variant", Version: "1", Args: []Arg{{Name: "track_id", Required: true}, {Name: "goal"}}},
    {Name: "session_review", Title: "Review a listening session", Version: "1", Args: []Arg{{Name: "since"}}},
}
type Embedded struct{ URI, MIMEType, Text string }
type Content struct{ Type string /* "text" | "resource" */; Text string; Resource *Embedded }
type Message struct{ Role string /* "user" */; Content Content }
type Sources interface {
    ContextJSON(ctx context.Context, p auth.Principal, trackID string) ([]byte, error)      // trackctx bundle, REST bytes
    Session(ctx context.Context, since time.Time, limit int) (listening.Session, error)       // C112.3
        Title(ctx context.Context, trackID string) (string, error)
    RecipeKnown(ctx context.Context, trackID string) (bool, error)                           // library.Service.RecipeKnown (D-M4a-25)
}
func Render(ctx context.Context, p auth.Principal, name string, args map[string]string, src Sources, now time.Time) (Spec, []Message, error)
```
Texts live in `internal/prompts/text/*.tmpl` (`text/template`, no HTML), golden-tested
(`internal/prompts/testdata/<name>[_variant].golden.json`, `-update`). Binding content:
- **`critique_track`** — message 1 (text): you are reviewing the take "<title>" (`<track_id>`); you cannot
  hear audio — base every statement on the context bundle (loudness, bandwidth, sections, flags,
  comments, listening) and name what needs the human's ears (`perception.blind_spots`); structure the
  answer as (1) what the numbers show, (2) questions for the human, (3) concrete fixes; record each fix
  with `add_comment` `kind=fix`, setting `at_second` only when `perception.timed` is true (otherwise name
  the section by its lyrics label without a time); agent ratings are stored as critic opinions and never
  change the human's stars. Message 2 (resource): `metamusic://tracks/<id>/context` with the bundle text.
- **`write_variant`** — a track with `recipe_known = false` (a reference track) ⇒ 422 `RECIPE_UNKNOWN` before any text is built. Message 1: the goal (or "improve the weakest section" when absent); propose
  **one** bounded change (lyric edit, style/BPM edit or new seeds), check it with `lint_recipe`, save it
  with `create_draft` (free). Then exactly one of two endings, chosen from the caller's scopes: with
  `generate` — "to render it, call `estimate_variant` and show the estimate to the human; spending needs
  `create_variant` with the confirmation token and may need the human's approval"; without — "rendering
  needs the generate scope, which this connection does not have; leave the draft for the human". Message
  2: the context resource.
- **`session_review`** — message 1: a fenced JSON block with `listening.Session` (C112.3) and the
  instructions: say which takes the human returned to, finished or skipped; ask about unrated ones;
  suggest next steps (`rate_track` belongs to the human, `add_comment`, `create_duel` between close
  takes, `create_draft` for a variant); never claim to have heard anything. `since` defaults to now − 12 h;
  RFC 3339; older than 30 days ⇒ 422.
**C112.3** `listening.Service.Session(ctx, since time.Time, limit int) (Session, error)` (new file
`internal/listening/session.go`, M2a C36 amended): human/device `listen_events` with `client_at ≥ since`,
grouped per track, newest last play first, ≤ `limit` (20) tracks:
`Session{since, until, tracks: [{track_id, title, plays, completed, skipped, seconds_heard, last_played_at,
stars|null, open_comments}], totals: {tracks, plays, seconds_heard}}` (`seconds_heard` = Σ `segment`
`to_ms − from_ms` / 1000, rounded).
**C112.4 MCP prompts:** `prompts/list` = `Catalog` (name, title, description, arguments); `prompts/get`
dispatches `get_prompt` through the gated handler (args ⇒ query parameters) and maps each message to
`mcp.PromptMessage` (text ⇒ `TextContent`; resource ⇒ `EmbeddedResource{Resource:
&ResourceContents{URI, MIMEType, Text}}`); a non-2xx ⇒ JSON-RPC error with the problem code.
Tests: `TestPrompts_GetEqualsTool` (HTTP + stdio: `prompts/get` == `get_prompt` REST, message by message),
`TestPrompts_ListGolden`, `TestPrompts_WriteVariantDependsOnGenerateScope`,
`TestPrompts_CritiqueEmbedsContext` (the embedded text equals `get_track_context`'s REST body),
`TestPrompts_SessionReview` (fixture listen events ⇒ expected totals), `TestPrompts_UnknownTrack404`, `TestPrompts_WriteVariantRefusesReferenceTrack`.
`TestMCPLists_Budget`: compact JSON of `prompts/list` + `resources/templates/list` ≤ 4 000 bytes.

### C113. Operations (types in `internal/api/types_m4a.go`; JSON snake_case)

Legend (M2a C38 / M3 C81): **C** = `idempotency: create`; **H** = `rate: heavy`; **S[…]** =
`secret_fields`. All `cost: free`. (rev 3, D-M4a-24) The five management operations are **not** human-only: they live under
`/api/`, need scope `admin` and have the generic MCP face.

| OperationID | Method + path | Scope | MCP | UI | Flags | Output |
|---|---|---|---|---|---|---|
| `get_offline_set` | GET `/api/offline-set` | read | generic | `/offline` | | `OfflineSet` (C111) |
| `get_download_link` | GET `/api/tracks/{id}/download` | read | generic | `/tracks/:id` | H | `DownloadLink` |
| `get_prompt` | GET `/api/prompts/{name}` | read | generic | `/tracks/:id` | | `PromptView` |
| `list_connector_grants` | GET `/api/connectors` | admin | generic | `/human/connectors` | | `{oidc: OIDCStatus, items: ConnectorGrant[]}` |
| `set_connector_grant` | PUT `/api/connectors/{id}` | admin | generic | `/human/connectors` | | `ConnectorGrant` |
| `list_subsonic_clients` | GET `/api/subsonic-clients` | admin | generic | `/human/subsonic` | | `{server_url, api_version, items: SubsonicClient[]}` |
| `create_subsonic_client` | POST `/api/subsonic-clients` | admin | generic | `/human/subsonic` | C, S[`api_key`,`password`] | 201 `CreatedSubsonicClient` |
| `revoke_subsonic_client` | POST `/api/subsonic-clients/{id}/revoke` | admin | generic | `/human/subsonic` | | `SubsonicClient` |

8 new operations: 0 first-class tools, 8 generic (three of scope read, five of scope admin), 0 excluded. `tools/list` therefore
grows only through the amended output schema of `get_media_links` (a first-class tool); M3's `toolsCeilingBytes = 87300`
(`internal/mcpgen/m2a_contract_test.go`) and the relative budget stay; the golden is regenerated. `TestOps_RegisteredWithMetaM4a`
pins these rows; `TestOps_BodyLimitsM4a`: every M4a op with a body uses 64 KiB; `TestRegistry_OpCount`
(`internal/api/ops_m2c_test.go`) is re-pinned from 81 to 89.

**Rules for the admin operations (rev 3, D-M4a-24; B2 implements, the owning lanes fill the handlers):**
- `api.Meta` validation (`internal/api/registry.go`): "secret_fields require a human-only create op" becomes "secret_fields require
  an `idempotency: create` operation" (the replay still nulls the secrets through `idem.Redact`); `human_only` ⇔ `/human/api/`
  is untouched; the registry tests cover the relaxed rule and the unchanged ones.
- Masking: for a non-human caller `ConnectorGrant.username` and `subject_hint` are `null`, and every `PrincipalRef` goes through
  `api.RefFor` (D-M2a-19); humans see everything.
- Audit and push: the gate audits every call; each handler sends one `notify.Message` (priority 3; text passes M3 C82's
  sanitiser; never a key, secret, URL or username): titles "Connector grant changed", "Subsonic client created", "Subsonic
  client revoked", "Connector grants listed", "Subsonic clients listed"; body "<caller name> <verb> <label>". A human's own
  `list_*` sends no push (assumption A-1).
- Who can call: `admin` is not a grant scope (CHECK in C103) and devices hold read and write, so only humans and admin-scope PAT
  agents can. `set_spend_caps`, `create_token` and `revoke_token` stay human-only, so no agent can raise the global caps.
- Scripted-agent and parity tests: `internal/acceptance/every_ui_action_test.go` derives its required set from the registry (not
  human-only, MCP not excluded, UI route) and `TestParityEveryOperation` (`internal/mcpgen/parity_test.go`) needs a case for every
  non-excluded operation. The lane that implements an operation adds its `uiScript` entry (the management calls with an admin-scope
  token) and its parity case (management cases mask ids, timestamps, keys and one-time secrets) when it replaces the stub. Until
  then the stub answers 501 `NOT_IMPLEMENTED` with `details.task = "M4a-<task>"`, which both tests recognise and skip by name
  (`t.Log`); B3's `TestNoPendingM4aStubs` (no M4a operation answers 501) turns the skip into a failure.

**Inputs:**
- `get_download_link`: path `id`.
- `get_prompt`: path `name` (`enum: critique_track, write_variant, session_review`); query `track_id`
  (required for the first two, else 422 `VALIDATION_FAILED`), `goal` (≤ 500 chars), `since` (RFC 3339).
- `set_connector_grant`: `{scopes?: ("read"|"write"|"generate")[] (unique, must contain "read"),
  limits?: {daily_budget_micro_usd: int|null, auto_approve_takes: int|null, auto_approve_micro_usd:
  int|null} (all three present; null = the global default), revoked?: bool}`, ≥ 1 field; amounts
  0..1 000 000 000, takes 0..100. (A whole `limits` object avoids the absent-vs-null ambiguity.)
- `create_subsonic_client`: `IdemHeader` + `{name: 1..80, username?: ^[a-z0-9][a-z0-9._-]{2,31}$,
  write?: *bool (nil ⇒ true), default_format?: "raw"|"mp3" (default raw), api_key?: *bool (nil ⇒ true),
  password?: *bool (nil ⇒ true)}`; `api_key` and `password` both false ⇒ 422. (`*bool` without Huma
  defaults: D-M2b lesson, an explicit `false` must survive.)
- `revoke_subsonic_client`: path `id`; idempotent.

**Shapes:**
```
DownloadLink {track_id, url (signed /media/<key>.flac; a lossy track: its master's /media/<sha>.mp3), filename, bytes, sha256 (of the tagged file),
  expires_at, ttl_s, tags: {title, album, track_number, track_total, date, genre|null}}
PromptView {name, title, description, version, arguments: [{name, description, required}],
  messages: [{role: "user", content: {type: "text", text} | {type: "resource", resource: {uri, mime_type, text}}}]}
OIDCStatus {enabled, issuer|null, resource ("<PublicURL>/mcp"), alg, required_scope: "metamusic",
  scopes_advertised[], clients: [{client_id, label}], keys: {count, fetched_at|null, last_error|null}}
ConnectorGrant {id, client_id, label, username|null, subject_hint|null (first 8 chars of the subject; both null for a non-human caller), scopes[],
  generate_enabled, limits: {daily_budget_micro_usd|null, daily_budget_usd|null, auto_approve_takes|null,
  auto_approve_micro_usd|null, auto_approve_usd|null},
  effective: {daily_budget_micro_usd, daily_budget_usd, auto_approve_takes, auto_approve_micro_usd, auto_approve_usd},
  created_at, last_used_at|null, updated_at, updated_by: PrincipalRef, revoked_at|null, revoked_by: PrincipalRef|null}
SubsonicClient {id, name, username, scopes[], default_format, has_api_key, api_key_prefix|null, has_password,
  created_at, created_by: PrincipalRef, last_seen_at|null, last_client|null, revoked_at|null}
CreatedSubsonicClient {client: SubsonicClient, api_key: string|null, password: string|null,
  server_url, username}   (secrets null on idempotent replay)
```
**Amended operations** (types and registrations M4a-B2, behaviour by the owning lane; each records the
change in its plan):
- `get_media_links` (M1 C19) gains `cover: {url_512, url_1024, expires_at}` (signed `/media/<key>.png`,
  TTL per M1 `TTLFor`; covers rendered on demand — a file-cache fill, never a DB write).
- `list_playlists`, `get_playlist`, `create_playlist`, `update_playlist` (M2a C38): `role` field; the
  offline-set rules of C111.
- `get_budget` / `set_spend_caps` (M3 C81): `CapsView.connectors` (C106); `get_budget` already reads `TokenLimitsFor`, which gains the connector branch.
- `revoke_own_token` (M2a C38): `Via != "token"` ⇒ 403 `NOT_A_TOKEN`.
**New problem codes:** `INSUFFICIENT_SCOPE` (403, token scope, C104), `CONNECTOR_FORBIDDEN` (403),
`CONNECTOR_REVOKED` (403), `OFFLINE_SET_TOO_LARGE` (422), `USERNAME_TAKEN` (409); `UNAVAILABLE` gains
components `oidc`, `covers`, `downloads`, `subsonic`.
**GET never mutates** (`TestGETNeverWritesDB_M4a`, the M2b C53 pattern): `get_offline_set`,
`get_prompt`, `get_download_link`, `get_media_links` and `list_*` as the proxy human leave
`data_version` and every table checksum unchanged (cover and download files may appear).

### C114. Wiring, HTTP server and CLI (amends M0 C6/C7, M2a C39, M3 C68)

- **`internal/app.Build`** gains (built once, shared by `serve`, `dev`, `mcp-stdio`, `servertest`):
  `oidc.Verifier` + `connectors.Store` + `connectors.Verifier` (only when `MM_OIDC_ISSUER` is set; else
  `Server.Connectors` is a **nil interface**, never a typed nil — `TestHTTPServer_NilConnectorsNoPanic`),
  `auth.SubsonicStore`, `cover.Service`, `tagged.Service`, the resolver chain of C110.3, the Subsonic
  handler (`subsonic.NewHandler`), and `Deps` fields `Connectors *connectors.Store`, `OIDC
  *oidc.Verifier`, `Subsonic *auth.SubsonicStore`, `Covers *cover.Service`, `Downloads *tagged.Service`,
  `Prompts prompts.Sources` (registration never dereferences them; nil ⇒ 503 `UNAVAILABLE` with the
  component). The OIDC HTTP client: 5 s timeout, no redirects, `http.DefaultTransport` clone; tests
  inject the stub's client through `app.Options.M4a.OIDCClient` and a no-op Subsonic failure sleep through
  `app.Options.M4a.SubsonicSleep` (a nested `M4aOptions` in `internal/app/m4a.go`, the `M3Options` precedent, where the new
  constructors live too; test-only, nil in production).
- **`httpserver.Options`** gains `Connectors auth.ConnectorVerifier`, `PRM http.Handler`, `Subsonic
  http.Handler` (nil ⇒ `/rest/` answers 404 problem `NOT_FOUND`). Mounts:
  `/.well-known/oauth-protected-resource/mcp` ⇒ `PRM` (the handler does its own method dispatch, C106);
  every other `/.well-known/` path, **including the root `/.well-known/oauth-protected-resource`** ⇒ 404
  problem (rev 3: today these paths fall through to `spaHandler`, i.e. the SPA shell with 200, so B2 adds the explicit mount); `/rest/` ⇒ `Subsonic`; `/sw.js` ⇒ the SPA file with
  `Content-Type: text/javascript; charset=utf-8`, `Cache-Control: no-cache` (today `spaHandler` serves `sw.js` like any file, without them); the auth middleware is built
  with `auth.Verifiers{…, Connectors: o.Connectors}`.
- **`serve`**: `go OIDC.Prefetch(ctx)` right after `app.Build` when the verifier exists (C105 eager
  fetch); hourly `Subsonic.PruneSalts` (next to M2a's idempotency prune); daily cover/download sweeps
  (C110.3).
- **CLI** (stdlib `flag`, `run(...)`-testable, `config.LoadOffline`; B2 owns `cmd/metamusic/main.go`: the `oidc` case in `run` and
  its usage lines, answered "not implemented" until M4a-C3, which implements it):
  - `oidc probe --server URL --client-id ID [--client-secret-file FILE] [--redirect URL]` (D-M4a-21):
    builds a go-sdk `auth.AuthorizationCodeHandler` (`PreregisteredClient`, `RequestRefreshToken: true`)
    whose `AuthorizationCodeFetcher` prints the authorization URL and reads the redirected URL from stdin;
    triggers it with a synthetic 401 carrying the server's real `resource_metadata` URL (so it fetches
    the live protected-resource metadata and provider metadata exactly like a hosted client); verifies the
    resulting access token **locally** with `oidc.Verifier` configured from the server's metadata and
    `--client-id` (signature, `iss`, `aud`, `exp`, scope, `mm_user`); then makes **one server round trip**:
    `POST <server>/mcp` (a JSON-RPC `initialize` body) with `Authorization: Bearer <token>` and expects
    **403 `CONNECTOR_FORBIDDEN`** — the probe client is not allow-listed (P.1), so this answer proves the
    server fetched the provider's keys and accepted signature, `iss`, `aud` and `exp` before refusing the
    client, and that no grant row was created (the allow-list check precedes `Store.Ensure`). The
    server-side scope check runs after the allow-list (C105 step 7) and is covered by the local check,
    which uses the same `oidc.Verifier` code. Other answers are reported as `server verify:` 401
    `invalid_token` (issuer/audience/keys mismatch between the server and the provider), 503 (the server
    cannot reach the keys — see `/human/connectors` `keys.last_error`) or "unexpected <status>". It prints
    the report (C105 fields; booleans for `mm_user` present, `offline_access` granted, refresh token
    received, PKCE S256 used, `iss` returned on the redirect; `server verify`) and exits 0 only when every
    check passes. Never prints a token, a code or a secret (`TestCLI_OIDCProbe` against the stub with
    canary secrets asserts stdout/stderr contain none of them). `--client-secret-file` is read by the
    binary (the agent never opens it).
  - `seed-synthetic --profile e2e` (M1 C20/C21 amended): additionally — idempotently, after both the fresh import and the "already seeded" early return, profile `e2e` only —
    puts the e2e profile's 12 s timed track (request id `syn-song-03`, seed 1003) first and its 6 s take (`syn-song-01`, seed 1001)
    second into the offline set and inserts one connector grant through `connectors.Store.Ensure` (client `e2e-connector`, subject `e2e-subject`, label `E2E Connector`,
    username `e2e`) so the `/human/connectors` page has a row without an OAuth flow.

### C115. Web (`web/`; amends M1 C22, M2a C40, M2b C54, M3 C84)

Routes (M4a-W1 registers all in `web/src/lib/router.ts` (`APP_ROUTES`, with `router.test.ts`) and in the `App.svelte` route table,
with placeholders for the pages W2/W3 fill): `/offline`,
`/human/connectors`, `/human/subsonic` (both `/human/*`: full-page navigations, M2a C40). Nav gains
"Offline" (badge = downloaded/total).

```
web/
  vite.config.ts                        (main build, amended) build.manifest: true ⇒ dist/.vite/manifest.json; keeps the existing
                                        emptyOutDir: true and the keep-dist-placeholder plugin (dist/.gitkeep must survive)
  vite.sw.config.ts                     second build, run after the main one: src/api/sw/sw.ts → dist/sw.js (format iife, no code
                                        splitting) with build.emptyOutDir: false (else it wipes the main build and dist/.gitkeep)
  scripts/precache.ts                   after both builds: writes the precache list (index.html + every emitted /assets/* file,
                                        from dist/.vite/manifest.json) and a version hash into dist/sw.js (replaces "__MM_PRECACHE__")
  src/api/sw/tsconfig.json              own TS project for the worker: lib ["ES2023", "WebWorker"] (no DOM), referenced by
                                        `bun run check`; the main tsconfig excludes src/api/sw/**
  src/api/sw/sw.ts                      service worker entry: install (precache shell), activate (drop old shell caches only, then
                                        self.clients.claim() so the first page load is already controlled), fetch → routes.ts
  src/api/sw/routes.ts                  pure: route(url, method, mode) → "media" | "shell" | "navigate-spa" | "navigate-server" | "pass"
  src/api/sw/range.ts                   pure: parseRange(header, size) → {start, end} | "unsatisfiable" | null; clampToCap(range, 4 MiB)
  src/api/sw/chunks.ts                  pure: CHUNK = 2 MiB; chunksFor(start, end); assemble(parts, start, end) → Uint8Array
  src/api/sw/store.ts                   ChunkStore interface + CacheStorage implementation (cache "mm-media-v1"), meta in IndexedDB "mm-offline"
  src/api/offline.ts                    download manager: downloadTrack(link, meta, onProgress, signal) — Range requests of 2 MiB against
                                        the signed URL, verified total length and sha256 (WebCrypto) before marking complete; evict; list; persist()
  src/api/swRegister.ts                 only when isSecureContext && "serviceWorker" in navigator: register("/sw.js", {scope: "/"})
                                        on boot (a rejected registration — e.g. Playwright's serviceWorkers: "block" — is logged
                                        and ignored); update prompt when a new worker waits
  src/lib/offline.svelte.ts             $state store over api/offline.ts (names frozen for later milestones):
                                        available (isSecureContext && serviceWorker && caches && crypto.subtle), items, progress,
                                        quota (navigator.storage.estimate), persisted;
                                        status(trackId) → "none" | "partial" | "complete"; download(trackId) → Promise<void>;
                                        remove(trackId) → Promise<void>; sync() → Promise<SyncSummary>; localSource(trackId) →
                                        Promise<{src, contentType, coverSrc|null} | null>
                                        (available === false ⇒ every offline UI element is hidden: nav entry, OfflineButton,
                                        the /offline page shows "Offline listening needs HTTPS")
  src/lib/player.svelte.ts              (M1/M2a) + offline source selection (below) + Media Session artwork from covers
  src/lib/components/OfflineButton.svelte    per-track "Download for offline" / progress / "Remove" (update_playlist on the offline set + local)
  src/lib/components/CoverArt.svelte         <img> of the signed cover link (512), alt = title
  src/lib/components/DownloadFlacButton.svelte  get_download_link ⇒ <a href download> (navigation, not fetch)
  src/lib/components/PromptButtons.svelte    "Copy critique prompt", "Copy variant prompt" (get_prompt ⇒ clipboard; char count shown)
  src/routes/Offline.svelte             online: the set (get_offline_set) merged with local state, "Sync offline set", storage
                                        estimate, persist state, play. Offline (navigator.onLine false, or get_offline_set fails
                                        with a network error): renders the downloaded items from IndexedDB files/tracks only and
                                        issues no API call (C115.4)
  src/routes/HumanConnectors.svelte     OIDC status + grants table; scopes checkboxes, limits (money.ts inputs), revoke/restore ⇒ set_connector_grant;
                                        the MCP URL to paste into a hosted client; never shows client secrets
  src/routes/HumanSubsonic.svelte       clients table; create form ⇒ API key and password shown once with copy buttons and per-app setup notes
                                        (server URL, username, "stream format: Raw/Original"); revoke
```
**C115.1 Service worker** (D-M4a-18; `routes.test.ts` is a table over these rules):
- Non-GET ⇒ `pass` (no `respondWith`).
- `/api/`, `/human/`, `/auth/`, `/mcp`, `/rest/`, `/worker/`, `/.well-known/`, `/__e2e/`, `/healthz`,
  `/sw.js` ⇒ `pass` always.
- `/media/<64 hex>.(flac|mp3|png)` (any query) ⇒ `media`: if `store.meta(sha, ext).complete` ⇒ respond
  from chunks: `Range` absent ⇒ 200 with the full length streamed chunk by chunk; `bytes=a-b`, `a-`,
  `-n` ⇒ **206** with at most **4 MiB** (`Content-Range: bytes a-e/size`, `e = min(b, a + 4 MiB − 1,
  size − 1)`), unsatisfiable ⇒ 416 `bytes */size`; headers `Content-Type` from meta, `Accept-Ranges:
  bytes`, `Content-Length`, `ETag: "<sha>"`. Not complete ⇒ `pass`.
- `/assets/*`, `/manifest.webmanifest`, `/icons/*` ⇒ `shell`: cache-first from `mm-shell-<version>`,
  network fallback.
- Navigations (`mode == "navigate"`): `/start`, `/pair`, `/auth/*`, `/human/*` ⇒ `navigate-server`:
  network, on failure 302 to `/offline`; every other path ⇒ `navigate-spa`: network first (redirects pass
  through untouched, so SSO login works), on failure the cached `/index.html`.
- `activate` deletes old `mm-shell-*` caches only; `mm-media-v1` and IndexedDB `mm-offline` are never
  touched by worker updates.
**C115.2 Offline store contract** (also used by the Playwright spec): Cache Storage `mm-media-v1`, chunk
keys `/__offline/<sha>.<ext>/<n>` (n from 0; every chunk 2 MiB except the last); IndexedDB `mm-offline`
v1, store `files` keyed `"<sha>.<ext>"`: `{sha, ext, content_type, bytes, chunks, complete, track_id,
title, album, duration_s, cover_key|null, downloaded_at}`; store `tracks` keyed `track_id`: `{track_id,
file: "<sha>.<ext>", cover: "<key>.png"|null}`. These two stores hold everything the `/offline` list and
the offline player need (title, album, duration, cover, content type), so neither calls the API offline.
**C115.3 Download and sync** (`api/offline.ts`): the device format is M1 C22.1's effective format
(`lossless` ⇒ flac, `high` ⇒ mp3, `auto` ⇒ mp3 iff saveData or cellular; a track whose `flac` link is null — a lossy reference,
D-M4a-25 — always takes its MP3 master); links from `get_media_links`
(human 12 h TTL), re-minted on 403. Each file is fetched with sequential `Range: bytes=<i·2MiB>-<…>`
requests (abortable, 3 retries per chunk), length checked against `bytes` and sha256 verified with
`crypto.subtle.digest` before `complete = true` (a mismatch deletes the chunks). The 512 px cover is
cached too. **Sync** = `get_offline_set` ⇒ download missing items in the device format, then evict local
files whose track left the set (listed in a summary). Before downloading, `navigator.storage.estimate()`
must show free quota ≥ needed bytes + 10 %, else the UI explains and stops. The first successful
download calls `navigator.storage.persist()` and shows the result. Nothing downloads without a tap
(D-M4a-19).
**C115.4 Player integration** (amends M1 C22.1/C22.2/C22.3): before minting links, the player asks the
offline store; a complete local file **and** `navigator.serviceWorker.controller !== null` ⇒ `src =
/media/<sha>.<ext>` (no query; the worker serves it) and no `get_media_links` call (an uncontrolled page
would send the unsigned URL to the network and get 403, so without a controller the player takes M1's
signed-link path). **Any** media error while a local source is playing (the network answers an
unsigned URL with 403, which browsers may report as `MEDIA_ERR_SRC_NOT_SUPPORTED`) ⇒ mark the local
file suspect, mint a signed link once and resume at the same position; it never runs M1's FLAC
fallback, records a failed probe or calls `put_device_profile` (that would wrongly downgrade the device).
**Offline playback needs no API call:** queue entries for downloaded tracks are built from the
IndexedDB `files`/`tracks` stores (no `get_track`, no `get_media_links`, no `get_offline_set`); listen
events go to M2a's outbox (`src/api/outbox.ts`, IndexedDB) exactly as online and are flushed when the
network returns.
Media Session artwork = `[{src: cover 512, sizes: "512x512", type: "image/png"}, {src: cover 1024,
sizes: "1024x1024"}]` from `get_media_links.cover` (offline: the cached 512 cover), falling back to M1's
icon.
**C115.5 Lint and tests:** ESLint bans gain `caches` (rev 3: `banned` in `web/eslint.config.js` already holds `fetch`, `XMLHttpRequest`, `EventSource` and
`indexedDB`, and `src/api/**` is exempt directory-wide, so `offline.ts` and `sw/` need no override). Unit tests (`bun test`): `range.test.ts` (`bytes=0-1`, `bytes=100-`,
`bytes=-500`, `bytes=0-` on 9 MiB ⇒ 4 MiB, `bytes=abc` ⇒ null, start ≥ size ⇒ unsatisfiable),
`chunks.test.ts` (chunksFor(0, 4 MiB − 1) ⇒ [0,1]; (2 MiB − 1, 2 MiB) ⇒ [0,1]; assemble across a chunk
boundary equals the source slice), `routes.test.ts`, `store.test.ts` (in-memory ChunkStore),
`offline.test.ts` (fake client + fake fetch: chunked download, sha mismatch ⇒ not complete, abort,
quota refusal, sync evicts removed items, persist() called once), `player.offline.test.ts` (complete
local file with a controller ⇒ no `get_media_links` call; the same file with `controller === null` ⇒ the
signed-link path; `MEDIA_ERR_SRC_NOT_SUPPORTED` on the local source ⇒ exactly one re-mint at the same
position, no MP3 switch, no `put_device_profile`; offline ⇒ no `get_track`/`get_media_links` call and
listen events land in the outbox), `offline.secure.test.ts` (`isSecureContext` false ⇒ `available`
false, `swRegister` never calls `register`, the nav entry and `OfflineButton` render nothing),
`precache.test.ts` (manifest ⇒ list). The worker sources type-check under `src/api/sw/tsconfig.json`
(`WebWorker` lib) as part of `bun run check`.

### C116. Tooling, CI, routing, privacy gate, coverage, docs (amends M1 C23/C24, M2a C41, M3 C85)

- **Routing contract** (`deploy/labels.example.yml`, `TestRoutingContract`): `mm-public` rule gains
  `|| Path(/sw.js)` (rev 3: `labels.example.yml` already routes `PathPrefix(/rest/)` to `mm-media` and `PathPrefix(/.well-known/)`
  to `mm-agent`; only `/sw.js` is a label change, the other rows are new contract-test cases); tests: `/sw.js` ⇒ `mm-public` (no forward-auth call, identity stripped);
  `/rest/ping.view?u=x&t=y&s=z` ⇒ `mm-media` (no forward-auth, access log off, no compression);
  `/.well-known/oauth-protected-resource/mcp` ⇒ `mm-agent` (the only metadata path; the root form is
  not a contract row — the app answers it 404); `/rest/x` with an `mm_dev` cookie still ⇒ `mm-media`
  (priority 100 > 50).
- **Provider example** (`deploy/authelia-oidc.example.yml`, public-safe; the operator merges it into the
  real configuration in M4a-X2): `identity_providers.oidc.claims_policies.metamusic` (`custom_claims:
  {mm_user: {attribute: username}}`, `access_token: [mm_user]`), `scopes.metamusic.claims: [mm_user]`,
  `authorization_policies.metamusic_owner` (`default_policy: deny`, rule `subject: user:owner` ⇒
  `two_factor`), `lifespans.custom.metamusic` (access token 1 h, refresh token 30 days), and clients
  `metamusic-claude` (`public: true`, `token_endpoint_auth_method: none`, `require_pkce: true`,
  `pkce_challenge_method: S256`, redirect `https://claude.ai/api/mcp/auth_callback`),
  `metamusic-chatgpt` (confidential `client_secret_post` with a digest placeholder, redirect
  `https://chatgpt.com/connector_platform_oauth_redirect`), `metamusic-probe` (`public: true`, loopback
  redirect `http://127.0.0.1:8765/callback`), each with `audience: ['https://mm.example.com/mcp']`,
  `requested_audience_mode: implicit`, `scopes: [openid, offline_access, metamusic]` (the client may
  request `offline_access`; the app no longer advertises it, D-M4a-4), `grant_types:
  [authorization_code, refresh_token]`, `response_types: [code]`, `access_token_signed_response_alg:
  RS256`, `authorization_policy: metamusic_owner`, **`consent_mode: explicit`** (no
  `pre_configured_consent_duration`), `claims_policy: metamusic`, `lifespan: metamusic`. Consent: Authelia
  forces explicit consent whenever `offline_access` is requested (`FormRequiresExplicitConsent`,
  `internal/oidc/util.go` on master — re-check on the 4.39 tag through context7 at implementation), so a
  pre-configured consent would have no effect; docs and the device checklist expect the provider's
  consent page on **every** authorization (first connect and every re-login, not on refreshes). Key names
  are verified against the provider's 4.39 documentation (context7) at implementation;
  `TestAutheliaExample_Shape` reads the example line by line (the repo has no YAML dependency; the routing contract reads
  `labels.example.yml` the same way) and checks these values (so docs and app constants cannot drift).
- **Privacy gate:** gitleaks custom rule `mms_[a-z2-7]{52}` (next to M0's `mmp_`, `mmc_`, `mmd_`);
  `TestNoSubsonicKeyLiterals` greps `*_test.go` for `mms_` literals (tests build keys at runtime from
  canaries); `TestSubsonicFixtures_Sanitised` scans `internal/subsonic/testdata/clients/*.jsonl` (no
  `t=`/`s=`/`apiKey=`/`p=`/`u=` value other than placeholders, no 36-char UUIDs, no free text outside
  placeholders); `third_party/opensubsonic/openapi.json` is allowed text.
- **Coverage thresholds** (`coverage.thresholds`): `internal/oidc 95`, `internal/connectors 95`,
  `internal/subsonic 90`, `internal/media/cover 90`, `internal/media/tagged 95`, `internal/prompts 90`;
  `internal/auth` stays 95 (Subsonic credentials included).
- **CI:** no new workflow. `go-fast` and `go-media` (`scripts/ci-go-test.sh`) run everything (heavy acceptance under no
  `-short`); `./internal/media/tagged` (needs `flac`) and `./internal/subsonic` (imports the synthetic archive) join the **media**
  list of `scripts/ci-go-test.sh` and the matching `scripts/ci-selftest.sh` check; the `e2e` job's Chrome projects run the M4a specs
  (C117.6).
- **Import graph:** `internal/archtest` gains `TestImportGraph_M4a` (rules like M3's `m3Rules`; packages that do not exist yet are
  skipped): `internal/oidc`, `internal/connectors`, `internal/subsonic`, `internal/media/cover`, `internal/media/tagged` and
  `internal/prompts` never reach `internal/api`, `internal/gate` or `internal/httpserver`; `internal/oidc` never reaches
  `internal/connectors`, `internal/store` or `internal/spend`; `internal/connectors` never reaches `internal/spend`;
  `internal/subsonic` reaches the domain only through services (never `internal/spend`, `internal/jobs`). M4a-X1 records `make check` (< 30 s warm) and the e2e
  wall time (target ≤ 5 min) in `metrics/REPORT.md`. No new `scripts/loop` targets.
- **Docs:** `docs/architecture.md` M4a section (public-safe); new `docs/connectors.md` (provider client
  setup from the example, hosted-client setup — claude.ai custom connector with only a client id;
  ChatGPT predefined client — the MCP URL, `oidc probe`, the consent page shown on every authorization,
  reachability of the provider's discovery/token/JWKS endpoints from the public internet and from inside
  the app container, troubleshooting by problem code and `keys.last_error`); new
  `docs/subsonic.md` (creating a client on `/human/subsonic`, Amperfy "Raw" stream format, Symfonium API
  key, Tempus token login, folders and star/rating semantics); `README.md` configuration table gains the
  C102 variables; `THIRD_PARTY_NOTICES.md`; `docs/device-checklist.md` M4a section (C117.7).

### C117. Test harness, fixtures and client fixtures (amends M2a C42, M3 C86)

**C117.1 `servertest`** gains `Options.M4a bool` (the M4a rows of `librarytest.FixtureM4a`; requires Fixture, M2c and M3 like `M3`;
the rig's unlimited limiter config lists the three Subsonic classes, B2) and `Options.OIDC bool` (starts an `oidctest.Provider` on the rig clock with
clients `test-claude` (public), `test-chatgpt` (confidential `client_secret_post`) and `test-probe`
(public, loopback redirect, **not** in `MM_OIDC_CLIENTS`), user `e2e` ↦ a fixed subject; configures
`MM_OIDC_ISSUER`, `MM_OIDC_CLIENTS=test-claude=Claude Test,test-chatgpt=ChatGPT Test` and
`MM_TRUSTED_USERS`), `Rig.OIDC *oidctest.Provider`, and:
- **Loopback public URL (OIDC rigs only).** The default rig's `PublicURL` `http://mm.example.com` is
  refused by C102 `Load` once OIDC is on (neither https nor loopback), and go-sdk refuses
  `resource_metadata`/metadata URLs that are neither https nor loopback and requires the metadata's
  `resource` to equal the URL it dialled. An OIDC rig therefore creates its server with
  `httptest.NewUnstartedServer`, sets `MM_PUBLIC_URL=http://127.0.0.1:<port>` (the listener's address)
  **before** `app.Build`, then starts it; `Rig.URL == PublicURL`, and every client (`Rig.Connector`,
  `TestCLI_OIDCProbe`) dials exactly `<PublicURL>/mcp`. The stub provider is also `http://127.0.0.1:<port>`.
  Non-OIDC rigs keep `http://mm.example.com`.
- **Rig-clock token source.** On a 401/403 go-sdk's streamable client calls `OAuthHandler.Authorize`,
  which runs a full authorization-code flow, never a refresh; `x/oauth2` refreshes only by the real wall
  clock (`expiryDelta` 10 s). `Rig.Connector` therefore sets `AuthorizationCodeHandlerConfig.NewTokenSource`
  to a test token source that keeps the current token and, on `Token()`, refreshes when **rig now ≥ exp −
  10 s** (exp = the access token's `exp` claim, decoded without verification — test-only) by calling
  `cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: rt}).Token()` with the context go-sdk passes in.
```go
type ConnectorOpts struct{ Client string /* default "test-claude" */; Scopes []auth.Scope /* set via set_connector_grant after the first call */ }
func (r *Rig) Connector(tb testing.TB, o ConnectorOpts) (*mcp.ClientSession, string /* grant id */)
// go-sdk StreamableClientTransport (endpoint <PublicURL>/mcp) + auth.AuthorizationCodeHandler (PreregisteredClient,
// RequestRefreshToken, RedirectURL "https://claude.ai/api/mcp/auth_callback", Client: the rig's plain http.Client,
// Fetcher: Rig.OIDC.Fetcher, NewTokenSource: the rig-clock token source above)
func (r *Rig) Subsonic(tb testing.TB, o subsonictest.ClientOpts) *subsonictest.Client // created through create_subsonic_client as the human
```
**C117.2 Parity runner — fourth face** (amends M2a C42 / M3 C86.1): rig D is an OIDC rig (C117.1, loopback
`PublicURL`) with an OAuth connector session. **Alignment:** D runs the same setup steps as A (including
minting the case's PAT, which D never uses) so the shared `IDSeed` sequences and audit rows stay aligned;
the grant is then **pre-created outside the sequence** — `connectors.Store.Ensure` on a store whose
`newID` returns the fixed id `00000000-0000-7000-8000-0000000000d4`, scopes set with `Store.Set`
directly (no op, no audit row) — so the first token finds an existing row and consumes no id, writes no
audit row and sends no ntfy (`Ensure` calls `newID` only when the `SELECT` misses, C106). **Subset:** D
runs a fixed list, not every MCP case: `whoami`, `search_tracks`, `get_track_context`, `add_comment`,
one generic `call_operation` (`get_offline_set`), and `estimate_variant` on a grant without `generate`
(tool error `FORBIDDEN_SCOPE` with the `/human/connectors` hint). Its normalised responses must equal
REST-on-A with masks: principal ids, client labels, `whoami.via`, `whoami.name`,
`get_budget.caps.connectors`, and absolute URLs compared after replacing D's origin with A's (D's
`PublicURL` differs).
`TestParityEveryOperation` (`internal/mcpgen/parity_test.go`) covers the eight new generic ops: `get_offline_set`,
`get_download_link` (deterministic key and signed URL under the shared clock), `get_prompt` and the five management operations
(admin-scope PAT on the REST, MCP and stdio rigs; masks for ids, timestamps, keys and the one-time secrets).
**C117.3 `librarytest.FixtureM4a` / `ApplyM4a`** (layered on `FixtureM3` like the M2c and M3 steps; `Fixture`, `FixtureM2c` and
`FixtureM3` stay byte-identical; the migration's offline-set row means every `playlists` count in earlier fixture tests moves by one,
B1 updates `librarytest/fixture_test.go` and `playlists_test.go`) add: two offline-set items (fixture tracks 1 and 2), one connector grant
(`read,write`, limits null; through `connectors.Store.Ensure`), one Subsonic client created through `auth.SubsonicStore.Create` (API key
and password; the plaintexts it returns are exposed as `Fixtures.SubsonicAPIKey` and
`Fixtures.SubsonicPassword`, never written to a file), one bookmark with a comment, a play queue of 2
tracks, and
`status_set_at` on every track. `make fixture-db` includes them (the Makefile target also runs the new `TestWriteFixtureDB_M4a`; B2).
**C117.4 Subsonic test kit** (`internal/subsonic/subsonictest`, test-only, owner M4a-S4):
```go
type ClientOpts struct{ Name, Username string; Write bool; Format string }
type Client struct{ /* base URL, username, password, api key, http client, clock */ }
type CallOpts struct{ JSON, View, POST, APIKey bool; Salt string /* "" ⇒ fresh random */; Range string; UserAgent string }
func (c *Client) Call(tb testing.TB, endpoint string, params url.Values, o CallOpts) *Response
type Response struct{ HTTP int; Header http.Header; Body []byte; Status string; Code int; Doc map[string]any /* JSON or XML→map */ }
func Replay(tb testing.TB, c *Client, lib Placeholders, path string)      // runs one fixture file (C117.5)
func Placeholders(tb testing.TB, st *store.Store) Placeholders            // {{song:N}} … resolved deterministically
```
`TestSubsonic_JSONMatchesSchema`: every implemented endpoint is called on the fixture library with `f=json`
and the `subsonic-response` object is validated against `third_party/opensubsonic/openapi.json`
(OpenAPI 3.0 schemas converted in the test: `#/components/schemas` ⇒ `$defs`, `nullable` ⇒ type unions;
`jsonschema-go`, test-only). `TestSubsonic_XMLGolden` (one golden per endpoint, `-update`; the XML parses
with `encoding/xml` and its root namespace is `http://subsonic.org/restapi`).
**C117.5 Client fixtures** (`internal/subsonic/testdata/clients/*.jsonl`; one JSON object per line):
`{"step", "advance": "<Go duration, optional: advance the rig clock before this step>", "salt": "<label,
optional: reuse the salt of the step with that label>", "method", "path", "params": {k: [v…]}, "auth":
"token"|"apikey"|"none", "headers": {"Range",
"User-Agent"}, "expect": {"http", "status", "code", "content_type", "counts": {"<json-or-xml path>": n},
"bytes_equal_master_from": "{{mid}}"…}}`. Placeholders: `{{song:N}}`, `{{album:N}}` (deterministic
order: created_at, id), `{{mid}}` (half the master size), `{{bytes}}`, `{{user}}`, `{{text}}`,
`{{query}}`. Synthetic files written in M4a-S4 from the clients' documented behaviour (research facts;
no client code is copied):
- `amperfy.jsonl` — XML, token/salt, `.view` paths: `ping`, `getOpenSubsonicExtensions`,
  `getMusicFolders`, `getIndexes`, `getMusicDirectory`, `getArtists`, `getArtist`, `getAlbum`, `getSong`,
  `getGenres`, `getAlbumList2 type=newest size=50`, `getRandomSongs`, `getStarred2`,
  `getSimilarSongs2`, `search3`, `getPlaylists`, `createPlaylist`, `updatePlaylist`, `deletePlaylist`,
  `stream format=raw maxBitRate=0` with `Range: bytes=0-1` then `Range: bytes={{mid}}-` (**raw stream
  with seek**), the same salt on `getSong` 11 minutes after its first use (API window: error 40), then
  the same stream URL (same salt) with `Range: bytes={{mid}}-` 11 hours after its first use (a long pause:
  media window, 206), `download`,
  `getCoverArt size=…`, `getLyricsBySongId`, `star`, `unstar`, `setRating rating=4`, `scrobble
  submission=false` then `submission=true`.
- `symfonium.jsonl` — JSON, `apiKey` without `u`, `v=1.13.0`: `ping`, `getOpenSubsonicExtensions`,
  `getMusicFolders`, **`search3 query=""`** with `artistCount=500 albumCount=0 songCount=0`, then
  `albumCount=500 albumOffset=0`, then `songCount=500 songOffset=0` and `songOffset=500`, the same three
  with **`query="\"\""`**, `getPlaylists`, `getPlaylist`, `getStarred2`, `reportPlayback` `starting` /
  `playing` / `paused` / `playing` / `stopped`, `stream` (no format: client default) with Range,
  `getCoverArt`, `setRating`.
- `tempus.jsonl` — JSON, token/salt, bare paths, some `POST` form bodies: `getLicense`, `getAlbumList
  type=random`, `getStarred`, `getSongsByGenre`, `getTopSongs`, `getAlbumInfo2`, `getArtistInfo2`,
  `getPlayQueue`, `savePlayQueue` (POST), `createBookmark position=30000 comment={{text}}`,
  `getBookmarks`, `deleteBookmark`, `search2 query={{query}}`, `getNowPlaying`, `getScanStatus`,
  `scrobble`.
There are no recorded fixtures (D-M4a-22 withdrawn); if the device checklist finds a client quirk, the
fix adds a synthetic step reproducing it to that client's file.
**C117.6 Playwright** (M4a-W4; proxy mode; `MM_RATE_SCALE=100`). **Service workers are blocked globally**
(`use: {serviceWorkers: "block"}` in `playwright.config.ts`): `page.route`/`context.route` do not see
requests a service worker handles, so the M1–M3 specs and `m4a-human` keep their network mocks and
observations intact. Only `m4a-offline.spec.ts` and `m4a-track.spec.ts` opt in with
`test.use({serviceWorkers: "allow"})`.
(rev 3) **Project placement.** The offline and track specs mutate the global offline set, so, like M3's generation specs, they form
their own serial phase in `playwright.config.ts` (one worker, `dependencies` on phase 1 and the `listen-*` projects): `m4a-offline` on
`chrome` only, `m4a-track` on `chrome` and `chrome-pixel7`; `m4a-human` joins phase 1 on `chrome` (it restores the grant it edits).
Specs create their own names and never assert global counters.
- `m4a-offline.spec.ts` (project `chrome` only — service workers plus `context.setOffline`):
  **`› offline set plays offline`** (the seeded set has 2 tracks: open `/offline` ⇒ 2 items not
  downloaded; tap "Sync offline set" ⇒ both "Downloaded", a `persist()` spy called once; Cache Storage
  `mm-media-v1` holds `ceil(bytes / 2 MiB)` chunks per file; go offline — `context.setOffline(true)`
  **and** `context.route("**/*", r => r.request().serviceWorker() ? r.abort("internetdisconnected") :
  r.fallback())`, so the worker's own fetches fail too; reload ⇒ the shell comes from the worker and the
  offline notice shows; open `/offline` ⇒ both items listed from IndexedDB; play the 12 s timed track ⇒
  `currentTime` advances ≥ 0.5 s within 3 s; seek to 6 s ⇒ `currentTime ≥ 6`; every `/media/` response
  observed `fromServiceWorker()` with status 206 and `Content-Length ≤ 4194304`; from the offline reload
  through playback **the page issued no `get_offline_set`, `get_track` or `get_media_links` request**
  (`page.on("request")` log) and no `/api/` request succeeded; the listen events are in the M2a outbox
  and, after going back online, are flushed by `log_listen_events`); **`› worker serves cached ranges capped at 4 MiB`** (the spec writes a
  synthetic 9 MiB file into the C115.2 store via `page.evaluate`, then `fetch('/media/<sha>.flac', {Range:
  'bytes=0-'})` ⇒ 206, `Content-Length: 4194304`, `Content-Range: bytes 0-4194303/9437184`;
  `bytes=-100` ⇒ the last 100 bytes; `bytes=9437184-` ⇒ 416); **`› agent-edited set syncs`** (a PAT
  agent adds a third track with `update_playlist` over REST ⇒ "Sync" downloads it; removing it ⇒ the next
  sync evicts it); **`› api is never served by the worker`** (offline: `/api/ping` fails with a network
  error, the session banner does not reload the page).
- `m4a-track.spec.ts` (`chrome`, `chrome-pixel7`; `serviceWorkers: "allow"`): the page is controlled
  after the first load (`clients.claim()`); cover art image loads (`/media/<64hex>.png`, 200,
  `image/png`); Media Session metadata artwork has the 512 and 1024 cover URLs; "Download FLAC" ⇒ a
  download event with filename `*.flac` (served `Content-Disposition: attachment`); "Copy critique
  prompt" ⇒ clipboard text contains "you cannot hear audio" and the track id; "Download for offline" on a
  track ⇒ it appears in `/offline` as downloaded; "Remove" ⇒ gone.
- `m4a-human.spec.ts` (`chrome`): `/human/connectors` lists `E2E Connector` with `read, write`; enabling
  generate and setting a daily budget of $0.50 ⇒ the row shows them after reload; revoke ⇒ "revoked";
  restore. `/human/subsonic`: create "Test phone" ⇒ API key (`mms_…`) and password shown once; reload ⇒
  not shown; the row lists `has_api_key`; revoke ⇒ revoked.
- Served-operation coverage: the 8 new ops are served (the teardown's existing check).
**C117.7 Device checklist (M4a)** (`docs/device-checklist.md`, operator-run, results in private notes):
- DC-M4a-1 claude.ai **web**: add the custom connector (MCP URL + client id only), sign in through the
  SSO with two factors, accept the provider's consent page (expected on every authorization), run
  "search my tracks for X" ⇒ `search_tracks`; ask it to comment ⇒ `add_comment`; on the track page the
  comment's author shows `{kind: agent, name: <client label>}` (e.g. "Claude").
- DC-M4a-2 claude.ai **mobile** (iOS or Android app): the same connector works after sign-in (and
  consent) on the phone; a second session the **next day** works without a new login (refresh token).
  This decides D-M4a-4's open point: if it needs a new login, set
  `MM_OIDC_SCOPES="openid offline_access metamusic"`, redeploy and repeat; record which setting works.
- DC-M4a-3 ChatGPT (if the account has connectors): predefined client; the same two calls.
- DC-M4a-4 Amperfy (iPhone): create a client on `/human/subsonic` (token login), stream format **Raw**;
  a 24-bit/48 kHz master plays; seeking by scrubbing works; **pause for 15 minutes, resume and seek** ⇒
  playback continues (media salt window, D-M4a-9; error 40 here is a bug); `setRating` from the app ⇒
  the human stars on the track page; starring ⇒ status final; CarPlay shows cover art (if available).
- DC-M4a-5 Symfonium (or Tempus) on Android: API key login; full sync (search3) finishes; offline
  download of an album works in the app; bookmark with a note ⇒ a timestamped comment.
- DC-M4a-6 installed PWA, iPhone and Android: Sync the offline set on Wi-Fi, enable airplane mode,
  relaunch from the home screen ⇒ `/offline` opens and plays with seeking; `storage.persist()` result
  recorded.
- DC-M4a-7 Media Session: lock screen shows the generated cover art.
- DC-M4a-8 Revoking the connector on `/human/connectors` ⇒ the next tool call in claude.ai fails
  (claude.ai may show only a generic error): confirm the 403 `CONNECTOR_REVOKED` in the app's request
  log (C104 row 1b refusal line; the middleware writes no audit row); restoring ⇒ works again.

---

## Acceptance mapping (roadmap M4a → named tests)

| Roadmap item | Named test(s) |
|---|---|
| hosted connector completes OAuth and runs `search_tracks` + `add_comment` | `TestAcceptance_M4a_HostedConnectorOAuth` (subtests `claude_public_client`, `chatgpt_confidential_post`: go-sdk OAuth client against the stub provider through the full chain — 401 + `resource_metadata` ⇒ protected-resource metadata ⇒ provider metadata ⇒ authorize with `resource=<PublicURL>/mcp` and PKCE S256 ⇒ token ⇒ `search_tracks` (items equal the human's REST search) ⇒ `add_comment` (201; author `{kind:"agent", name:"Claude Test"}`; an identical second call replays, one comment) ⇒ `get_audit` rows with `principal.id = agent:<grant>`, transport `mcp`, client `oauth:test-claude/…`; grant `read,write`; ntfy "Connector connected") · real clients: DC-M4a-1..3 |
| pre-registered clients; JWT via JWKS; `iss`/`aud`/`exp`/scope/alg pinning | `TestVerify_Table` · `TestVerify_HeaderKeyMaterialIgnored` · `TestJWKS_*` · `TestVerifier_PrefetchStatus` · `TestStub_*` · `TestAcceptance_M4a_ConnectorTokenLifecycle` (rig clock advanced past `exp − 10 s` ⇒ the rig-clock token source refreshes before the next call ⇒ the call succeeds; `Provider.Requests()` shows exactly **one** `refresh_token` grant and **no** additional authorize; missing `metamusic` ⇒ 403 `insufficient_scope` ⇒ go-sdk re-authorizes with the step-up scope ⇒ continues; other audience ⇒ 401; unlisted client ⇒ 403 `CONNECTOR_FORBIDDEN` with ≤ 1 authorize attempt and no step-up; untrusted `mm_user` ⇒ 403; a revoked grant ⇒ ≤ 1 authorize attempt) · `TestAcceptance_M4a_ConnectorReauthOn401` (a 401 on a still-fresh token — `RotateKey` + `DropOldKeys` — causes at most **one** re-authorization, then the call succeeds) · `TestConnector_RefreshWithoutOfflineAccessInPRM` · `TestTrustMatrix_M4a` · `TestCLI_OIDCProbe` |
| protected-resource metadata | `TestPRM_Golden` (only the `/mcp` path; root ⇒ 404) · `TestPRM_ResourceMatchesMCPURL` · `TestHTTPServer_M4aMounts` · `TestRoutingContract` (mm-agent) |
| per-(client, subject) grants, budgets, auto-approve, generate off by default | `TestConnector_GrantAutoCreatedReadWrite` · `TestConnector_GenerateOffByDefault` (`estimate_variant` ⇒ tool error `FORBIDDEN_SCOPE` with the hint; after `set_connector_grant` adds generate ⇒ 200) · `TestSetConnectorGrant_*` (spend_cap_changes row, revoke/restore, admin scope required, agent masking, audit and push) · `TestTokenLimitsFor_Table` · `TestSpend_ConnectorDailyCap` · `TestSpend_ConnectorTopUpUsesGrantDaily` · `TestSpend_ConnectorAutoApproveZero` · `TestGetBudget_ConnectorOwnLimits` · `TestParity_OAuthConnector` (fourth face, fixed subset) · `m4a-human.spec.ts` |
| native Subsonic client plays raw 24/48 FLAC with seek | `TestAcceptance_M4a_SubsonicRawStreamSeek` (24-bit/48 kHz synthetic master: `stream format=raw` ⇒ `bytes=0-1` 206 `fLaC`[0:2] with `Content-Range: bytes 0-1/<size>`; `bytes=<mid>-` 206 equal to the master's bytes; HEAD; `getSong` says `bitDepth 24, samplingRate 48000, suffix flac`) · `TestSubsonic_StreamRangeMatrix` · DC-M4a-4 |
| … and syncs rating | `TestAcceptance_M4a_SubsonicRatingSync` (`setRating rating=4` ⇒ REST `get_track.rating.human.stars == 4`, `by.kind == "device"`; `setRating 0` clears; critic row untouched; `star` ⇒ status final and `getStarred2` lists it; `unstar` ⇒ `to_relisten`) · DC-M4a-4/5 |
| Symfonium syncs through `search3 ""` (design §9) | `TestAcceptance_M4a_SubsonicClients/symfonium` (acceptance archive: 1 artist, 7 albums, 9 songs for both `query=""` and `query="\"\""`; offset 500 ⇒ 0 songs) |
| XML + JSON, `.view` + bare, GET + POST, apiKey + token/salt, salt window, auth rate limit | `TestSubsonic_Routing` · `TestSubsonic_Envelopes` · `TestSubsonicAuth_Order` · `TestSubsonicAuth_Matrix` (43, 44, 40, 42, 10, 50) · `TestSubsonicAuth_SaltWindow` (API 10 min) · `TestSubsonicAuth_SaltWindowMedia` (stream/download/getCoverArt 12 h) · `TestSubsonicAuth_FailureRateLimit` · `TestSubsonic_FailDelayBounded` · `TestSubsonic_FailDelayReal` · `TestSubsonicPassword_AADBindsRow` · `TestSubsonic_JSONMatchesSchema` · `TestSubsonic_XMLGolden` |
| domain mapping (album = lineage, folders, bookmarks ⇒ comments, scrobble ⇒ listen events) | `TestSubsonic_AlbumIsLineage` · `TestSubsonic_Folders` · `TestSubsonic_BookmarkCommentOnce` · `TestSubsonic_ScrobbleListenEvents` · `TestSubsonic_ListenEventFields` · `TestSubsonic_ReportPlaybackSegments` · `TestSubsonic_ScrobbleSuppressedAfterReportPlayback` · `TestSubsonic_SetRatingKeepsVerdict` · `TestSubsonic_StatusErrorsAreError0` · `TestSubsonic_PlaylistsCRUD` · `TestSubsonic_Lyrics` |
| download with current tags | `TestTagged_*` · `TestSubsonic_DownloadCurrentTags` · `m4a-track.spec.ts › download FLAC` |
| offline set plays offline | `m4a-offline.spec.ts › offline set plays offline` · `› worker serves cached ranges capped at 4 MiB` · `range/chunks/routes/offline` unit tests · DC-M4a-6 |
| offline set editable by agents | `TestOfflineSet_AgentEdits` · `m4a-offline.spec.ts › agent-edited set syncs` |
| cover art from seed + lineage | `TestCover_*` · `TestSubsonic_CoverArt` · `m4a-track.spec.ts › cover art` · DC-M4a-7 |
| MCP resources + prompts with tool equivalents | `TestAcceptance_M4a_ResourcesAndPrompts` (PAT, OAuth connector, stdio: templates list, context and spectrogram reads equal their tools, 3 prompts, `prompts/get` equals `get_prompt`) · `TestResources_*` · `TestPrompts_*` |
| no secret leaks; GET never mutates | `TestNoSecretLeak_M4a` (`internal/acceptance`; extends the M3 form: Subsonic API keys and passwords only in the issuing 201, null on replay, never in logs, audit or idempotency rows; OAuth tokens and codes never logged) · `TestGETNeverWritesDB_M4a` · E2E leak scanner |
| coverage gates | `coverage` CI job (C116 thresholds) |

**Plan-level checks** (not roadmap or design §9 items; ordinary tests, listed so they are not mistaken
for acceptance criteria):
- Synthetic client fixtures for Amperfy/Symfonium/Tempus: `TestAcceptance_M4a_SubsonicClients` (replays
  every synthetic `testdata/clients/*.jsonl`) · `TestSubsonicFixtures_Sanitised`.
- OpenSubsonic attribution and GPL hygiene: `TestSubsonic_AttributionHeader` (types.go header,
  `third_party/opensubsonic/{LICENSE,NOTICE.md}` present, `THIRD_PARTY_NOTICES.md` lists it) ·
  `TestThirdParty_OpenSubsonicPinned` · review checklist item in M4a-X1.

---

## Lanes and tasks

Stage 0: **M4a-X0** — **done 2026-10-15** (D-M4a-24 changed, D-M4a-8 approved; see the task).
Stage 1 (sequential, branch `m4a` from `main` (the Release 1 gate is green), its own root worktree, `MM_LANE=m4a-found`):
**M4a-B1 → M4a-B2**. Stage 1 lands every shared contract: config, migration 0009, principals and the
middleware's row 1b (against a fake verifier), the Subsonic credential store, the listen-event source
`subsonic`, the playlist `Role` field and its read SQL, `connectors.Store.Ensure`/`Get`, all 8
registrations with final types (handlers call `STUB(M4a-…)` package functions that answer 501
`NOT_IMPLEMENTED` with `details.task = "M4a-<task>"`), the amended types, `serve.ServeObject`, the resolver chain, `app.Build` and
`httpserver` mounts, the `servertest`/`librarytest` additions, the vendored OpenSubsonic description,
the e2e seed additions, `openapi.json` and `schema.d.ts`. **`openapi.json` and `schema.d.ts` are frozen
after M4a-B2.**
Stage 2 (parallel worktrees under `../metamusic-wt/`, branched from M4a-B2): **Connectors**
(`m4a-conn`: C1 → C2 → C3), **Subsonic** (`m4a-sub`: S1 → S2 → S3 → S4), **Media** (`m4a-media`: M1 →
M2), **Agent** (`m4a-agent`: A1 → A2), **Web** (`m4a-web`: W1 → W2 → W3), **Infra** (`m4a-infra`: I1 →
I2).
Stage 3 (`m4a-int`): **M4a-B3** (wiring and cross-lane acceptance) → **M4a-W4** (Playwright) →
**M4a-X1** integration → final review → **M4a-X2** deploy and provider configuration
(**operator-assisted**) → **M4a-X3** real clients and device checklist (**operator-assisted**).

**File ownership.** In Stage 2 no lane edits `internal/api/api.go`, `internal/api/registry.go`, `internal/api/types_m4a.go`, the
amended `types_m1/m2a/m3.go`, `internal/app/**`, `internal/httpserver/server.go`,
`internal/auth/{middleware.go,principal.go,classify.go,subsonic.go}`, `internal/store/migrations/**`,
`internal/media/serve/**`, `cmd/metamusic/{main.go,cmd_serve.go}`, `openapi.json` or
`web/src/api/schema.d.ts`; a needed contract change goes back through this document and a Stage-1-owner
fix commit every lane rebases onto. Each lane owns its packages and replaces only the stubs in its own
`internal/api/ops_*.go` files. Named exceptions: Connectors C2 edits `internal/spend/caps.go` (the connector branch of `TokenLimitsFor`; no call site changes),
`internal/generation/budget.go` (`capsView`: `connectors`) and `internal/servertest/connector.go`;
Subsonic S1 edits `internal/servertest/subsonic.go`; Media M1 edits the importer's orphan scan (`internal/importer/report.go`); Agent A1
edits `internal/playlists/**` (on top of B2's `Role` field) and adds `internal/listening/session.go`
(the listen source `subsonic` is already B1's); Agent A2 edits `internal/mcpgen/**` (never
`dispatch.go`'s client-label code, which B1 owns). `web/src/App.svelte` (route table) belongs to W1
only. Parity cases are added by the lane that implements the op (M2a C42 rule), which also adds the op's scripted call to
`internal/acceptance/every_ui_action_test.go`.

### Task M4a-X0 (Stage 0 — sign-offs; **done 2026-10-15**)
Recorded by the operator on 2026-10-15 (the private notes hold the wording): (1) **D-M4a-24 is changed** — `create_subsonic_client`,
`revoke_subsonic_client`, `list_subsonic_clients`, `list_connector_grants` and `set_connector_grant` are **not** human-only: they
are registry operations an agent may call when its token has the `admin` scope (humans too), every call is audited and sends an
ntfy push, and the global caps (`set_spend_caps`) stay human-only so a connector budget can never exceed the human cap; (2)
**D-M4a-8 is approved** — a Subsonic `apiKey` is a dedicated `mms_` credential (device kind, read/write, never generate); PATs
are refused on `/rest/`. Assumption **A-1** (a human's own `list_*` call sends no push) is the one point to confirm at the next
sign-off. **Done:** both decisions recorded; the plan's decision table, operation table, parity text and tests are updated.

### Task M4a-B1: config, migration 0009, principals, middleware row 1b, Subsonic credentials, leaves
**Files:** `internal/config/{config.go,config_m4a.go,config_m4a_test.go}` (C102; `config.go` only calls `loadM4a`);
`internal/store/migrations/0009_remote.sql` (`0008_remote.sql` until L1 merges, "Dependencies"),
`internal/store/{schema_m4a_test.go,triggers_m4a_test.go,upgrade_m4a_test.go}`, `upgrade_m3_test.go` (`v != len(migrations)`) and M2a's
`TestNoDeleteSQL` allowlist (`nodelete_test.go`) (C103); the earlier tests the migration's playlist row moves
(`internal/playlists/playlists_test.go` `List` expectations, `internal/library/librarytest/fixture_test.go` row counts, schema lists); `internal/auth/{principal.go,middleware.go,classify.go,subsonic.go}` + tests (C104, C107);
`internal/reqinfo/*`, `internal/redact/*`, `internal/ratelimit/*` (C102 leaves); `internal/listening`
(the Go-validated listen source set gains `subsonic`, M2a C36); `internal/judgments`
(the `UpdateTrack` SQL: `status_set_at`; `comments.go`: `commentSrcs` gains `subsonic`), `internal/library/write.go` (`InsertTake` sets `status_set_at`);
`internal/mcpgen/dispatch.go` (OAuth client label, C104); the `revoke_own_token` handler and the `whoami` docs (`internal/api/ops_tokens.go`, `ops_system.go`, C104);
`internal/httpserver/server.go` (builds `auth.Verifiers{…}` with `Connectors: nil`); plan amendments:
M0 C1/C3, M1 C11/C17, M2a C25–C31/C36/C38.
Tests first:
- `TestConfig_OIDC` (defaults: OIDC off, scopes `openid metamusic`; `MM_OIDC_ALG` is not a variable;
  `MM_OIDC_ISSUER=http://auth.example.com` ⇒ error naming the variable; `http://127.0.0.1:9/` ok;
  `MM_OIDC_CLIENTS=a=Claude,b=ChatGPT` ⇒ two clients; duplicate id, empty label or a label containing
  `=` ⇒ error; `MM_OIDC_SCOPES=openid` ⇒ error (no `metamusic`); `MM_OIDC_SCOPES="openid offline_access
  metamusic"` ok; issuer set with `MM_TRUSTED_USERS` empty ⇒ error; issuer set with an `http://`
  non-loopback `MM_PUBLIC_URL` ⇒ error; `http://127.0.0.1:<port>` ok). No error message contains a value.
- `TestListening_SourceSubsonic` (an event with source `subsonic` is accepted; an unknown source is still
  refused), `TestJudgments_CommentSourceSubsonic` (`AddComment(…, "subsonic", …)` accepted; an unknown source refused).
- `TestMigration0009_Applies` (`schema_m4a_test.go`; `user_version == len(migrations)` and the migration named `…_remote.sql` at
  position `remoteMigration`; the five tables, both unique indexes and every trigger name, read from the DDL like
  `TestMigration0007_Applies`; the offline-set row with id `00000000-0000-7000-8000-0000000000f1`, role `offline`, revision 1),
  `TestMigration0009_UpgradeFromM3` (`upgrade_m4a_test.go`: `store.OpenRaw` + `store.MigrateTo(ctx, raw, remoteMigration-1)` with
  `librarytest.FixtureM3` on a schema-7 copy — schema 8 on L1's fixture after the renumber step — then `store.Open` migrates;
  every track's `status_set_at == created_at`; a PAT still verifies; earlier row sets unchanged like
  `TestMigration0007_UpgradeFromM2d`), `TestTriggers_M4a` (DELETE on `connector_grants`, `subsonic_clients`,
  `subsonic_bookmarks`, `subsonic_play_queue` ⇒ "no physical delete"; UPDATE of `connector_grants.subject`,
  `subsonic_clients.api_key_hmac`, `playlists.role` ⇒ "frozen column"; a second `role='offline'` row ⇒ unique violation;
  `scopes='read,admin'` ⇒ CHECK; `UPDATE connector_grants SET scopes='read'` ⇒ ok), `TestNoDeleteSQL` (allowlist
  `{idempotency, human_sessions, subsonic_salts}`), every C103 write form run twice.
- `TestTrustMatrix_M4a` (C104, with a fake `ConnectorVerifier` returning each outcome and counting calls:
  shape failures on `/mcp` ⇒ 401 `invalid_token` with `resource_metadata` and zero verifier calls),
  `TestClassify_M4a`, `TestMiddleware_JWTOnAPIIs401`, `TestHTTPServer_NilConnectorsNoPanic`.
- `TestSubsonicStore_*`: create returns `mms_` + 52 base32 chars and a 24-char password, stores neither
  in plaintext (`SELECT *` contains neither); `VerifyAPIKey` ok / revoked / unknown;
  `VerifyToken` with `md5(password+salt)` ok; wrong token, unknown user (timing path exercises the dummy
  decrypt), salt of 5 chars, 65 chars or with a space ⇒ `ErrSubsonicCredentials`;
  `TestSubsonicAuth_SaltWindow` (fake clock, `SaltWindowAPI`: same salt at T0 and T0 + 9 m 59 s ok;
  T0 + 10 m 1 s ⇒ `ErrSaltExpired`; a new store instance on the same DB still refuses it);
  `TestSubsonicAuth_SaltWindowMedia` (`SaltWindowFor("stream")`, `"download"`, `"getCoverArt"` ⇒ 12 h,
  `"getSong"`, `"setRating"` ⇒ 10 min; a salt first used on `stream` at T0 ⇒ `stream` at T0 + 11 h 59 m
  ok, T0 + 12 h 1 m ⇒ `ErrSaltExpired`; the same salt with the API window at T0 + 11 m ⇒
  `ErrSaltExpired`; prune at T0 + 13 h 1 m removes the row, at T0 + 12 h 59 m keeps it);
  `TestSubsonicPassword_AADBindsRow`; `TestSubsonicStore_UsernameTaken` (live duplicate ⇒
  error; after revoke the name is free); `TestSubsonicStore_TouchAtMostOncePerMinute`.
- `TestClientIP_Table` (untrusted peer + XFF ⇒ peer; trusted peer, XFF `198.51.100.7, 10.0.0.2` with
  `10.0.0.2` trusted ⇒ `198.51.100.7`; all trusted ⇒ peer; garbage ⇒ peer), `TestRedact_M4aPatterns`,
  `TestRateLimit_Peek` (never consumes; empty ⇒ `retryAfter > 0`; an unknown class ⇒ ok), `TestRateLimit_M4aClassesScaled`.
- `TestUpdateTrack_StatusSetAt` (status change sets it; a title-only change does not),
  `TestInsertTake_StatusSetAt`, `TestDispatch_ClientLabel_OAuth`
  (`oauth:test-claude/claude-ai`), `TestRevokeOwnToken_RefusesOAuthPrincipal`.
Done: `make check` green; every earlier test green unchanged; `internal/auth` ≥ 95 %.

### Task M4a-B2: contract freeze — types, registrations, stubs, ServeObject, wiring, harness, vendored schema
**Files:** `internal/api/{types_m4a.go,ops_offline.go,ops_download.go,ops_prompts.go,ops_connectors.go,
ops_subsonic_clients.go}` + tests; `internal/api/registry.go` (the `secret_fields` rule, C113) and its tests;
`internal/api/ops_m2c_test.go` (`TestRegistry_OpCount` 81 → 89); amended types in `internal/api/{types_m1.go,types_m2a.go,types_m3.go}`
(`MediaLinks.cover`, playlist `role`, `CapsView.connectors`); `internal/playlists` (the `Role *string`
field on `Playlist` and `PlaylistSummary` and the `role` column in every playlist `SELECT` — C111; the
offline-set **rules** stay A1's); package skeletons with their final exported
API and `// STUB(M4a-<task>)` bodies: `internal/oidc/`, `internal/oidc/oidctest/`, `internal/connectors/`
(implemented here, not stubbed: `PRMHandler` (~40 lines, C106), `Store.Ensure` and `Store.Get` (C103
upsert; needed by `seed-synthetic`, the librarytest fixture and later the verifier)),
`internal/media/cover/`, `internal/media/tagged/`,
`internal/prompts/`, `internal/subsonic/` (handler answering error 0 for every endpoint;
`types.go` with the attribution header); `internal/media/serve/{serveobject.go,handler.go,types.go}`
(C110.3); `internal/app/{app.go,m4a.go}` (C114); `internal/httpserver/server.go` (C114 mounts); `internal/servertest/`
(`rig.go`: `Options.M4a`, `Options.OIDC`, the unlimited limiter classes; `Rig.Connector` → `tb.Skip("M4a-C2")`, `Rig.Subsonic` →
`tb.Skip("M4a-S1")`); `internal/library/librarytest/{fixture_m4a.go,fixture_v2.go}` (`FixtureM4a`, `ApplyM4a`: C117.3 rows; the
Subsonic client via `auth.SubsonicStore`, the grant via `connectors.Store.Ensure`) and `internal/servertest/m2b_test.go`
(`TestWriteFixtureDB_M4a`) with the `fixture-db` target in `Makefile`;
`cmd/metamusic/{main.go,cmd_seed.go}` (the `oidc` case; C114 e2e additions) + test; `internal/archtest/import_graph_m4a_test.go`
(C116); `cmd/metamusic/nohelpers_test.go`
(`oidctest`, `subsonictest`); `third_party/opensubsonic/{openapi.json,LICENSE,NOTICE.md}`,
`THIRD_PARTY_NOTICES.md`; `openapi.json`, `web/src/api/schema.d.ts`,
`internal/mcpgen/testdata/tools_list.golden.json`; plan amendments M0 C4/C6, M1 C18/C19/C20/C21, M2a
C33/C38/C39/C42, M2b C45, M3 C81. **Precondition:** M4a-X0 recorded (2026-10-15).
Tests first:
- `TestOps_RegisteredWithMetaM4a` (the 8 rows of C113 incl. flags and `secret_fields`; the five management ops are
  `human_only = false`, scope `admin`, MCP `generic`), `TestValidateMeta_SecretFieldsM4a` (relaxed rule: a non-human create op may
  declare secret fields; a non-create op may not), `TestImportGraph_M4a` (C116),
  `TestOps_BodyLimitsM4a`, `TestOps_NilDepsUnavailableM4a` (each new op answers 503 naming its
  component when its dependency is nil), `TestOps_CreateOpsDeclareIdempotencyKey` (M2a test; now also
  `create_subsonic_client`).
- `TestServeObject_SameAsMediaHandler` (M1's Range matrix runs against both; byte-identical headers),
  `TestMediaPath_PNG`, `TestResolverChainOrder`.
- `TestHTTPServer_M4aMounts` (`/.well-known/oauth-protected-resource/mcp` ⇒ the PRM handler: GET 200
  with `Cache-Control: public, max-age=300`, `OPTIONS` ⇒ 204 with the CORS headers, POST ⇒ 405 problem
  `METHOD_NOT_ALLOWED` with `Allow`; the root `/.well-known/oauth-protected-resource` and
  `/.well-known/other` ⇒ 404 problem; `Subsonic` nil ⇒ `/rest/ping` 404 problem; `/sw.js` from a test
  `fs.FS` with the C114 headers and anonymous in proxy mode), `TestPRM_Golden` (the `/mcp` path in both
  modes; root ⇒ 404).
- `TestConnectorsStore_Ensure` (first call ⇒ created, `read,write`, `updated_by system:oauth`; second
  call ⇒ same id, `created=false`, **`newID` not called**; concurrent first calls ⇒ one row),
  `TestPlaylists_RoleRead` (`Get`/`List` return `Role` "offline" for the migration row, nil otherwise).
- `TestThirdParty_OpenSubsonicPinned` (sha256 of `openapi.json` equals the value in `NOTICE.md`),
  `TestSubsonic_AttributionHeader`.
- `TestCLI_SeedSynthetic_E2EM4a` (after `seed-synthetic --profile e2e`: the offline set holds 2 tracks
  (the 12 s timed one first), one grant `E2E Connector` with `read,write`).
- `TestToolsListGolden` regenerated (`-update`), budget constant re-baselined in review with a one-line
  justification; `TestToolsList_Ceiling` (M2a) green.
Done: `make check` green; `grep -rn 'STUB(M4a-' internal` lists only lane-owned stubs (C1, C2, S1–S3, M1,
M2, A1, A2); `make openapi` committed; `make fixture-db` includes the M4a rows.

> **B2 contract notes (as built).** (1) The schema component of `get_download_link` is `TrackDownloadLink`: `DownloadLink` is
> M2a's export link. (2) `connectors.Store` is built by `app.Build` whether or not `MM_OIDC_ISSUER` is set (it is a plain
> table store the management operations and `seed-synthetic` need); only `oidc.Verifier` and `connectors.Verifier` depend on the
> issuer, and `Server.Connectors` stays a nil interface without it. (3) `get_media_links.cover` is nullable (null while no cover
> service is wired; M4a-M1 fills it). (4) `CapConnector` carries the `_usd` strings next to the micro amounts, like `CapToken`.
> (5) The plan's function `subsonictest.Placeholders` collides with its type; the function is `NewPlaceholders`. (6) `Options`
> fields of `httpserver` are `Connectors` (verifier), `ConnectorStore`, `Subsonic` (handler) and `SubsonicStore`. (7) The
> parity runner and the scripted-agent test skip an M4a stub by name (501 `NOT_IMPLEMENTED`, `details.task` `M4a-…`).
> (8) `toolsCeilingBytes` 87300 → 87600 (measured 87501: the nullable cover object of `get_media_links`).

### Task M4a-C1 (Connectors): `internal/oidc` and the stub provider
**Files:** `internal/oidc/{config.go,verify.go,jwks.go,discovery.go}` + tests,
`internal/oidc/oidctest/{stub.go,authorize.go,token.go,keys.go,fetcher.go,stub_test.go}`, `go.mod`/`go.sum`
(`github.com/golang-jwt/jwt/v5` becomes direct; D-M4a-3, justified in the commit message).
Tests first — `TestVerify_Table` (stub-minted tokens, fixed clock; one row each):
1. valid RS256 ⇒ ok, `Claims{Subject: <fixed>, ClientID: "test-claude", User: "e2e", Scopes: [openid
   offline_access metamusic], Audience: [<resource>]}`;
2. `alg: none` ⇒ `ErrInvalid`, zero JWKS fetches; 3. `HS256` signed with the RSA public key's DER bytes
   as the secret ⇒ `ErrInvalid`; 4. header `alg` `ES256`, `PS256` or `RS512` (via `Mutate`) ⇒
   `ErrInvalid`, zero JWKS fetches;
5. `typ` missing ⇒ invalid; `JWT` ⇒ invalid; `AT+JWT` ⇒ ok; `application/at+jwt` ⇒ ok;
6. `crit: ["exp"]` ⇒ invalid; 7. one flipped signature bit ⇒ invalid; 7b. the signature segment
   re-encoded with non-zero trailing bits (same decoded bytes, non-canonical base64url) ⇒ invalid
   (`WithStrictDecoding`);
8. after `RotateKey` a token with the new `kid` ⇒ ok after exactly one JWKS fetch; a `kid` absent from
   the refreshed set ⇒ invalid; a second unknown `kid` within 60 s ⇒ invalid with no new fetch;
9. `iss` with a trailing slash ⇒ invalid; 10. `aud` missing ⇒ invalid; `aud` = resource + `/` ⇒ invalid;
   `aud: ["other", <resource>]` ⇒ ok;
11. `exp = now − 31 s` ⇒ invalid; `now − 29 s` ⇒ ok; `exp` missing ⇒ invalid;
12. `iat = now + 31 s` ⇒ invalid; `iat` missing ⇒ invalid; `nbf = now + 31 s` ⇒ invalid;
13. `sub` empty ⇒ invalid; `client_id` missing ⇒ invalid;
14. `client_id: "other"` ⇒ `ErrClientUnknown`; `client_id: "other"` **and** `scope: "openid"` ⇒
    `ErrClientUnknown` (the allow-list precedes the scope check — never `MissingScopeError`);
15. `scope: "openid offline_access"` (listed client) ⇒ `*MissingScopeError{Scope: "metamusic"}`;
    `scp: ["metamusic"]` without `scope` ⇒ ok; 16. no `mm_user` ⇒ ok with `User == ""`;
17. 9000-byte token, four segments, padded base64 ⇒ invalid (no fetch);
18. JWKS endpoint failing and no cached keys ⇒ `ErrUnavailable`.
Plus `TestVerify_HeaderKeyMaterialIgnored`, `TestJWKS_RefreshRateLimited` (100 concurrent unknown-`kid`
verifies ⇒ 1 fetch), `TestJWKS_StaleKeysUpTo24h`, `TestJWKS_Limits` (65 KiB body, 302, 500, http
non-loopback ⇒ refused, logged code only), `TestJWKS_WeakKeysSkipped` (RSA 1024, `e = 3`; an EC key
in the set is skipped without error), `TestVerifier_PrefetchStatus` (C105),
`TestDiscovery_IssuerMismatch`, and the stub's own `TestStub_*` (PKCE required and verified; redirect
exact; `resource` outside the audience ⇒ `invalid_target`; per-client auth method enforced; code
single-use; refresh only with `offline_access`; rotation and reuse detection revoke the family; `iss` on
the redirect; metadata golden; `mm_user` only with `metamusic`; `Requests()` masks secrets and records
the grant type of every token request).
Done: `internal/oidc` ≥ 95 %; the stub is in `TestNoTestHelpersInBinary`; no EC/ES256 code exists.

### Task M4a-C2 (Connectors): grants, verifier, ops, spend limits, acceptance
**Files:** `internal/connectors/{store.go,verifier.go}` + tests (`store.go` extends B2's `Ensure`/`Get`),
`internal/api/ops_connectors.go` (handlers), `internal/spend/caps.go` (the connector branch of `TokenLimitsFor`, which the create
transaction, estimates, `get_budget` and `ReserveTopUp`'s batch-owner daily already call) and `internal/generation/budget.go`
(`capsView`: `connectors`),
`internal/servertest/connector.go` (`Rig.Connector`, rig-clock token source), `internal/mcpgen/parity_test.go`
(fourth face, C117.2), `internal/connectors/acceptance_test.go`; plan amendments M3
C69 (`spend_cap_changes.target` comment)/C72.3/C72.6/C81/C82, M2a C42.
Tests first:
- `TestVerifyConnector_Order` (spies: oidc → trusted user → `Ensure` → revoked → touch → principal).
- `TestConnector_GrantAutoCreatedReadWrite` (first valid token ⇒ one row `read,write`, limits null; one
  audit row `auth:connector_grant`, transport `oauth`, status 201; one ntfy message "Connector
  connected" whose body contains the label and neither the username nor the subject; a second token ⇒
  no new row, no message).
- `TestConnector_UntrustedUser` (403 `CONNECTOR_FORBIDDEN`, no row), `TestConnector_Revoked` (403
  `CONNECTOR_REVOKED`; restore ⇒ ok), `TestConnector_TouchAtMostOncePerMinute`.
- `TestSetConnectorGrant_Table` (scopes without `read` ⇒ 422; `admin` ⇒ 422; `limits` ⇒ one
  `spend_cap_changes` row with before/after JSON and target `connector:<id>`; `revoked:true` ⇒
  `revoked_by` = the human's key; `revoked:false` restores; a read-scope agent, a connector principal and a paired device ⇒
  403 `FORBIDDEN_SCOPE`; an admin-scope PAT agent ⇒ 200 with one audit row and one ntfy push; the agent's view masks `username` and
  `subject_hint`; unknown id ⇒ 404), `TestSetConnectorGrant_AgentCannotExceedGlobalCaps` (an admin agent sets a grant daily budget of
  $1000: a create by that connector still fails `BUDGET_EXCEEDED` on the global batch ceiling or month, and `set_spend_caps` answers
  403 `HUMAN_ONLY` to the same agent), `TestConnectorOps_ScriptedAndParity` (the `uiScript` entries and parity cases of the two
  connector operations, replacing the stub's pending skip), `TestListConnectorGrants_OIDCStatus` (disabled ⇒ `enabled:false`,
  `issuer:null`; enabled ⇒ clients, resource, `keys.count ≥ 1` after `Prefetch` with no verify).
- `TestPRM_ResourceMatchesMCPURL`, `TestTokenLimitsFor_Table`,
  `TestSpend_ConnectorDailyCap`, `TestSpend_ConnectorTopUpUsesGrantDaily`,
  `TestSpend_ConnectorAutoApproveZero`, `TestGetBudget_ConnectorOwnLimits`,
  `TestCapsView_ConnectorsVisibility` (human: all grants; OAuth caller: its own; PAT caller: none).
- `TestConnector_GenerateOffByDefault`, `TestParity_OAuthConnector` (C117.2 subset and masks).
- `TestConnector_RefreshWithoutOfflineAccessInPRM` (default `MM_OIDC_SCOPES`: the protected-resource
  metadata lists no `offline_access`; the stub's authorization-server metadata does; `Provider.Requests()`
  shows the authorize request carrying `offline_access` (go-sdk added it) and the token response
  includes a refresh token; a later `refresh_token` grant succeeds).
- **`TestAcceptance_M4a_HostedConnectorOAuth`**, **`TestAcceptance_M4a_ConnectorTokenLifecycle`** and
  **`TestAcceptance_M4a_ConnectorReauthOn401`** exactly as in the acceptance mapping (fixture library,
  rig clock, loopback `PublicURL`). The lifecycle test advances the rig clock past `exp − 10 s` and
  asserts, through `Provider.Requests()`, that the next MCP call was preceded by exactly **one**
  `refresh_token` grant and **no** extra authorize request, and that a revoked grant produces at most one
  authorize attempt. The re-auth test makes a still-fresh token fail with 401 (`RotateKey` +
  `DropOldKeys`) and asserts at most **one** new authorize request before the call succeeds.
Done: `internal/connectors` ≥ 95 %; `TestParityEveryOperation` green with the fourth face.

> **C2 contract notes (as built).** (1) `connectors.Store` gains `Touch` (the C103 once-a-minute SQL), exported `TouchInterval` and the
> errors `ErrInvalidScopes`, `ErrInvalidLimits` and `ErrEmptyChange`; `Set` stores scopes in the CHECK's order (`read,write,generate`)
> whatever the request's order and keeps the first revoker when a revoked grant is revoked again. (2) `VerifierOptions` gains an
> optional `Logger` (nil is `slog.Default()`); refusals log one line with a code (`client_not_allowed`, `user_claim_missing`,
> `user_not_trusted`, `keys_unavailable`, `grant_revoked`), never a value. (3) The grant-created audit row has client `oauth:<client_id>`.
> (4) `api.Authorize` adds `details.hint` ("the operator can enable this scope for the connector at /human/connectors") to the
> `FORBIDDEN_SCOPE` problem of a `Via oauth` principal. (5) go-sdk closes a session whose call was refused with a 403 (revoked grant),
> so the lifecycle test reconnects after restoring the grant. (6) `TestAcceptance_M4a_ConnectorReauthOn401` makes the server learn the
> new key first (a token of the new key reaches `/mcp` once), because the verifier's cache keeps the old key until it refetches.
> (7) `golang.org/x/oauth2` is a direct requirement now (the rig-clock token source imports it).

### Task M4a-C3 (Connectors): `metamusic oidc probe`
**Files:** `cmd/metamusic/{cmd_oidc.go,cmd_oidc_test.go}`; M0 plan C7 note.
Tests first: `TestCLI_OIDCProbe` (OIDC rig with loopback `PublicURL` (C117.1) + stub, client
`test-probe` (public, loopback redirect, not allow-listed); `--server` is exactly the rig's
`PublicURL`; the test feeds the stub's redirected URL on stdin ⇒ exit 0; the report lists `iss ok, aud
ok, typ at+jwt, alg RS256, kid known, scope metamusic: yes, mm_user present: yes, offline_access: yes,
refresh token: yes, PKCE S256: yes, iss on redirect: yes, server verify: CONNECTOR_FORBIDDEN (ok)`; the
rig then has **zero** `connector_grants` rows; canary client secret, code and token strings absent from
stdout/stderr), `TestCLI_OIDCProbe_ServerKeysUnavailable` (the rig's verifier cannot reach the JWKS ⇒
the round trip answers 503 ⇒ exit 1 with `server verify: keys unavailable`),
`TestCLI_OIDCProbe_ServerWrongKeys` (the rig's `MM_OIDC_JWKS_URL` points at a second stub's JWKS ⇒
the local checks pass but the server answers 401 `invalid_token` ⇒ exit 1 with `server verify:
invalid_token`), `TestCLI_OIDCProbe_MissingUserClaim` (stub without
`mm_user` ⇒ exit 1, `mm_user present: no`), `TestCLI_OIDCProbe_StateMismatch` (exit 1),
`TestCLI_OIDCProbe_NoAuthorizationServers` (server with OIDC off ⇒ exit 2 with "the server advertises
no authorization server").
Done: `metamusic oidc probe --help` documents the paste flow; no output path prints a secret.

### Task M4a-S1 (Subsonic): protocol layer, auth flow, system endpoints, client ops
**Files:** `internal/subsonic/{handler.go,params.go,envelope.go,auth.go,system.go,audit.go,ids.go}` +
tests, `internal/subsonic/subsonictest/client.go`, `internal/api/ops_subsonic_clients.go` (handlers),
`internal/servertest/subsonic.go` (`Rig.Subsonic`).
Tests first:
- `TestSubsonic_Routing` (`/rest/ping` == `/rest/ping.view`; `/rest/Ping` and `/rest/ping/x` ⇒ error 0;
  PUT ⇒ 405 with `Allow: GET, HEAD, POST`; form POST ⇒ ok; JSON POST body ⇒ error 0; form > 64 KiB ⇒
  error 0; HEAD ⇒ headers only).
- `TestSubsonic_Envelopes` (XML and JSON goldens for `ping` ok and an error; namespace
  `http://subsonic.org/restapi`; `openSubsonic` true; `f=jsonp` ⇒ JSON error 0; content types).
- `TestSubsonic_CommonParams` (missing `c` or `v` ⇒ 10; `v=2.0.0` ⇒ 30; `v=1.13.0` ok).
- `TestSubsonicAuth_Order` (spies: per-IP `Peek` → verify → on failure per-IP consume, global `Allow`,
  `Sleep`; the global class is never touched on success), `TestSubsonicAuth_Matrix` (`apiKey`+`u` ⇒ 43;
  bad key ⇒ 44; bad token ⇒ 40; `u`+`p` ⇒ 42; nothing ⇒ 10; revoked client ⇒ 40/44; read-only client on
  `setRating` ⇒ 50), `TestSubsonicAuth_WindowByEndpoint` (the handler passes
  `auth.SaltWindowFor(endpoint)`: a salt reused after 11 minutes works on `stream`, `download` and
  `getCoverArt`, fails with 40 "salt expired" on `getSong`),
  `TestSubsonicAuth_FailureRateLimit` (fake clock, no-op `Sleep`: 5 failures from one IP pass the
  burst, the next request from that IP ⇒ HTTP 429 with `Retry-After`, even with valid credentials;
  another IP with valid credentials ⇒ ok; after `Retry-After` ⇒ ok; 20 failures spread over 5 IPs
  exhaust the global class ⇒ a sixth IP with **valid** credentials ⇒ **200**; the same sixth IP with
  **invalid** credentials ⇒ **429** at once (the `Sleep` fake records no call)),
  `TestSubsonic_FailDelayBounded` (blocking fake `Sleep`: 17 concurrent failures ⇒ the 17th gets 429
  without sleeping), `TestSubsonic_FailDelayReal` (the one test with the real timer: one failure with
  `FailDelay` 1 s takes ≥ 1 s; a cancelled context ends the wait early),
  `TestSubsonic_GetOpenSubsonicExtensionsAnonymous` (no credentials ⇒ the 4 extensions sorted).
- `TestSubsonic_SystemEndpoints` (`getLicense`, `tokenInfo` (API key and token auth ⇒ the client's
  username), `getUser` (own ⇒ roles by scope; other ⇒ 50), `getMusicFolders` (3 folders)).
- `TestSubsonicClients_Ops` (create ⇒ 201 with `api_key` and `password`; replay with the same key ⇒ both
  null; `list_subsonic_clients` never contains them; revoke idempotent; `USERNAME_TAKEN`; both auth
  methods false ⇒ 422; a read-scope agent ⇒ 403 `FORBIDDEN_SCOPE`; an admin-scope PAT agent ⇒ 201 with one ntfy push, the secrets in
  that one response only — never in the audit row, the idempotency row or the push; the agent's list view masks created_by), `TestSubsonic_LogsNeverContainCredentials` (captured slog for 50
  mixed calls contains no `t`, `s`, `apiKey`, `p`, `u` value).
Done: `Rig.Subsonic` usable by S2–S4 and B3.

### Task M4a-S2 (Subsonic): catalog, browsing, lists, search
**Files:** `internal/subsonic/{catalog.go,browse.go,lists.go,search.go,stubs.go}` + tests.
Tests first (fixture library; ids resolved through `subsonictest.Placeholders`):
- `TestSubsonic_AlbumIsLineage` (siblings share `al-<root>`; track numbers 1, 2 in recorded order;
  album name = the root's title; after `update_track` renames the root, `getAlbum` shows the new name).
- `TestSubsonic_Folders` (a fixture track set to `discarded` appears only with `musicFolderId=3`; a
  `final` one in folder 1; `draft` in folder 2; no folder ⇒ 1 + 2).
- `TestSubsonic_Search3Empty` (`query=""`, `query="\"\""` and no `query` ⇒ everything; `songCount=2`
  pages through all songs without duplicates or gaps; `songOffset` beyond the end ⇒ empty),
  `TestSubsonic_Search3Query` (`citta` finds a title containing `città`; FTS operators in the query never
  error), `TestSubsonic_Search2Shape`.
- `TestSubsonic_AlbumListTypes` (each type's order on the fixture, incl. `highest` with an unrated album
  last, `recent` excluding unplayed, `byYear` reversed range, unknown type ⇒ 10),
  `TestSubsonic_RandomDeterministicWithRand`, `TestSubsonic_Genres`, `TestSubsonic_SimilarSongs`
  (song id and album id forms), `TestSubsonic_IndexesIfModifiedSince`, `TestSubsonic_StubsShape`,
  `TestSubsonic_UnknownIdsAre70`.
Done: S2 endpoints match C109; no SQL outside `catalog.go`.

### Task M4a-S3 (Subsonic): media, annotation, bookmarks, play queue, playlists, lyrics
**Files:** `internal/subsonic/{media.go,annotate.go,bookmarks.go,playqueue.go,playlists.go,lyrics.go,
listens.go}` + tests (media tests use fake `CoverSource`/`DownloadSource` serving fixture files).
Tests first:
- `TestSubsonic_StreamFormats` (`raw`/`flac`/`original` ⇒ master `audio/flac`; `mp3` ⇒ `audio/mpeg`;
  none + default raw ⇒ flac; none + `maxBitRate=128` ⇒ mp3; `format=opus` ⇒ mp3; a client with default
  `mp3` ⇒ mp3 and `transcodedSuffix` in `getSong`), `TestSubsonic_StreamRangeMatrix`.
- `TestSubsonic_DownloadCurrentTags` (fake source ⇒ `Content-Disposition: attachment;
  filename="<slug>__<seed>.flac"`), `TestSubsonic_CoverArt` (`al-…`, `tr-…` and a bare song id; `size=300`
  ⇒ the fake receives 320; unknown ⇒ 70).
- `TestSubsonic_LossyMasterStreamsMP3` (`archivetest.SyntheticArchive(t, "reference")`: a lossy track answers its master MP3 for
  `format=raw`, `flac`, `mp3` and any `maxBitRate`, `getSong` says `suffix mp3`, no `bitDepth`, no `transcodedSuffix`; `download` is the
  master unchanged), `TestSubsonic_ListenFormatMP3ForLossy`.
- `TestSubsonic_StarUnstar`, `TestSubsonic_StatusErrorsAreError0` (a fake `UpdateTrack` returning a
  `*library.ValidationError` ⇒ protocol error 0 with the fixed message, HTTP 200; no audit row with
  status 200), `TestSubsonic_SetRatingKeepsVerdict` (human rating 3 with verdict "too long" ⇒
  `setRating rating=5` ⇒ stars 5, verdict still "too long"; `rating=0` ⇒ stars cleared, verdict kept),
  `TestSubsonic_ScrobbleListenEvents` (false ⇒ one `start`; true ⇒ one
  `complete`; the identical call again ⇒ no new row; source `subsonic`, principal kind `device`),
  `TestSubsonic_ListenEventFields` (`session_id` `ss-` + 16 hex for scrobbles — same value for two
  scrobbles of one client on one UTC day, different on the next day or for another client — and `rp-` +
  16 hex for one `reportPlayback` session, 19 chars, inside M2a's 8–64 CHECK; `format` `flac` after a
  raw stream, `mp3` after an MP3 stream, the client default without a stream; `device_id` NULL),
  `TestSubsonic_ReportPlaybackSegments` (`starting` 0 → `playing` → `paused` 30000 ⇒ `start` + `segment
  [0, 30000]`; `stopped` at ≥ 90 % ⇒ `complete`; `ignoreScrobble=true` ⇒ none; `mediaType=podcast` ⇒ ok,
  none), `TestSubsonic_ScrobbleSuppressedAfterReportPlayback` (fake clock: `reportPlayback` `starting` …
  `stopped` at 95 % for (client A, track T) ⇒ one `start`, one `complete`; then `scrobble
  submission=false` and `submission=true` for (A, T) within 30 minutes ⇒ ok, **no** new event; the same
  scrobbles for (A, another track) or (client B, T) ⇒ recorded; for (A, T) after 31 minutes ⇒ recorded),
  `TestSubsonic_BookmarkCommentOnce` (comment "check the bridge" at 30000 ⇒ one comment
  `at_second 30.0`, source `subsonic`; same call ⇒ no new comment; new text ⇒ a second comment; no
  comment ⇒ none; `deleteBookmark` keeps the comments and hides the bookmark), `TestSubsonic_PlayQueue`,
  `TestSubsonic_PlaylistsCRUD` (create, add, remove by index, rename; delete ⇒ hidden; deleting the
  offline set ⇒ 50; a concurrent REST update ⇒ the bridge retries and succeeds), `TestSubsonic_Lyrics`
  (headers dropped, blank line between sections, `synced:false`, `lang` from the detected language,
  `und` otherwise; no lyrics ⇒ empty `lyricsList`), `TestSubsonic_WritesAudited` (one audit row per write,
  op `subsonic:<name>`, client `subsonic:<c>`, `args_sha256` independent of credentials and salt).
Done: every C109 endpoint implemented; `internal/subsonic` ≥ 90 %.

### Task M4a-S4 (Subsonic): schema contract, XML goldens, client fixtures, replay
**Files:** `internal/subsonic/subsonictest/{replay.go,placeholders.go}`,
`internal/subsonic/{schema_test.go,xml_golden_test.go,clients_test.go,fixtures_test.go}`,
`internal/subsonic/testdata/xml/*.golden.xml`, `internal/subsonic/testdata/clients/{amperfy,symfonium,tempus}.jsonl`.
Tests first: `TestSubsonic_JSONMatchesSchema`, `TestSubsonic_XMLGolden`, `TestSubsonicFixtures_Sanitised`,
**`TestAcceptance_M4a_SubsonicClients`** (heavy: an `archivetest.SyntheticArchive(t, "acceptance")` copy;
subtests per fixture file; expectations in each step; the Symfonium file asserts 1 artist, 7 albums and
9 songs for both empty-query forms and 0 songs at `songOffset=500`; the Amperfy file asserts the raw
stream steps (206, `Content-Range`, bytes equal to the master from `{{mid}}`), the API-window refusal
at 11 minutes and the media-window stream reuse at 11 hours (`advance`/`salt` fixture fields)),
**`TestAcceptance_M4a_SubsonicRawStreamSeek`**, **`TestAcceptance_M4a_SubsonicRatingSync`**.
Done: the three synthetic files replay green; adding a step for a newly found client quirk needs no
harness change.

### Task M4a-M1 (Media): cover art
**Files:** `internal/media/cover/{render.go,palette.go,prng.go,service.go}` + tests,
`internal/media/cover/testdata/cover.golden.json`, the `get_media_links` handler change (`cover`), the
importer's orphan scan (`Importer.libraryOrphans` in `internal/importer/report.go`: `derived/covers/`, `derived/dl/`; test
`orphans_m4a_test.go`); M1 plan C16.7/C19 amendments.
Tests first: `TestCover_*` (C110.1), `TestGetMediaLinks_Cover` (both URLs verify with the signer; human
12 h, agent 15 min; no DB write), `TestMediaHandler_PNG` (signed ⇒ 200 `image/png`, unsigned ⇒ 403),
`TestOrphans_IgnoreCoverAndDownloadCaches`, `TestSweep_Covers` (fake clock: 31 days ⇒ removed; a later
`EnsureTrack` recreates the same key).
Done: `internal/media/cover` ≥ 90 %; no new dependency.

### Task M4a-M2 (Media): downloads with current tags
**Files:** `internal/media/tagged/{write.go,service.go}` + tests, `internal/api/ops_download.go` (handler).
Tests first: `TestTagged_*` (C110.2), `TestGetDownloadLink` (filename, sha256 of the tagged file, tags;
agent TTL 15 min; heavy rate class; unknown track 404; a lossy track ⇒ the master's link, bytes and sha256), `TestBackup_ExcludesCoverAndDownloadCaches`,
`TestSweep_Downloads`.
Done: `internal/media/tagged` ≥ 95 %; `flac --test` passes on the output (heavy test, CI).

### Task M4a-A1 (Agent): offline set, prompts, session summary
**Files:** `internal/playlists/**` (role rules, C111), `internal/api/ops_offline.go`,
`internal/prompts/{prompts.go,render.go,text/*.tmpl}` + tests + goldens, `internal/api/ops_prompts.go`,
`internal/listening/session.go` + test; parity cases for `get_offline_set` and `get_prompt`; M2a plan
C33/C36 amendments.
Tests first: `TestOfflineSet_*` (C111), `TestPlaylists_RoleField` (`list_playlists` shows `role`),
`TestPrompts_RenderGolden` (each prompt; `write_variant` with and without `generate`),
`TestPrompts_CritiqueEmbedsContext`, `TestPrompts_SessionReview`, `TestPrompts_UnknownTrack404`, `TestPrompts_WriteVariantRefusesReferenceTrack`, `TestGetPrompt_Validation` (`critique_track` without `track_id` ⇒ 422; `since` older than 30 days ⇒ 422;
unknown name ⇒ 422 from the enum), `TestSession_Summary` (fixture events ⇒ plays, completed, skipped,
`seconds_heard`; agent events excluded).
Done: `internal/prompts` ≥ 90 %.

### Task M4a-A2 (Agent): MCP resources and prompts
**Files:** `internal/mcpgen/{resources.go,prompts.go}` + tests (HTTP and stdio builders),
`internal/mcpgen/testdata/{prompts_list.golden.json,resource_templates.golden.json}`; M2a plan C30.
Tests first: `TestResources_SpecsValid`, `TestResources_ReadEqualsTool` (PAT over HTTP and stdio),
`TestResources_ListEmpty`, `TestResources_UnknownIDNotFound`, `TestResources_ScopeRefusal`,
`TestPrompts_GetEqualsTool` (HTTP and stdio), `TestPrompts_ListGolden`,
`TestPrompts_WriteVariantDependsOnGenerateScope` (PAT with and without generate over MCP),
`TestMCPLists_Budget`.
Done: `tools/list` golden unchanged by A2 (resources and prompts are separate lists).

### Task M4a-W1 (Web): service worker, offline store, `/offline`, player integration
**Files:** `web/vite.config.ts` (`build.manifest: true`; the existing `keep-dist-placeholder` plugin
stays), `web/vite.sw.config.ts` (`build.emptyOutDir: false`), `web/scripts/precache.ts`,
`web/package.json` (`build` runs the main build, then the worker build, then `precache`; `check` also
type-checks `src/api/sw/tsconfig.json`), `web/tsconfig.json` (excludes `src/api/sw/**`),
`web/src/api/sw/{tsconfig.json,sw.ts,routes.ts,range.ts,chunks.ts,store.ts}` + tests,
`web/src/api/{offline.ts,swRegister.ts}` + tests, `web/src/lib/offline.svelte.ts`,
`web/src/lib/player.svelte.ts` (C115.4), `web/src/routes/Offline.svelte`, `web/src/lib/router.ts` (`APP_ROUTES`: the three routes) + `router.test.ts`, `web/src/App.svelte` (route
table: `/offline`, `/human/connectors`, `/human/subsonic` with placeholders; nav "Offline" only when
`offline.available`; register the worker), `web/eslint.config.js` (`caches` added to `banned`; D-M2b-13 is amended in the M2b plan text only — `src/api/**` is
already exempt), `web/src/main.ts`; plan amendments M1 C22.1/C22.3/C22.5,
M2b D-M2b-13.
Tests first: C115.5 unit tests (`range`, `chunks`, `routes`, `store`, `offline`, `offline.secure`,
`player.offline`, `precache`); `swBuild.test.ts` (after `bun run build`: `dist/sw.js` exists, is a classic
script (no `import`/`export`), contains the precache list and no `/api/` URL, and calls `clients.claim`;
`dist/index.html`, `dist/.vite/manifest.json` and `dist/.gitkeep` still exist — the worker build did
not empty the directory).
Done: `bun run lint` + `bun run check` clean (both TS projects); M1/M2a/M2b/M3 Playwright specs still pass
with service workers blocked (C117.6; `swRegister` tolerates the refused registration); plain-HTTP
non-loopback origins show no offline UI.

### Task M4a-W2 (Web): track page, cover art, Media Session artwork
**Files:** `web/src/lib/components/{CoverArt.svelte,DownloadFlacButton.svelte,OfflineButton.svelte,
PromptButtons.svelte}`, `web/src/routes/Track.svelte` (adds them), `web/src/lib/mediaSession.ts`
(artwork) + test, `web/src/lib/components/PlayerBar.svelte` (small cover).
Tests first: `mediaSession.test.ts` (artwork list from cover links; falls back to the icon; offline
uses the cached 512 cover), `promptButtons.test.ts` (one `get_prompt` call per click; char count),
`offlineButton.test.ts` (add ⇒ `update_playlist add_track_ids` with the current revision, then download;
409 `REVISION_CONFLICT` ⇒ re-read and retry once; remove ⇒ `track_ids` without it, then evict).
Done: track page calls `get_download_link`, `get_prompt`, `get_offline_set` (for the button state) and
`get_media_links` (cover).

### Task M4a-W3 (Web): `/human/connectors` and `/human/subsonic`
**Files:** `web/src/routes/{HumanConnectors.svelte,HumanSubsonic.svelte}`,
`web/src/lib/components/{GrantRow.svelte,SubsonicClientForm.svelte,SecretOnce.svelte}` + tests.
Tests first: `grantForm.test.ts` (scope checkboxes always include read; dollar inputs via `money.ts`;
empty ⇒ null (global default); one `set_connector_grant` per Save), `secretOnce.test.ts` (secrets live
only in component state, never in storage; navigating away clears them; idempotency key reused on
retry), `subsonicForm.test.ts` (username derived from name; "API key" and "Password" toggles; both off ⇒
submit disabled).
Done: both pages work in local mode behind step-up (M2a) and in proxy mode.

### Task M4a-I1 (Infra): routing contract, provider example, gate, coverage
**Files:** `deploy/labels.example.yml` (`mm-public` gains `Path(/sw.js)`),
`deploy/traefik-contract/contract_test.go` (C116 rows), `deploy/authelia-oidc.example.yml` +
`deploy/authelia_example_test.go` (`TestAutheliaExample_Shape`), `.gitleaks.toml` (`mms_` rule),
`internal/auth/nokeyliterals_test.go` (`TestNoSubsonicKeyLiterals`), `coverage.thresholds`, `scripts/ci-go-test.sh` and
`scripts/ci-selftest.sh` (media list: `./internal/media/tagged`, `./internal/subsonic`); M1 plan C24,
M2a plan C41 amendments.
Tests first: `TestRoutingContract` (new rows; `/.well-known/oauth-protected-resource/mcp` ⇒ `mm-agent`),
`TestAutheliaExample_Shape` (every client has `audience: [https://mm.example.com/mcp]`, implicit
audience mode, RS256 JWT access tokens, PKCE S256, scope `metamusic`, `consent_mode: explicit` and no
`pre_configured_consent_duration`, claims policy copying `mm_user` into the access token, the custom
authorization policy; `metamusic-claude` and `metamusic-probe` have `public: true` and
`token_endpoint_auth_method: none`; the Claude client has the vendor callback),
`scripts/public-gate-selftest.sh` gains a planted `mms_` key ⇒ blocked.
Done: `make routing` and `make gate` green.

### Task M4a-I2 (Infra): docs, device checklist, compose
**Files:** `docs/architecture.md` (M4a section), `docs/connectors.md`, `docs/subsonic.md`, `docs/runbook.md` (service-worker recovery, connector
revocation, Subsonic client rotation), `README.md`
(C102 variables; status line), `docs/device-checklist.md` (DC-M4a-1..8), `deploy/compose.example.yml`
(commented `MM_OIDC_*` examples with `auth.example.com`; no secrets).
Tests first: `TestDocs_ConfigTableListsEveryEnv` (README's table covers every `MM_*` variable parsed by
`config.Load`; extends the M1-era check if present, else new).
Done: docs are public-safe (`make gate`).

### Task M4a-B3 (Stage 3, Backend): wiring and cross-lane acceptance
**Files:** `internal/app/app.go` (real cover/download services into the Subsonic handler — replacing the
lane fakes — and the resolver chain), `cmd/metamusic/cmd_serve.go` (OIDC `Prefetch` goroutine, salt
prune, sweeps), `internal/mcpgen/acceptance_m4a_test.go`, `internal/acceptance/{m4a_registry_test.go,nosecretleak_test.go}`
(`TestNoSecretLeak_M4a`, `TestGETNeverWritesDB_M4a`, `TestNoPendingM4aStubs`).
Tests first: **`TestAcceptance_M4a_ResourcesAndPrompts`** (PAT, OAuth connector and stdio faces),
`TestNoSecretLeak_M4a` (C113 row), `TestGETNeverWritesDB_M4a`, `TestNoPendingM4aStubs`, `TestServe_SweepsAndPrunes` (fake clock: the
daily ticker removes a 31-day cover and an 8-day download; the hourly prune removes salts older than
`SaltWindowMedia` + 1 h and keeps younger ones), `TestServe_OIDCPrefetch` (with OIDC set, `/human/connectors`
shows `keys.count ≥ 1` before any connector request), `TestSubsonic_RealMediaSources` (the wired handler
streams a real cover and a real tagged download).
Done: `grep -rn 'STUB(M4a-' internal` empty.

### Task M4a-W4 (Stage 3, Web): Playwright M4a specs
**Files:** `web/e2e/{m4a-offline.spec.ts,m4a-track.spec.ts,m4a-human.spec.ts}`,
`web/playwright.config.ts` (global `use: {serviceWorkers: "block"}`; the serial M4a phase of C117.6 — offline spec on project
`chrome` only, track spec on `chrome` and `chrome-pixel7`, `m4a-human` in phase 1; clipboard permissions for the track spec); `test.use({serviceWorkers: "allow"})` only
inside `m4a-offline.spec.ts` and `m4a-track.spec.ts` (C117.6).
Specs: C117.6. Served-operation coverage: zero unserved ui ops (the 8 new ones included). The E2E leak
scanner (`/__e2e/leaks`) stays `[]`.
Done: `make e2e` green on chrome + firefox; CI e2e ≤ 5 min.

### Task M4a-X1 (Integration)
**Step 0 (done in the main merge: L1 merged first, the file is `0009_remote.sql`, `remoteMigration = 9`; the tools/list ceiling is 104 500 bytes, measured 104 472, and the "Reload to update" action waits for playback to stop):** rebase `m4a` onto `main`, `git mv internal/store/migrations/0008_remote.sql
internal/store/migrations/0009_remote.sql`, set `remoteMigration = 9`, regenerate `openapi.json`, `schema.d.ts` and the `tools/list` golden
(never hand-merge them), run the upgrade test from schema 8 on L1's fixture, and resolve the shared edits (`servertest.Options`,
`librarytest` fixture layering `FixtureL1` / `FixtureM4a`, `TestRegistry_OpCount`, `toolsCeilingBytes`) by re-measuring. If M4a merges
first, L1 performs the mirror steps. Then: merge the lanes into `m4a`; `make tools hooks check test e2e gate cover routing` green; regenerate
`openapi.json` + `schema.d.ts` (must be unchanged since B2 unless a recorded contract fix happened);
update `metrics/REPORT.md` (`make check` warm, e2e wall time, coverage of the new packages, tools/list
bytes); review (spec + quality + security): token verification order (allow-list before scope) and
pinning, the middleware's error mapping (no `WWW-Authenticate` on forbidden/revoked; `invalid_token` +
`resource_metadata` for every malformed bearer on `/mcp`), no OAuth token or Subsonic credential in
any log, audit row, idempotency row or response other than the issuing one,
`/rest/` never accepting a PAT, the salt windows, the global auth-failure class never refusing valid
credentials, connector limits on top-ups, GPL hygiene (no code from Subsonic servers or client
apps; attribution present), the service worker never touching `/api/`; copy D-M4a-1…26 (22 withdrawn)
into the private design document (§4.2 Subsonic and connector rows, §5.3 offline, §5.6, §9 M4a).

**Reconcile the later drafts' numbering (same commit series, private plans only):** the L2, L3, M5 and M6 drafts were written
against older numbers (L2 claims C102–C116 and migration 0008, L3 C117–C130 and 0009, M5 and M6 their own ranges and
migrations). M4a took **C102–C117** and migration **`0009`**. M4a-X1 therefore does **not** renumber those drafts
blindly: each renumbers when its own X1 task runs, after the highest number then in use on `main` and in the plans already built.
M4a-X1 only (1) records the numbers M4a froze in each draft's header note and (2) updates the M6 draft's assumed-name table to the
names M4a freezes:
   - A1 (Subsonic lyrics): there is **no** `subsonic.Options.Lyrics`; `getLyricsBySongId` reads lyrics
     through `library.Service` inside `internal/subsonic/lyrics.go` (D-M4a-13, unsynced), `songLyrics [1]`
     is advertised (C108.6), and the OpenSubsonic description is vendored at
     `third_party/opensubsonic/openapi.json` (not `internal/subsonic/testdata/`). M6 adds its own
     `Options` field if it needs word cues.
   - A2 (offline PWA): `web/src/lib/offline.svelte.ts` exports `available`, `status(trackId) → "none" |
     "partial" | "complete"`, `download(trackId) → Promise<void>`, `remove(trackId)`, `sync()`,
     `localSource(trackId)` (C115) — keyed by **track id** (the device picks the file format), not by
     sha; 2 MiB chunks, 206 ≤ 4 MiB, `persist()` on first download.
   - A3 (Subsonic playlists): unchanged — every non-hidden M2a playlist is a Subsonic playlist (C109).
   - A4 (cover art): there is **no** `cover.URL(trackID)`; covers are served as signed
     `/media/<key>.png` and reached through `get_media_links.cover.{url_512,url_1024}` (C113) or, in Go,
     `cover.Service.EnsureTrack(ctx, trackID, size) (serve.Object, key, error)` (C110.1).
   - Connector principals are `agent` with `Via "oauth"`; MCP resources register through `mcpgen.Resources` (C112.1); the management
     operations are admin-scope registry operations (D-M4a-24), not human-only.
(3) records the renumbering decision in both drafts' header notes.

### Task M4a-X2 (Deploy and provider configuration — **operator-assisted**; host details only in private notes)
1. **Prerequisites:** M4a merged to `main`; CI green; the restart window for the SSO provider agreed
   in advance (it logs every session out).
2. **Deploy** with the M3 runbook order (disarm → drain → deploy → arm; `deploy.sh` from a clean
   worktree). OIDC is still off.
3. **Smoke (agent may run these; no secrets involved):** `/.well-known/oauth-protected-resource/mcp` ⇒
   200 with no `authorization_servers` and `Cache-Control: public, max-age=300`; the root
   `/.well-known/oauth-protected-resource` ⇒ 404 problem; `/sw.js` ⇒ 200 without a cookie,
   `text/javascript`, `no-cache`; `/rest/getOpenSubsonicExtensions.view?f=json` ⇒ the 4 extensions;
   `/rest/ping.view?f=json&c=smoke&v=1.16.1` ⇒ error 10; `apiKey=` a **syntactically valid fake** PAT
   built at runtime in the shell (`mmp_` + 52 random base32 characters, never a real PAT) ⇒ error 44;
   `/mcp` with a fake JWT ⇒ 401 (OIDC off ⇒ row 1c).
4. **Operator:** merge `deploy/authelia-oidc.example.yml` into the provider configuration with the real
   domain and username (the Claude client is public: no secret; a ChatGPT client secret, if needed, is
   generated with the provider's hashing command, its plaintext kept in the operator's password manager
   and only the digest in the configuration), validate the configuration with the provider's
   `config validate` command, restart in the agreed window, and record the time in the private notes.
5. **Operator — reachability:** confirm the provider's discovery document
   (`/.well-known/openid-configuration`), token endpoint and JWKS URL answer (a) **from the public
   internet** without forward-auth, an IP allowlist or a geo filter in front of them (hosted clients call
   the token endpoint from vendor egress; e.g. `curl` from a machine outside the home network, or a
   phone on cellular), and (b) **from inside the app container** (`docker exec <app> wget -qO- <jwks
   url>` or equivalent; the app's verifier fetches discovery and JWKS from there). Record both in the
   private notes.
6. **Operator:** set `MM_OIDC_ISSUER` and `MM_OIDC_CLIENTS` in the compose environment (no secrets),
   redeploy; the protected-resource metadata now names the issuer; `/human/connectors` shows OIDC
   enabled and **`keys.count ≥ 1`** with `keys.last_error` null (the eager fetch, C105) — a 0 here is fixed
   before any client is tried.
7. **Operator:** run `metamusic oidc probe --server https://mm.example.com --client-id metamusic-probe`
   from a terminal with a browser at hand; accept the consent page; paste the redirected URL; all checks
   must say ok, including `server verify: CONNECTOR_FORBIDDEN (ok)` (the server verified the probe token
   with its own keys; no grant row appears on `/human/connectors`). If `mm_user` or `aud` is missing, fix
   the claims policy or client audience (another restart only if the probe proves it necessary). Record
   the (redacted) report in the private notes.
(The parity-exception and §4.2 sign-offs moved to M4a-X0, before Stage 1.)

### Task M4a-X3 (Real clients and device checklist — **operator-assisted**)
1. **Hosted connectors:** DC-M4a-1 (claude.ai web), DC-M4a-2 (claude.ai mobile, next-day refresh — and
   the `offline_access` decision of D-M4a-4), DC-M4a-3 (ChatGPT, if available), DC-M4a-8
   (revoke/restore). After the first connection, the operator reviews `/human/connectors` (grant
   `read,write`, generate off) and decides limits.
2. **Native players:** create one client per app on `/human/subsonic`; DC-M4a-4 (Amperfy, token login,
   Raw, pause-15-minutes resume and seek), DC-M4a-5 (Symfonium API key or Tempus token login). A client
   quirk found here is fixed in a PR that adds a synthetic step reproducing it to that client's fixture
   file (C117.5); no request recording.
3. **PWA:** DC-M4a-6 (offline set, airplane mode, both phones), DC-M4a-7 (cover art on the lock screen).
4. Tag the release per the Definition of done.

## Definition of done (M4a)
- All named tests green; `make check` < 30 s warm; `make test` green with media tools; `make e2e` green
  (chrome + firefox) with zero unserved ui ops and an empty leak list; `make gate`, `make routing` green;
  coverage gates (C116) met on merged unit + e2e data.
- A hosted connector authorises through the SSO provider and runs `search_tracks` and `add_comment`
  (CI against the stub; DC-M4a-1/2 on the real provider, results in the private notes).
- A native client plays a raw 24-bit/48 kHz FLAC with seeking and syncs ratings (CI: the synthetic
  client fixtures and the acceptance tests; DC-M4a-4/5 on real phones, results in the private notes).
- The offline set plays offline (Playwright; DC-M4a-6).
- No OAuth token, Subsonic credential, client secret, real hostname, username or device name in any
  tracked file, log line or non-issuing response; the public plan copy contains nothing below the
  private-appendix marker.

## Risks / open questions
- **Provider token details** (RFC 9068 `typ`, `scope` vs `scp`, whether `aud` carries the resource,
  whether the custom claim reaches the access token, refresh tokens for a public client) are documented
  but unverified on the real instance. Mitigations: `oidc probe` before any hosted client (M4a-X2 step
  7, which ends with a server-side verification round trip); the eager JWKS fetch shown on
  `/human/connectors`; the verifier accepts `scope` or `scp`; the stub reproduces the documented
  behaviour and has its own tests; fallback for refresh: a confidential client.
- **Provider reachability:** hosted clients reach the provider's token endpoint from vendor egress, and
  the app fetches JWKS from inside its container; a forward-auth rule or IP filter in front of either
  breaks connectors in ways the stub cannot show. Mitigation: M4a-X2 step 5 checks both paths before
  the first client.
- **Hosted-client OAuth behaviour** (scope selection from `scopes_supported`, honouring a 403
  `insufficient_scope` step-up, client-secret method for confidential clients) varies by vendor and
  version. Mitigations: public client for Claude (no method ambiguity), implicit audience, scopes
  configurable through `MM_OIDC_SCOPES`, each failure mapped to a distinct problem code that the
  operator can read on `/human/connectors` (`keys.last_error`) and in logs.
- **Refresh tokens without advertising `offline_access`** (D-M4a-4): go-sdk requests it from the
  authorization server's metadata, but another hosted client might only read the protected-resource
  `scopes_supported`. DC-M4a-2 decides; the override `MM_OIDC_SCOPES="openid offline_access metamusic"`
  (an MCP SHOULD-NOT) is the fallback, at the cost of nothing but the deviation.
- **Consent page on every authorization:** Authelia forces explicit consent whenever `offline_access`
  is requested, so each first connect and every re-login shows it (refreshes do not). Accepted; docs
  and DC items say so.
- **Subsonic client quirks** (XML attribute sets, empty-element expectations, paging assumptions) are
  only partly documented; the schema contract test, the synthetic fixtures and the device checklist
  catch them. A sanitising request recorder (withdrawn D-M4a-22) is reconsidered only if the checklist
  finds quirks these miss.
- **Token/salt authentication uses MD5 by protocol design**; mitigated by per-client random app
  passwords (not reused anywhere), TLS, the salt windows (12 h only on read-only media endpoints),
  failure throttling and API keys for clients that support them.
- **Service-worker regressions can strand an installed app on an old shell.** Mitigations: navigations
  are network-first, shell caches are versioned and pruned on activate, `/sw.js` is `no-cache` and
  public, the worker never caches API responses. Runbook entry: a broken worker is replaced by
  deploying a fixed `/sw.js` (browsers check it on every navigation).
- **iOS storage and media in standalone mode** (quota heuristics, `persist()` refusal, 206 handling from
  a worker) can only be verified on devices (DC-M4a-6).
- **Disk use of caches** (`derived/dl/` holds full FLAC copies): 7-day sweep; the operator watches
  `get_stats.storage` in the first weeks.
- **Third-party transcripts:** connectors receive signed media links (15 min TTL) and agent-masked
  principal references (D-M2a-19), unchanged from PAT agents.
- **Agent-reachable credential management (D-M4a-24, rev 3).** An admin-scope PAT agent can create Subsonic credentials and change
  connector grants (including `generate` and budgets). Bounds: `admin` is never a grant or device scope; the global caps, tokens and
  the approval rules stay human-only; every call is audited and pushed (A-1 open for human list views); the one-time secrets appear
  only in the issuing response (an MCP transcript holds them — rotate a Subsonic client by revoking and re-creating). Operators should
  give `admin` to a PAT only deliberately; `create_token` stays human-only.
- **Step-up (rev 3).** In local-auth mode a stolen session cookie could now call these `/api/` operations without the `/human/*`
  step-up (the page load keeps it, the API calls do not). Proxy mode (production) is unaffected: the same forward-auth session applies
  either way.
- **Lossy references in Subsonic and offline (D-M4a-25).** A library of MP3-only reference takes plays through the same endpoints as an
  MP3; clients that assume FLAC for album art or download names are covered by the device checklist only.
- **L1 interplay.** L1 is built in parallel: both extend `servertest.Options`, the fixture layering, `TestRegistry_OpCount` and the
  tools ceiling; the later merge re-measures them (M4a-X1 step 0). L1's vocabulary (Muse, Song, Pick) never reaches Subsonic clients
  (L1 C99); album naming stays the root's title.

