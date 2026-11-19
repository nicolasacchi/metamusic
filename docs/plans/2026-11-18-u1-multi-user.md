# U1 — multi-user — Implementation Plan (design; not built)

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (TDD per task: named tests first, watch them
> fail, implement). Read `CLAUDE.md` (public-repo rules, `make check`), `docs/architecture.md` and the M0, M2a, M3, M4a, L1, L2, L3,
> M5 and T1 plans first. Vocabulary is unchanged (Muse, Song, Take, Pick, Session, Sound, Record, Crate); do not rename a noun.
> Contracts are **C301–C315** (the highest allocated number today is C300, reserved for M6; U1 takes the next block, and M6 renumbers
> if it still starts at C300). Decisions are `D-U1-n`. Task ids carry the `U1-` prefix; lane labels (`MM_LANE`) are `u1-core`,
> `u1-ratings`, `u1-view`, `u1-editor`, `u1-web`, `u1-int`.
>
> **Public/private split.** Everything here is public-safe: example users are `alice` (owner), `bob` (editor), `carol` (listener); the
> host is `mm.example.com`; fixtures use synthetic canaries (`CANARY-COST-900`, `CANARY-MUSE-900`, `CANARY-NOTE-900`). No real
> username, artist, hostname, lyric or Muse text may appear in code, tests, goldens or docs. `make gate` must stay green.
>
> **Money and secrets (binding).** Unchanged from M3. U1 adds no spend path. It narrows who may reach the existing ones. No agent
> arms the provider, approves a batch, confirms a spend or edits caps while building or testing U1; tests use the fake backend.

**Goal.** One library, several people. The operator decision of 2026-11-18: three roles over the same library; each user has their own
rating and verdict per track; login stays Authelia SSO through the existing proxy (`Remote-User` + `X-MM-Edge` + trusted peer) and the
app maps each username to a role; a Listener sees the whole library read-only except discarded takes, and never sees costs, spend,
provider status, audit, tokens, private notes or private Muse text.

| Role | Intent |
|---|---|
| **owner** | Today's single human. Everything: spend, approvals, caps, publishing, tokens, admin. Exactly one in v1. |
| **editor** | Creates and edits drafts, playlists, crates and records (unpublished); requests generations that always wait for Owner approval; cannot approve spend, change caps, publish, or administer. |
| **listener** | Listens and rates. Read-only view of the library minus discarded takes and minus every private or money field. |

**Architecture (one paragraph).** A user is a username from a config map; a role is an enum on `Principal`, resolved from config when the process
starts (revocation is "edit `MM_USERS`, restart"; there is no live reload in v1, D-U1-4). Roles map to scope sets; two new scopes (`listen`, `rate`) let a Listener hold scopes that no
existing op except the listen/rate ops requires. One pure function `api.Permitted(p, meta)` is the only gate; REST (`Authorize`),
MCP (tool list, `describe_operation`, `call_operation`, resources, prompts) and the SPA (`whoami.allowed_ops`) all call it, so parity is
structural. A new `Meta.OwnerOnly` flag is the fail-closed marker for spend, approval, publishing and administration. Field-level
redaction for Editors and Listeners lives in one package, `internal/view`, called by the op handlers (so REST, MCP and the SPA share
it). Ratings become per rater; every curation signal keeps reading the Owner's rows only. No users table: the DB stores the rater key
in the rows that need one and nothing else.

## Draft judgement and what was kept

Two drafts were compared on safety (no escalation, no Listener leak), parity, smallest correct change and testability.

| Criterion | Draft A (scopes, config users) | Draft B (`MinRole`, users table) | Taken |
|---|---|---|---|
| Safety: Listener | Listener holds only `listen`+`rate`; an unclassified op is unreachable by scope alone | Listener holds `read,write`; one missed `MinRole` is a leak | **A** |
| Safety: Editor/new ops | An unclassified read op defaults to Editor-visible | Missing `MinRole` defaults to owner (fail closed) | **B**: the golden matrix fails any op not classified; `OwnerOnly` is mandatory by validation for admin, human-only and approve ops |
| Parity | One `Permitted`, filtered MCP per capability set, `whoami.allowed_ops` | Same idea | **Same**; one function, three consumers |
| Smallest change | Config map, no new table, only the ratings table rebuilt | Users table, three FK columns, user admin ops and page | **A**, with B's fixed `owner` rater key so the migration needs no config and a rename is free |
| Redaction | Pointer+`omitempty` next to `RefFor` | A shared `internal/view` package | **B** (one place, archtest-able) |
| Startup safety | Validate env, refuse to start | `EnsureOwner`, refuse on mismatch | **Both**: strict validation (D-U1-3) |
| Testability | Golden role matrix, leak sweep, archtest | Same, plus view goldens | **Merged** (C314) |

Rejected from B: the users table and `/human/users` page (YAGNI for a handful of people; Authelia is the identity source), `MinRole`
as a second axis, and Listener ownership of `read,write`. Rejected from A: letting a missing classification default to Editor access
(replaced by the matrix test) and rater = raw username for the Owner (replaced by the fixed key `owner`).

## Dependencies and sequencing

Builds on `main` (migrations to `0016_billing_backend.sql`; next free number at build time, currently **0017**; U1 adds two migrations: ratings per user (0017) and the additive device owner key (**0018**); the loader rejects
gaps). Order is fixed by risk: **the privilege-escalation fix lands first with a single Owner, and no non-owner account is provisioned
in production until lane `u1-view` and the leak sweep are merged.** Deploys are never unattended and the env is validated first
(deploy-safety rule: a malformed env once caused a 17 h outage).

## Decisions recorded by this plan

- **D-U1-1** Roles are `owner`, `editor`, `listener`. Exactly one Owner in v1 (the operator's "Owner = today's single human"; confirmed 2026-11-18, Q11 resolved).
- **D-U1-2** Users are a config map `MM_USERS=alice:owner,bob:editor,carol:listener`; no users table. `MM_TRUSTED_USERS` stays as an
  alias meaning "owner" and must then hold exactly one name. Production may list several names there today, so the rollout checks the value first and
  `metamusic config check` (C301.8) validates before any restart.
- **D-U1-3** Startup refuses only on config errors that affect the Owner or make the map ambiguous: no owner, two owners, duplicate (after case folding), empty or
  comma-containing names, unknown role, the reserved names `owner`, `critic`, `system`, `user`, `human`, `MM_AUTH=local` with a non-owner user. Names are
  lowercase: the `Remote-User` value and the `MM_USERS` keys are both folded to lowercase before matching, reserved-name checks and use as a rater key. An unknown
  proxy user stays anonymous (401), never a role. **Orphan raters never stop startup** (D-U1-4).
- **D-U1-4** `Principal.Role` is derived from the config map and never stored in a session, device or token row. The map is read at startup
  (`auth.Middleware` takes the config by value; there is no reload path in `cmd` or `internal`), so **a demotion or removal takes effect on the next restart**, and the
  runbook says so. Removing a user from `MM_USERS` is pure revocation: ratings they left stay as inert rows, readable by the Owner as "former user <name>", and are never a
  reason to refuse to boot. An optional follow-up (SIGHUP or file-backed atomic reload) is open question Q17; nothing in v1 depends on it.
- **D-U1-5** Scopes: add `listen` and `rate`. `Has(listen)` holds for `listen` or `read`; `Has(rate)` for `rate` or `write`. Owner and Editor
  keep the legacy scopes (Editor without `admin`); Listener holds only `listen`, `rate`. Tokens and connector grants carry only the legacy four.
- **D-U1-6** `Meta.OwnerOnly` (`x-mm.owner_only`) means "refused for any principal whose role is Editor or Listener" (a human, or a device or Subsonic client that inherits that role, D-U1-14). Principals with no role (agent tokens, connector
  grants, system) and the Owner's own devices and Subsonic clients are Owner delegates: `Permitted` does not apply `OwnerOnly` to them, and they pass or fail on scope exactly as today. `HumanOnly` keeps its meaning
  (not an agent or device) and is unchanged. `validate()` requires `OwnerOnly` on every op that is `HumanOnly`, `cost=approve`, scope `admin`, on every scope-`read` op on the sensitive
  list (audit, tokens, backups, imports, spend, ledger, provider, connectors; devices and Subsonic clients are not on it, they are self-service with an ownership guard, D-U1-14), and on the ops named in C305. Agent-facing ops (`propose_pick`,
  `next_duel`, `submit_duel`, `submit_callback_verdict`, `revoke_own_device`, `revoke_own_token`) are not tagged for the sake of the human rule; C305 states each.
- **D-U1-7** `ActsAsHuman()` is removed and replaced by `IsPerson()`, `IsOwner()`, `CanSeePrivate()`; an archtest bans the old patterns outside
  `internal/auth` and `internal/view`.
  A fourth predicate, `IsOwnerSide()` (= role is not editor or listener, whatever the kind), is what service guards use where a device or agent legitimately acts (C303).
- **D-U1-8** Ratings are keyed `(track_id, source, rater)`. `rater` is `owner` for the Owner, the username for others, `critic` for the agent
  row. The existing human ratings (including device-origin ones) become `owner`. A rating from a device or Subsonic client attributes to the user who paired it (D-U1-14).
- **D-U1-9** Curation signals (picks, song state `judged`, Atlas, next actions, provenance, ladder, stats histograms) read `owner_ratings` only.
  Viewer-facing displays (stars shown, star sort and filter) use the viewer's own row.
- **D-U1-10** Duels, callbacks, picks, voice notes, listen heat, the offline set and Subsonic bookmarks stay Owner-only in v1. A device or Subsonic client of an Editor or Listener reaches none of them (an offline set for non-owner phones is Q22, deferred).
- **D-U1-11** Editor generation always creates a batch in `awaiting_confirmation` (reason `editor_batch`), ignoring auto-approve; only the Owner approves. It uses the existing
  global caps and the existing per-batch reservation. v1 adds no Editor-specific cap column, pending-batch limit or notification coalescing (deferred, Q1).
- **D-U1-12** A Listener never receives a money field (`*_micro_usd`, `*_usd`, `display_*`), provider, audit or token data. Redacted fields are
  pointers with `omitempty`: absent, never zeroed. Discarded takes are `404 NOT_FOUND` for a Listener, never 403.
- **D-U1-13** Listener signed media links use the 15 minute agent TTL and are never minted for discarded takes; links already minted stay valid
  until expiry (accepted, documented). `TTLFor` is by role, not kind: 15 min for any Listener-role principal (a Listener's browser, and equally a Listener's paired device or Subsonic client); 12 h for the Owner, Editors and their devices.
- **D-U1-14** (revised 2026-11-18; replaces the Owner-only rule, Q3 resolved) Editors and Listeners may pair their own phone or browser and use Subsonic apps under their own identity. A device or Subsonic client belongs to the user who paired it (an `owner_key` on the row: `owner` for the Owner, the username otherwise), carries that user's role and never more (effective scopes are clamped to `ScopesFor(role)`), and its ratings and plays attribute to that user (`RaterKey`). The user starts pairing themselves after SSO login (a human-only, step-up-protected action; a device or agent cannot pair); the pairing code is bound to the pairing user. The Owner can list and revoke every device and Subsonic client; Editors and Listeners list and revoke only their own. A demoted or removed user's devices lose the old access on the next restart (D-U1-4, restart-to-revoke): clamped to the new role, or refused if the user is gone; the Owner can also revoke any device at once. Devices are therefore **no longer automatic Owner delegates**: a device acts as its user's role (C302, C303, C312). Tokens and connector grants stay Owner-only and remain Owner delegates, as do agents and system; the Owner's own devices and clients behave exactly as today.
- **D-U1-15** Listener sees only their own name; other users appear as `user`. Editor sees usernames. Owner sees all.
- **D-U1-16** Listener does not see an average rating in v1 (anchoring); Editor sees own plus average; Owner sees each user plus average (confirmed by the operator 2026-11-18, Q5 resolved; the same shaping applies to a Listener's or Editor's device and Subsonic client).
- **D-U1-17** (revised 2026-11-18, Q18 resolved) A Listener never receives a master, the master's sha256, or anything that embeds the hidden fields. Frozen masters carry `MM_SEED`, `MM_REQUEST_ID`, `MM_RECIPE_ID`; tagged renditions and exports add `MM_TAKE_ID` and
  `MM_STATUS`, and `get_media_links` returns the master `sha256`. A Listener gets exactly two audio renditions, both stripped of every `MM_*` key (allow-list: only a `TITLE` tag survives): **MP3 320** (`mp3_320_clean`) and **lossless FLAC** (`flac_clean`, C308.7). Media links, downloads and Subsonic streams for a Listener resolve only to those two; the download filename never
  uses the seed; the Listener `get_media_links` shape has no `sha256` field and its URLs carry the rendition's own hash, never the master's. Both are built on demand and cached (lane V3). This applies equally to a Listener's device and Subsonic client.
- **D-U1-18** Transforms never spend (CPU quota only). Editors may create, estimate, list, get and wait on transforms and cancel their own; Listeners are refused. Editors draw from their own quota pool, not the Owner's `human` pool.
- **D-U1-19** Deferred until the operator asks: an Editor daily pot, `PENDING_LIMIT`, push coalescing, `MM_RATER_REMAP`, `list_users`, the audit `principal_role` column, per-role MCP servers, Editor-minted agent tokens (Q16 resolved 2026-11-18: Editors are web app plus their own paired phones only, no tokens, no MCP), an offline set for non-owner phones (Q22).

## Baseline reconciliation (read in the `u1-design` worktree, `origin/main`)

| Fact | Where | Consequence |
|---|---|---|
| `humanPrincipal` grants every scope to any listed username | `internal/auth/principal.go` | Role must be assigned there |
| `ActsAsHuman()` = human or device; 34 non-test lines (re-counted with `rg`, definition included) | `internal/auth/principal.go` and callers | Each caller is decided in C303 |
| `Authorize` (in `registry.go`) checks `Kind == human` then scope only; `permit.go` is a new file | `internal/api/registry.go` | Any Editor or Listener would pass every `/human/api` op today |
| `Meta` has `Scope, Cost, MCP, UI, HumanOnly, …` | `internal/api/registry.go` | Add `OwnerOnly` |
| `proxyPrincipal` checks `slices.Contains(cfg.TrustedUsers, user)` | `internal/auth/middleware.go` | Becomes a role lookup; header checks unchanged |
| `ratings` PK `(track_id, source)`, CHECK `source='human'` ⇔ kind human or device, WITHOUT ROWID, triggers `ratings_no_delete`, `ratings_frozen`; provenance triggers `pd_ratings_ai/au` | `0003_judge.sql`, `0008_provenance.sql` | One non-additive migration |
| `TTLFor`: `ActsAsHuman` gets 12 h | `internal/media/serve/sign.go` | Listener must get 15 min |
| The MCP server is built once; the generic ops list every op | `internal/mcpgen` | Per capability set filtering (C304) |
| The gate audits `p.Kind == KindAgent` or (`HumanOnly` or `Scope != ScopeRead`) and holds the response of audited calls | `internal/gate/gate.go` | Retagging browse ops to `listen` must not make every GET audited (C304.4) |
| Config is read once and passed by value; no SIGHUP or reload | `internal/auth/middleware.go`, `cmd` | Revocation is restart-only (D-U1-4) |
| `/mcp` is served only to `Kind == KindAgent` (others get 401) | `internal/httpserver/server.go` | Editors and Listeners have no MCP path in v1 (C304; Q16 resolved: deferred) |
| `p.Kind != auth.KindAgent` / `== KindAgent` is used as "the operator" in transform cancel, backlog gate, quota pool, jobs and caps | `transform/runner.go`, `generation/patches_m5.go`, `transform/quota.go`, `jobs/service.go`, `spend/caps.go` | The archtest covers these too (C303) |
| Device bearer tokens and Subsonic credentials build a device principal today with fixed scopes `read,write` and no user; the `devices`, `subsonic_clients` and pairing-code tables have no owner column | `internal/auth` bearer path and the device and Subsonic stores (lane D1 re-reads the names) | Add `owner_key`; role resolved from it (C301.11, C312) |

Citations other than the ones above came from subsystem maps and **must be re-read by the owning lane** (record corrections in the
Revision log). The roughly 25 `FROM/JOIN ratings` lines (about 20 packages), the 34 `ActsAsHuman` lines and the leak surfaces below are enumerated by
`rg`, not by this list. Task F1 starts with a re-verification step that writes corrected counts to the revision log before the contracts freeze.

## Contracts (all lanes build against these — do not change without updating this file)

### C301. Roles, config and principal resolution (`internal/config`, `internal/auth`)

1. `auth.Role` is `owner|editor|listener`, set for **human** principals, and inherited by a paired device or Subsonic client from the user who paired it (D-U1-14); empty for agent, connector-grant and system principals, which are Owner delegates (D-U1-6). `Principal` gains `Role Role` and `User string`
   (the username, lowercase; equals `ID` for humans; for a device or Subsonic client it is the owner's username and `ID` stays the device or client id).
2. Config: `Users map[string]auth.Role` replaces `TrustedUsers []string`; parsed from `MM_USERS`. A bare name means owner. Validation per D-U1-3,
   returned as a startup error with the offending name, never a panic.
3. `proxyPrincipal`: the trusted-peer, single `X-MM-Edge`, single `Remote-User`, non-empty and no-comma checks are **unchanged**; the
   membership test becomes `role, ok := cfg.Users[user]`; not ok returns `Principal{}, false`. Then
   `humanPrincipal(user, role, "proxy")` with `Scopes = ScopesFor(role)`. Device bearer tokens and Subsonic credentials resolve the same way (lane D1): the row's `owner_key` (`owner` means the configured Owner, anything else is a `cfg.Users` lookup) gives `Role` and `User`; a key absent from the map (removed user) yields 401; effective scopes are the stored device scopes clamped to `ScopesFor(role)` (a Listener's device holds `listen`, `rate` only; an Owner's keeps `read,write`). The clamp is defined through the `Has` implication, not set intersection: `effective(role, stored) = { x in ScopesFor(role) : stored.Has(x) }`, so stored `read,write` gives a Listener `{listen, rate}` (read implies listen, write implies rate; `read` and `write` themselves are not in the Listener set), an Editor and the Owner `{listen, read, rate, write}`; `generate` and `admin` are never granted because a device's stored scopes never imply them. Unit test table in C314.1: stored `read,write` (and `read` only, `write` only) x each of owner, editor, listener.
4. The same lookup replaces the flat list in the connector verifier and in the OIDC config check. The verifier requires `Role == owner` for the
   granting user (a connector is an Owner delegate).
5. Local auth (`human_account` single row) stays Owner-only; it is a dev mode.
6. `ScopesFor`: owner `{listen, read, rate, write, generate, admin}`; editor `{listen, read, rate, write, generate}`; listener `{listen, rate}`.
7. `RaterKey(p)`: human owner → `owner`; other human → username; device and Subsonic client principals → their owner's key (`owner` for the Owner's devices, otherwise the username, D-U1-14); agent → `critic`.
   Guard: the owner key is written only by the server from the authenticated pairing user, never from a request field, and a device cannot change it; token minting stays `OwnerOnly`. That is what makes the device rule sound.
8. **`metamusic config check`** runs the full config validation (D-U1-3) without starting the server and prints the resolved user-to-role map (names only) and any deprecated `MM_TRUSTED_USERS` alias use. Startup runs the same function.
9. Case: `Remote-User` and `MM_USERS` keys are lowercased before comparison; validation rejects names that collide after folding.
10. Orphan raters (a human rating row whose rater is neither `owner` nor a configured name) are a startup **warning**, a metric and a `library verify` finding. Never a refusal.
11. **Device owner key.** Migration `<next+1>_device_owner.sql` (currently 0018, additive): `owner_key TEXT NOT NULL DEFAULT 'owner'` on `devices`, `subsonic_clients` and the pairing-code table. Every existing row becomes the Owner's, so current phones and clients behave as before. Rows are never physically deleted (revocation is a status flag, CLAUDE.md rule 5), so joins from history stay valid. Table and column names are re-read by lane D1.

### C302. One gate: `Permitted`, `Authorize`, scopes, `Meta.OwnerOnly` (`internal/api`, `internal/auth`)

1. `api.Permitted(p auth.Principal, m Meta) *Problem`, in this order: anonymous → 401 `UNAUTHENTICATED`; `HumanOnly` and `p.Kind != human` →
   403 `HUMAN_ONLY`; `OwnerOnly` and `p.Role` is editor or listener (a human, or a device or Subsonic client of such a user) → 403 `OWNER_ONLY` (principals with no role, and the Owner's devices, skip this rule, D-U1-6); `!p.Has(m.Scope)` → 403 `FORBIDDEN_SCOPE` (details and the connector
   hint unchanged). All problem+json with stable codes. The golden matrix pins agent-read, agent-write, agent-admin, device (the Owner's), device-editor and device-listener rows so a regression for the Owner's agent or phone, or a leak through a non-owner's phone, fails.
2. `Authorize(ctx, meta)` (today in `registry.go`) becomes a thin wrapper. The registration wrapper, the request gate and in-process MCP dispatch call it today; F1 re-verifies each call site.
3. `validate()` and `ValidateMeta`: `HumanOnly`, `cost=approve` and scope `admin` require `OwnerOnly`; the `listen` and `rate` scopes are added to
   the accepted set; `allScopes`, the token allow-list and connector canonical scopes stay legacy-only (tokens may not carry `listen` or `rate`).
   `TestEveryOpHasValidMeta` mirrors the new field (it re-declares the Meta fields).
   A `validate()` rule also forces `OwnerOnly` on scope-`read` ops on the sensitive list (D-U1-6), because several of them (`get_audit`, `list_tokens`, `list_backups`, `list_import_runs`, `get_spend`, `list_ledger`, `get_provider_status`) are scope `read`, not `admin`.
4. `Principal.Has` implements the implication in D-U1-5, so existing tokens, connectors and OpenAPI consumers behave unchanged.
5. `x-mm.owner_only` is emitted into `openapi.json`; `make openapi` regenerates it and `web/src/api/schema.d.ts` (commit both).
6. Service-level defence in depth: `spend.ApproveBatch`, `RejectBatch`, `records` publish, `curation` settings, `picks` pin, provider state and
   spend caps additionally require `by.IsOwnerSide()` (and are `HumanOnly` or `OwnerOnly` at the op), so an Owner-side device or agent that legitimately reaches them today keeps working while an Editor or Listener, human or device, cannot. F1 names the exact functions after reading them.

### C303. `ActsAsHuman` split and the archtest (`internal/auth` and callers)

1. `IsPerson()` = human or device (what `ActsAsHuman` meant). `IsOwner()` = human with role owner. `IsOwnerSide()` = role is not editor or listener, whatever the kind: the Owner, the Owner's devices, agents, connector grants, system. A device or Subsonic client of an Editor or Listener is **not** owner-side: devices are no longer automatic Owner delegates (D-U1-14). `CanSeePrivate()` = role owner or editor (human or device), or no role (agents, system, as today); never a Listener's human or device. Predicates read the role, not the kind.
2. Each caller is decided by the lane, per this table (re-read every site; the table is the rule, line numbers are not):

| Predicate | Sites (by purpose) |
|---|---|
| `IsPerson` | rating source decision (human vs critic); tag source; listening principal |
| `IsOwner` | pick pin; taxonomy proposal decide; record update and publish rules; curation settings; local auth |
| `IsOwnerSide` | sites an Owner device or agent reaches today and that are OwnerOnly for Editors and Listeners: voice notes (the Owner's devices accepted), callback verdicts, duel source (`duels.SourceFor`: human or device → `human`, agent → `critic`), `callbacks.sourceOf`, `propose_pick` (stays agent-only; the Owner and every human are refused as today), nextactions human items, `generation` via `human_create` |
| `IsAgent` (kind check, outside `auth`) | transform cancel own-only rule, callbacks backlog gate (`gateBacklog`, which must also apply to Editors), transform quota pool, `jobs` and `spend` caps: each decided by the lane; an Editor is never treated as "not an agent, therefore the operator" |
| `CanSeePrivate` | draft reference text; Muse text and Muse readers; Atlas Muse layer; audit refs; connector refs |
| role-aware, bound to the caller (`owner_key`) | device pairing, Subsonic client admin and the step-up proxy: none of them carries an `IsOwner` guard (a Listener must be able to pair). Step-up applies to any human: it is enforced at the edge by the `^/human/` two-factor rule for every user (C312.1), and the SPA's `mm:stepup-done` refresh (C313.1) is role-independent; the lane re-reads the step-up proxy and removes any Owner check there. Pairing and client creation bind to the caller's owner key, list and revoke are own-only (Owner: all). `export_record_tape` is not an `IsOwner` site: it follows C305 (Editor Y, Listener N) |
| role-aware | `RefFor`/`KeyFor` (C309); signing TTL (D-U1-13); `view.For`; `RaterKey`; device and Subsonic principal construction (C312) |

3. **Archtest** `internal/archtest/role_predicates_u1_test.go`, in the style of `muse_readers_l3_test.go`: bans `ActsAsHuman`, `Kind != auth.KindHuman`, `Kind == auth.KindHuman`,
   `Kind != auth.KindAgent` and `Kind == auth.KindAgent` outside `internal/auth`, `internal/view`, test files and an allow-list file with a reason per site (the agent-only rules above). A missed site would silently give an Editor
   or Listener owner powers.

### C304. MCP and SPA parity (`internal/mcpgen`, `internal/api/ops_system.go`)

1. `/mcp` is served only to agent principals today (`httpserver/server.go`: any other kind gets 401), tokens are Owner-minted (C312) and carry legacy scopes only. So **Editors and Listeners have no MCP path in v1**; there is no per-role MCP server and no per-set cache.
   `TestEditorListenerMcp401` pins it. Agent principals keep exactly today's tool list; `Permitted` is applied to them with unchanged outcomes (the golden matrix agent rows). Editors have the web app (and their own paired phones, D-U1-14) and no agent tokens (Q16 resolved 2026-11-18: deferred, D-U1-19). Devices and Subsonic clients never reach `/mcp` (agent principals only), whatever their owner's role.
2. `describe_operation`, `call_operation`, `tools/list`, resources and prompts call the same `Permitted`. A forbidden op id answers `NOT_FOUND`, identical to an unknown id. A pre-U1 golden of the Owner agent's tool list must stay identical.
3. `whoami` (`GET /api/me`) gains `role`, `user` and `allowed_ops []string` (computed by `Permitted` over `Operations()`); nullable or additive only. This is how the SPA shows Editor and Listener shells (the SPA is the only role-bearing consumer).
4. **Audit gate rule changes with the retag.** The current predicate audits any call that is agent, `HumanOnly`, or `Scope != read`, and holds those responses. After the retag of browse ops to `listen` that would audit every browse GET. New rule: audited = agent, or `HumanOnly`,
   or scope not in `{read, listen}`. `write`, `generate`, `admin` stay audited and so does `rate_track` (a low-frequency write; the audit row carries refs only, never the request body, so the 280-character verdict is **not** copied into the audit row, and its response hold is the small rating shape). **`log_listen_events` is exempt from audit and from response holding** (`Meta` flag `NoAudit`, allowed only for scope `rate` ops that write an immutable event log and carry `Idempotency` none; `validate()` rejects it elsewhere): it is high frequency during playback and `listen_events` rows are already an immutable, attributed log, so an audit row adds nothing. Both ops get a dedicated rate class `listen` (new `ratelimit.Class`, per principal key, default burst 60 and 1 req/s sustained for Editor and Listener principals; the Owner's `human` class is unchanged) so a Listener loop cannot share or exhaust the Owner-class budget; over limit answers 429 with `Retry-After`. Tests: a Listener or Owner `search_tracks` GET writes no audit row and its response is not held; `rate_track` writes exactly one row with no verdict text; a Listener session of 200 `log_listen_events` writes zero audit rows and a bounded number of `listen_events` rows (batch size cap), and exceeding the `listen` limit answers 429. Done in F1b with the retag; the gate edit is F2.
5. `/openapi.json` and docs: lane `u1-core` checks whether they are served to non-owners; if so, owner-only ops are omitted for Listeners (open question Q13).

### C305. Per-op permission table

Notation: **Y** allowed; **own** own rows only; **R** allowed with redaction (C308); **N** refused (403 `OWNER_ONLY`/`FORBIDDEN_SCOPE`, or
404 for hidden data); **OO** `OwnerOnly`: refused for a human Editor or Listener only, non-human delegates pass by scope as today (D-U1-6). "Scope" shows the scope after U1 and must be read from `openapi.json` `x-mm` by the lane (this table was corrected against it; the lane re-checks).
**The Editor and Listener columns are roles, not kinds:** they apply equally to a device or Subsonic client of an Editor or Listener (D-U1-14); the golden matrix has `device-editor` and `device-listener` rows. Every op in the registry is classified in
`internal/api/testdata/role_matrix.golden`; an op missing from it fails the test (C314), which is how this table stays complete. The table
lists op families; the lane that owns a family expands it to op ids from `openapi.json`.

**Library browse**

| Ops | Scope | Owner | Editor | Listener |
|---|---|---|---|---|
| `search_tracks`, `get_track`, `get_track_peaks`, `get_track_analysis`, `get_track_spectrogram`, `get_media_links`, `get_download_link`, `get_clip`, `compare_tracks`, `get_ladder` (by `root_track_id`), `get_library_facets` | listen (`get_ladder` is OO, see Judgement) | Y | R (no cost) | R, discarded hidden (404 by id, also for `compare_tracks` a/b), counts over visible only, search and filters per C308.3b, media per D-U1-17 |
| `list_songs`, `get_song`, `get_song_atlas`, `search_sounds`, `get_sound_taxonomy`, `list_tags` | listen | Y | Y | R (no Muse, no cost, no drafts or sessions) |
| `get_lineage` | listen | Y | R (no cost) | R (visible nodes; discarded parent shown as an untitled stub; no cost or recipe internals) |
| `list_playlists`, `get_playlist`, `list_crates`, `get_crate` | listen | Y | Y | R (visible members; crate `filter_json` hidden) |
| `list_records`, `get_record`, `get_record_changes`, `get_tape`, `suggest_sequence` | listen | Y | Y | R (published, non-hidden; no liner Muse text, change log or proposal notes) |
| `get_offline_set` | read | Y | N (Owner-only singleton, D-U1-10; also N for a device of an Editor or Listener, Q22) | N (same) |
| `get_adherence`, `get_system_info` | read | Y | N for `get_system_info`; Y for `get_adherence` | N |
| `get_track_context` and its MCP resource | read | Y | R (no cost, no Muse text) | N |
| `get_stats` | read | Y | R (no generation, backup, storage, import blocks) | N |
| `whoami`, `ping` | listen | Y | Y | Y |

**Search and filter surfaces for a Listener** (C308.3b): `q` runs only over title and lyrics; the hidden-data filters (`has_open_comments`, `session_id`, `has_open_proposals`, `queued`, `draft_id`, `angle`, `origin`, `engine`, `root_track_id`, `lyrics_of`, the Muse filter, `color_by=muse`) are rejected 422 `FILTER_NOT_ALLOWED`; Atlas `q` follows the same rule.

**Judgement**

| Ops | Scope | Owner | Editor | Listener |
|---|---|---|---|---|
| `rate_track` | rate | Y (own; sees all) | Y own | Y own (404 on discarded) |
| `log_listen_events`, `put_device_profile` | rate | Y | Y own | Y own |
| `update_track` | write | Y | title and `to_relisten`; `final` and `discarded` OO (Q4) | N |
| `set_tags` | write | Y | Y | N |
| `add_comment`, `update_comment`, `list_comments` | write / read | Y (any) | Y (edit own) | N (private notes) |
| `create_voice_note`, `get_listen_heat`, duel ops (`create_duel`, `submit_duel`, `next_duel`, `suggest_duel_pairs`, `get_ladder`, `list_duels`, `get_duel`), `submit_callback_verdict` | write / read | OO (human Owner Y; agents unchanged: critic duel and callback paths; the Owner's own devices unchanged, device voice notes; Editor and Listener devices N, D-U1-10, C312.6) | N | N |
| `list_callbacks`, `get_callback`, `list_pick_events` | read | Y | Y | N |
| `pin_pick`, `dismiss_pick_proposal`, `decide_taxonomy_proposal` | write | OO | N | N |
| `propose_pick` | write | agent-only as today (the service refuses every human; not OO) | N | N |
| `propose_taxonomy`, `list_taxonomy_proposals`, `map_sound_term` | write / read | Y | propose and read; `map_sound_term` N | N |
| `list_next_actions` | read | Y | R (no spend or callback items) | N |
| `list_patches`, `get_patch`, `propose_patch` | read / write | Y | Y | N |

**Authoring**

| Ops | Scope | Owner | Editor | Listener |
|---|---|---|---|---|
| `create_draft`, `update_draft`, `upload_batch_jsonl`, `lint_recipe`, `list_drafts`, `get_draft` | write / read | Y | Y (reference metadata as an agent sees it) | N |
| `create_playlist`, `update_playlist`, `create_crate`, `update_crate`, `edit_crate_items`, `export_playlist_zip` | write | Y | Y | N |
| `create_record`, `update_record`, `edit_record_slots`, `propose_record`, `export_record_tape`, `update_song`, `list_sessions`, `update_session` | write / read | Y | Y (a published record is edited by Owner only) | N |
| `publish_record` | write | OO | N | N |
| `list_muses`, `get_muse`, `get_muse_board` | read | Y | Y read (Q6) | N |
| `create_muse`, `update_muse`, `assign_muse_songs` | write | Y | N | N |
| `export_jsonl`, `list_export_sources`, `get_export` | write / read | Y | own (`get_export` checks `exports.principal_id`) | N |
| `get_curation_settings`, `get_songwriting_settings`, `get_prompt` | read | Y | Y read | N |
| `set_curation_settings`, `update_songwriting_settings` | admin | OO | N | N |

**Generation and spend**

| Ops | Scope | Owner | Editor | Listener |
|---|---|---|---|---|
| `estimate_generation`, `estimate_variant`, `list_engines` | generate / read | Y | Y (own estimate in full; no price, pools or month budget) | N |
| `create_generation`, `create_variant` | generate | Y | Y, always `awaiting_confirmation` (the web app only: an Editor's paired device holds no `generate` scope, C312.2, so from it these ops answer 403 `FORBIDDEN_SCOPE`; matrix row `device-editor` N) | N |
| `estimate_transform` | write | Y | Y | N |
| `create_transform` | write (cost free, never spends) | Y | Y, no approval (CPU quota only, own quota pool, D-U1-18) | N |
| `get_transform`, `list_transforms`, `wait_for_transform`, `get_transform_capabilities` | read | Y | Y (no cost fields; list shows own plus others without creator) | N |
| `cancel_transform` | write | Y (any) | own only | N |
| `create_generation_human` | generate | OO | N | N |
| `request_approval` | generate | n/a | Y (auto-sent on batch creation) | N |
| `get_approval_status` | read | Y | own | N |
| `approve_batch`, `reject_batch` | generate | OO | N | N |
| `cancel_batch`, `cancel_job` | write | Y (any) | own batches (Editor withdraws a pending one) | N |
| `list_batches`, `get_batch`, `list_jobs`, `get_job`, `wait_for_job` | read | Y | own with cost; others without cost or creator | N |
| `get_budget` | read | Y | R (`approval_required`, `generation_enabled`, `disabled_reason` only) | N |
| `get_spend`, `list_ledger`, `set_spend_caps`, `update_provider_state` | read / admin | OO | N | N |
| `get_provider_status` | read | Y | R (`generation_enabled`, `disabled_reason` only) | N |

**Administration** (OO: refused for an Editor or Listener, human or device; scope as in the registry, several are `read`): `get_audit`, `list_tokens`, `create_token`, `revoke_token`, `list_backups`, `create_backup`,
`list_connector_grants`, `set_connector_grant`, `list_import_runs`, `get_import_run`, `get_device_probe`,
`list_device_profiles`. Admin-scope ops on `/api` keep working for Owner-minted admin tokens (non-human delegates pass `OwnerOnly`, D-U1-6). Not OO because they are a principal's own self-service: `revoke_own_device` (`mcp: exclude:device-self-service`) and `revoke_own_token`.
`put_device_profile` (rate): an Editor or Listener (human or device) may write only a row whose `created_by` is their own principal key or the key of a device they own (an existing row of another principal answers 404); the Owner and delegates keep today's behaviour. No `list_users` op in v1 (D-U1-19).

**Devices and Subsonic clients** (self-service, D-U1-14; replaces the earlier OO rule). Not OO. All `HumanOnly` (a paired device or agent never pairs or creates clients), scope `listen`, behind the same step-up as today. Final state after lane D1; until D1 merges, F1b keeps these ops `OwnerOnly`.

| Ops | Owner | Editor | Listener |
|---|---|---|---|
| `create_pairing_code`, `create_subsonic_client` | Y (owner key `owner`) | Y, bound to the caller | Y, bound to the caller |
| `list_devices`, `list_subsonic_clients` | Y, all users (owner column) | own only | own only |
| `revoke_device`, `revoke_subsonic_client` | Y, any (immediate) | own only (a foreign id answers 404) | own only (a foreign id answers 404) |
| `revoke_own_device` (device self-service, unchanged) | Y | Y | Y |

Limit: at most 5 active devices and 5 active Subsonic clients per Editor or Listener, 422 `DEVICE_LIMIT` (the Owner is unlimited; default, Q21). A device or client acts as its user's role: a Listener's phone is refused on every OO op, on `get_offline_set` and on Subsonic bookmarks, gets the Listener view and the Listener renditions, and its ratings go to its user's row. List and revoke ops are `HumanOnly`, so a phone cannot enumerate devices. Tokens and connector grants stay OO.

**MCP generic**: `describe_operation` and `call_operation` are filtered by `Permitted` (agent principals only, C304). **No new op for ratings:** the per-user shape is folded into `get_track.rating` and the existing `rate_track` (C306.4), so the op set and parity do not change.

**Rule for a new op.** It fails `TestRoleMatrixComplete` until classified. The default for a new read op is `read` (Owner, Editor, agents); `listen`
is opt-in; spend, approval, publish and admin ops must be `OwnerOnly` (enforced by `validate()`).

### C306. Ratings per user: migration `<next>_ratings_per_user.sql` (currently 0017) and API shape

1. SQLite cannot alter a primary key: rebuild. Create `ratings_new` (WITHOUT ROWID) with the same columns plus `rater TEXT NOT NULL CHECK (length(rater) > 0)`, PK
   `(track_id, source, rater)`, the same `source`/`principal_kind` CHECK, and the pairing CHECKs `((source='agent') = (rater='critic'))` (so `human` rows can never be `critic` and agent rows never a user). Copy: `rater = CASE WHEN source='human' THEN 'owner' ELSE 'critic' END`. Drop, rename.
2. Recreate in the same migration: `ratings_no_delete`; `ratings_frozen` on `track_id, source, rater, created_at`; index `ratings_source_stars`; new
   index `(track_id, rater)`; the provenance triggers `pd_ratings_ai` and `pd_ratings_au` with `WHEN NEW.source='human' AND NEW.rater='owner'` (so a Listener rating
   storm never churns the provenance dirty queue; `pd_verdicts_ai` is unchanged, duels stay Owner-only).
3. Views: `owner_ratings` (`source='human' AND rater='owner'`) and `critic_ratings`. Both static (the Owner key is the constant `owner`, so no config is read by the migration).
4. `TrackRating` becomes `mine` (the viewer's own row), `human` (deprecated alias of `mine` for one release so the Owner's wire value is unchanged), `others []{user,
   stars, verdict, updated_at}` (Owner only), `average {mean, n}` (Owner and Editor; absent for a Listener and a Listener's device, D-U1-16, confirmed 2026-11-18), `critic` (Owner and Editor). An Editor sees `mine` and `average` and no `others`; the Owner sees every user's row plus the average.
5. `Rate` upserts `ON CONFLICT(track_id, source, rater)`; the rater comes from `RaterKey(p)`. **`rate_track` and `log_listen_events` call `library.Visible` before any write on the Listener path** (the archtest `TestTrackHandlersCallVisible` covers write handlers too, C308.2): a Listener rating or logging a play of a discarded take gets 404 and writes nothing. The verdict is capped at 280 characters, stored as text, and shown only per C306.4. Volume is bounded by the `listen` rate class and the audit rules in C304.4.
6. Rollback: non-additive. Take a backup first (`create_backup`); once another user has rated, the old shape cannot hold the data. Rehearse on a `VACUUM INTO` copy
   of the real database (task X2). Orphan raters are never fatal (C301.10): rows of a removed user stay inert, the Owner sees them as "former user <name>" and the average includes them.
7. Listening: `listen_events.principal_id` already exists and rows are immutable; no schema change. Add index `(principal_id, track_id)`. Readers that today filter `principal_kind IN ('human','device')` (named by U1-R2:
   `library/judgments.go` plays, completion and `last_played`; listening session summary; heat and hotspots; `unheard`; next-action inputs) count Owner-side events only: the Owner's principal key (bound as a query parameter, resolved from config at startup, so SQL never hard-codes a name) or a device whose `devices.owner_key = 'owner'` (join on the device id, rows are never deleted; a bare `device:*` match would let a Listener's phone steer Owner heat; Subsonic client plays resolve the same way, the lane re-reads the principal id used for scrobbles); viewer stats filter by the viewer's own principal key or the keys of devices they own. A rename of the Owner's SSO username splits listening history and needs a restart anyway; the runbook says so. Test: a Listener play, from the browser or from a Listener's paired device or Subsonic client, changes neither heat nor `unheard` for the Owner.

### C307. Owner-derived state: rewriting the `source='human'` consumers

About 25 `FROM/JOIN ratings` lines across about 20 packages join ratings on `source='human'` or read the `ratings` table directly and assume at most one row per track (judgments `HumanStars`, `library/judgments.go`, `library/stats.go`, picks recompute, song view, songs filter,
song state, playlists, lineage, record view, provenance verify, Atlas scope, next-action rules, duel suggest, Subsonic catalog). With per-rater rows
a `LEFT JOIN` fans out and `COUNT`/`SUM` inflate silently, with no compile error. `source = 'human'` also appears legitimately in `duel_verdicts`, `callback_verdicts` and `track_tags` (about 45 hits in 28 files); those stay Owner-only and are out of scope for the lint.

1. **Curation signal** (picks, song state, Atlas, next actions, provenance, ladder, suggest, stats histograms) reads `owner_ratings`. Listener and Editor ratings never steer picks.
2. **Viewer-facing display** (stars shown on takes, playlists and records, `min_stars` and star sort, Subsonic stars) joins on `rater = RaterKey(viewer)`.
3. **Lint test** `TestNoRawHumanRatingJoin` targets the table, not the literal: any SQL containing `ratings` after `FROM`, `JOIN`, `INTO` or `UPDATE` is banned outside `internal/judgments`, `internal/library/judgments.go`, the view definitions and the migration files, so readers must go through `owner_ratings` or the viewer join. Verdict tables are out of scope.
4. Duel and callback unique indexes (`duel_verdicts_one_human`, `callback_verdicts_one_human`) are untouched; ladder and recompute queries are pinned explicitly to Owner verdicts so opening
   them later cannot contaminate scores.
5. **File ownership.** U1-R2 lands before U1-V1 starts on shared files. R2 enumerates its consumers by file with `rg` at lane start (recorded in the revision log) and each file has one owner at a time; V1 and V2 rebase onto R2.

### C308. Listener and Editor visibility (`internal/view`, `internal/library`, media)

1. `view.For(p) Viewer{Role, User, HideDiscarded, SeeCost, SeeMuse, SeePrivate}`. Per-type shapers (`view.Track`, `Song`, `Lineage`, `Playlist`, `Record`, `Stats`, `Budget`, …) are
   the last step of each handler (the same place as `maskTrack`), so REST, MCP, `call_operation` and the SPA share one shaping. Redacted fields are pointers with `omitempty`: absent, never zeroed;
   money stays int64 micro-USD and a missing cost is absent, not 0. One schema serves all roles. `view.For` keys on `p.Role`, never on `Kind`, so a Listener's device or Subsonic client gets the Listener view.
2. **Discarded takes.** Today discarded is hidden only as a default search filter. Add `library.Visible(ctx, viewer, trackID)` and a SQL fragment `t.status <> 'discarded'`, forced for Listeners. By-id reads return
   404 `NOT_FOUND`, never 403. Applied in: search, `get_track`, lineage and siblings, song takes, playlist and crate members, record slots (shown empty), Atlas, `get_library_facets` and `list_songs` counts
   (so a status count cannot reveal them), `library.Resolve`, every media signing site (`get_media_links`, download, clip, spectrogram, duel view, tape, export), Subsonic folder 3 and by-id stream.
   Archtest `TestTrackHandlersCallVisible`: every handler that reads a track id from the path, a query parameter or the body (including `compare_tracks` `a`/`b`, Atlas `track_id` and `near_song_id`, `get_ladder`, `suggest_sequence`, `get_record`, `get_playlist` and `get_crate` members) calls `Visible` or is on an allow-list with a reason.
3. **Fields hidden from a Listener** (everything below is absent, per D-U1-12):
   - Money: every `*_micro_usd`, `*_usd`, `display_*`: `search_tracks` `cost_display_*`, `get_track.generation.cost`, `get_song.takes[].cost_display_usd`, `get_lineage.nodes[].cost_*`,
     transform responses, `get_stats.generation`. A repo scan test lists the money fields in `internal/api/types_*.go`.
   - Recipe internals: `recipe.cot`, `seed`, `hash`, `request_id`, raw `style`; `generation.*` (job, batch, operator, rationale, env); `import.*`; `status_set_by`; `master_sha256` and audio md5; `playlists`; `listening` totals; `rating.critic`.
   - Muse text **and structure**: Muse text, `song.muse_id`, `TrackSong.muse_id`, the `list_songs` Muse filter, `get_song.muse`, `get_song_atlas.muses` and `points[].mi`, `color_by=muse` (rejected), smart-crate `filter_json`, session names.
     The `museTextOps` allow-list of the L1 leak test must be empty for Listener.
   - Private notes: comments, voice notes, callbacks, draft reference metadata, `proposal_note`, `proposed_by`, `queued_jobs`, `get_song.draft`, `sessions`, `proposals`, `events`; pick evidence and events.
   - Other users' ratings and identities (C309).
   - **Visible**: title, status, sections, lyrics (Subsonic `getLyrics` already serves them), derived `style_terms`, declared BPM, analysis, peaks, spectrogram.
   - **3b. Query-side oracles.** FTS indexes `title, request_id, style, lyrics`, and several filters are boolean oracles on hidden data. For a Listener: `q` is restricted to the visible columns (FTS column filter `{title lyrics}:`, same for Atlas `q`); `has_open_comments`, `session_id`, `has_open_proposals`, `queued`, `draft_id`, `angle`, `origin`, `engine`, `root_track_id`, `lyrics_of` and the Muse filters are rejected 422 `FILTER_NOT_ALLOWED`. The sweep sends each filter and `q` with a canary and asserts the result set never changes or the call is refused.
4. **Editor** sees the same shapes without money in library views and without Muse text in `get_track_context` and `get_stats` blocks as listed in C305.
5. **Signed media links** (`/media/{sha}.{ext}?exp&sig`, unauthenticated, bound to sha, ext, expiry): never signed for a Listener on a discarded track; `TTLFor` gives a Listener, and a Listener's paired device or Subsonic client, 15 min (D-U1-13); the Owner, Editors and their devices keep 12 h. Per D-U1-17 a Listener's links reference the two stripped renditions only (C308.7); the same holds for clips, tapes and any export reachable by a Listener (including via a Listener's device): they source from `mp3_320_clean` or `flac_clean`, never a master, or are refused; the master and its sha256 are never signed, shown or reachable for a Listener.
6. **Subsonic** (a client carries its owner's role, D-U1-14): a Listener's client does not see folder 3, gets 404 on discarded ids, and `star` is refused (it changes status; refused for Editor clients too in v1); `setRating` writes the owner's per-user rating; bookmarks are refused for non-Owner clients (D-U1-10). Stream and download for a Listener client resolve to `mp3_320_clean` or `flac_clean` only (default or `format=mp3` gives MP3, `raw` or `flac` gives the lossless clean FLAC, never the master); `getSong` reports the rendition's suffix, size and content type and exposes no master path or checksum. Subsonic responses for a Listener client (`getSong`, `getAlbum`, lists, search) never emit `averageRating`, only `userRating` from the owner's own row (D-U1-16); Editor clients may carry the average. The role comes from the client row's `owner_key` (C312), so this lands with lane D1 (rendition lookup with V3).
7. **Listener renditions** (D-U1-17; Q18 resolved).
   - **Key.** Two rows per track in the `renditions` table, keyed `(track_id, kind)`: `mp3_320_clean` and `flac_clean`. `kind` is Go-validated (Draft 2 found no SQL CHECK, so no migration); lane V3 re-verifies that.
   - **Build of `flac_clean`.** From the frozen master by stream copy into a new file (`ffmpeg -i master -map 0:a -map_metadata -1 -c:a copy -fflags +bitexact`, then the single allowed tag `TITLE`). The audio frames are bit-identical to the master; the master is only read (rule 7: frozen, mode 0444, never rewritten). Allow-list, not deny-list: a Vorbis comment other than `TITLE`, any `MM_*` key, and any PICTURE, APPLICATION or CUESHEET block fail the build. `mp3_320_clean` keeps its own build (`-map_metadata -1`, `TITLE` only) under the same allow-list.
   - **On demand and cache.** Built on the first Listener request, singleflight per `(track, kind)`, written to a temp file then renamed, mode 0444, recorded with its own sha256 and size; later requests hit the row. Nothing is prebuilt, so a library without Listeners pays nothing; a stream copy is I/O only, so `flac_clean` is cheaper than the MP3 encode (this settles Q18's cost worry). A title change (an Editor may edit titles) rebuilds both renditions on the next request; the lane chooses between recording the embedded title on the row and deriving the path from a title hash.
   - **Hashes.** The rendition's sha256 differs from the master's (the metadata differ) and is the only hash a Listener sees, inside the URL. The FLAC STREAMINFO MD5 of the decoded audio equals the master's for any lossless copy; that is accepted, is not exposed as an API field, and the tests state it.
8. **Build cap, verify, backup** (lane V3).
   - **Cap.** Builds run under one global semaphore (default 2) with a bounded queue; beyond it a request answers 503 `RENDITION_BUSY` with `Retry-After`, so one Listener cannot saturate the CPU. Both kinds share it.
   - **`library verify`** gains a rendition check for both kinds: the file exists and its sha256 equals the row; the tags, read by an in-process stdlib parser of FLAC metadata blocks and ID3 (no `metaflac` dependency at runtime), are within the allow-list and carry no `MM_*` key; for `flac_clean` the STREAMINFO MD5 equals the master's. Findings: a missing file is a warning (row dropped, rebuilt on demand); a tag leak or hash mismatch is an error (file and row removed, never served until rebuilt). Verify never writes a master.
   - **Backup.** `*_clean` renditions are derived and rebuildable from frozen masters, so they are not required for restore. If the backup job lists directories explicitly the lane leaves them out, otherwise including them is harmless; either way a restore test with an empty rendition directory passes, a first Listener request rebuilds, and `library verify` is clean afterwards.
   - **Tools.** ffmpeg with libmp3lame is already asserted by the Dockerfile; the FLAC copy needs only the native flac muxer.

### C309. Identity masking

`RefFor`/`KeyFor` collapse non-operator views of the operator to `human`, which assumes one human. New rule (D-U1-15): Owner sees usernames; Editor sees usernames; Listener sees only their own name and any other
user as `user`. Applies to `rating.others[].by`, comment authors, `created_by`, `updated_by`, `decided_by`, `armed_changed_by`, audit refs, and batch creators shown to an Editor. SSO usernames never reach a Listener.

### C310. Editor generation (`internal/spend`, `internal/generation`, `internal/jobs`)

Deliberately minimal (D-U1-11, D-U1-19): no new cap schema.

1. **Via.** Only the Owner may use `ViaHumanCreate`; for an Editor `estimateVia` and `create.go` force `ViaAPI`. `create_generation_human` is `OwnerOnly`. The SPA Generate page calls `create_generation` for Editors.
2. **Approval.** `NeedsApproval` gets a role branch before the agent branch: role editor → `Needed=true, Reason="editor_batch"`, unconditionally; `auto_approve_takes` and `auto_approve_micro_usd` are ignored. Auto-approve stays agent-only.
3. **Deciders.** `approve_batch` and `reject_batch` are `OwnerOnly` and the service guard requires `IsOwnerSide()` (C302.6). The Editor's `cancel_batch` withdraws their own awaiting batch.
4. **Caps.** The existing global month, batch and per-principal caps apply. The existing per-batch reservation holds the ceiling until approval, rejection, withdrawal or the 24 h TTL; the Owner can reject. Whether Editors need their own pot or a pending limit is Q1.
5. **Money exposure.** An Editor sees their own estimate in full (expected, max, ceiling, per-take max), approval state, job states and settled cost of their own batches, `generation_enabled` and `disabled_reason`. An Editor never sees month cap or usage, others' batch costs, the ledger, balance, price per hour, GPU pools, RTF, token or connector caps, or auto-approve limits. `CAP_EXCEEDED` details are reduced for an Editor to a code and message.
6. **Cancel.** `mayCancel` (batches and jobs): Owner any; Editor own batches; agents unchanged. `cancel_transform`: Owner any, Editor own, agents own (today's rule extended).
7. **Transforms and backlog.** Transforms never spend and have no approval path (D-U1-18). Editors use their own quota pool (`QuotaKind`, `principalPool`). `gateBacklog` applies to Editors as well as agents.

### C311. Audit

Audit rows already carry `principal_kind`, `principal_id`, `principal_name`; `Key()` is per user, so per-user attribution needs no change and there is no new column in v1 (`principal_role` deferred, D-U1-19). `OWNER_ONLY` and
`FORBIDDEN_SCOPE` refusals on human principals are logged as denials (no request bodies, per CLAUDE.md). The gate rule is C304.4: `rate_track` is audited with refs only (no verdict text), `log_listen_events` is not audited and its response not held, and both run under the `listen` rate class. `get_audit` is OO; its ref masking follows C309. A device principal's row names the device; the Owner maps it to a user through the device list (owner column); no audit column is added in v1.

### C312. Devices, tokens, connectors, Subsonic

Revised 2026-11-18 (D-U1-14, Q3 and Q16 resolved). Tokens and connector grants are minted by the Owner only and stay Owner delegates; agent caps, auto-approve and the `critic` source are unchanged; connector verification requires role owner (C301.4). Editors and Listeners cannot mint tokens or reach MCP (C304.1; Editor-minted agent tokens deferred, D-U1-19).

Devices and Subsonic clients are per user:

1. **Pairing.** After SSO login the user starts pairing themselves (`create_pairing_code`, `HumanOnly`, behind the same step-up as today). The pairing is under `/human/`, so the edge's two-factor rule applies to every user at that moment: a Listener who pairs a phone is prompted for two factors then (C314.8). The code row stores the creator's owner key; redeeming it (unauthenticated by design) creates the device with that key. The owner key is never read from a request field.
2. **Identity.** The device principal's `Role` and `User` come from its owner key, resolved against the role map on every request (C301.3). Effective scopes are `effective(role, stored)` (C301.3, defined through the `Has` implication): the Owner's device `read,write` as today, an Editor's `read,write` (never `generate` or `admin`), a Listener's `listen,rate`. `Permitted`, `view.For`, `TTLFor` and `RaterKey` read the role and owner key, never the kind. Ratings, plays and `device_profiles` rows attribute to the owner key.
3. **Subsonic.** A client row carries `owner_key`, authenticates with its own credential and acts as that user with that role (C308.6).
4. **Owner oversight.** The Owner lists every device and client with the owner key and can revoke any of them at once (status flag). Editors and Listeners list and revoke their own; a foreign id answers 404. Limit 5 devices and 5 clients per non-owner user (Q21).
5. **Revocation by demotion or removal.** The role map is read at startup (D-U1-4): a demoted user's devices are clamped to the new role and a removed user's devices answer 401, on the next restart; no live change is claimed. Rows of a removed user stay inert, their ratings appear as "former user".
6. **Still Owner-only** (D-U1-10): the offline set, bookmarks, duels, callbacks, voice notes, picks. A device of an Editor or Listener reaches none of them.

### C313. Web (`web/`)

1. One `me` rune module loaded once and refreshed on `mm:stepup-done`, exposing `role`, `user`, `can(opId)`; it replaces the seven independent `/api/me` callers.
2. String checks on `kind` and `scopes` (generate intent, record view, proposal view, job state, App, More) become `can('approve_batch')`, `can('publish_record')`, and so on.
3. Nav links, tabs and routes are filtered by `can(<op of the route's x-mm.ui>)`; a page whose primary op is refused renders "no access". UI hiding is cosmetic; the server is the authority. A Listener shell: Library, Songs, Playlists, Crates, Records, Atlas, now-playing, own rating and My devices (pair a phone or Subsonic client, list and revoke own, behind the step-up gate).
   An Editor shell (with My devices too) adds drafts, crates and records editing, Generate-as-request and own pending state, and shows no Spend, Caps, Provider, Approve or Publish control. No approvals badge for non-owners (polling for Owner only).
4. `SecureGate` stops probing the human-only devices endpoint and uses `can(...)`. The devices page lists every user's devices with an owner column for the Owner and only their own for an Editor or Listener.
5. Rating UI: "your rating" everywhere; the Owner additionally sees each user's rating and the average; an Editor sees the average.
6. The fetch ban outside `web/src/api/` is unchanged. Types regenerate with `make openapi`.
7. **Browser state is per origin, not per user.** `localStorage` holds the device id and quality (`device.svelte.ts`), the player queue `mm:player:v1` (`playerStore.ts`) and inbox seen/heard markers (`inbox.svelte.ts`); the service worker keeps the media cache `mm-media-v1` and the IndexedDB `mm-offline` (`web/src/api/sw/store.ts`, the Owner's offline set). Two Authelia users on one browser would share them: a Listener's `put_device_profile` would hit C305's "row of another principal answers 404", and a restored queue or cached media of the previous user could contain takes that are discarded or hidden for the next. Rule: the `me` store records the last username under one key (`mm:me:user`); on boot, before the first render, if `/api/me` returns a different `user` than the stored one (or none is stored and any `mm:*` key exists) it **clears every `mm:*` localStorage key, the `mm-media-v1` cache and the `mm-offline` database**, then stores the new username. Per-user state written afterwards is namespaced `mm:<user>:...` (queue, inbox markers, device id and quality). Belt and braces: `put_device_profile` answering 404 for an unknown own-id makes the client mint a new device id once and retry. On the SW, the offline precache is enabled only when `can('get_offline_set')` (Owner). Playwright spec `user-switch` (C314.7) drives two user projects through one browser context.

### C314. Tooling, harness, tests

Heavy suites run in GitHub CI only (operator rule); agents run focused package tests locally.

1. **Unit.** `auth`: role per user, unknown user → 401, header rules unchanged, lowercase folding and collisions, config validation (every D-U1-3 refusal), orphan raters warn but do not stop startup (`TestOrphanRaterDoesNotBlockStartup`), role map fixed at startup (no "immediate demotion" claim), scope implication, `ScopesFor`, device and Subsonic principals (role and scopes from the owner key, clamp never exceeds the owner's role, removed owner key gives 401, role fixed at startup). `cmd`: `config check`. `api`: `Permitted` rule order (OwnerOnly skips non-human principals),
   `TestEveryOpHasValidMeta` (owner-only rules incl. the sensitive read-op list), `TestRoleMatrixComplete` with `testdata/role_matrix.golden` (every op × owner, editor, listener, agent-read, agent-write, agent-admin, device (the Owner's), device-editor, device-listener; `-update` flag; the agent and device rows equal the pre-U1 behaviour; `ops_m2a_test.go` `rate_track` row changes to scope `rate` in the same commit). `gate`: browse GETs write no audit row, `rate_track` does. `spend`: Editor branch never auto-approves even under the auto-approve limits. `transform`: Editor cancel own only, own pool, backlog gate for Editors.
   `judgments`: two users rate one track independently; critic unchanged. `view`: golden redaction per role per type. Slow tests (full matrix, sweep, migration copy) skip under `testing.Short`, use one rig per role and a fixed seeded fixture, so `make check` keeps its budget; they run in CI.
2. **Arch/lint.** `role_predicates_u1_test.go` (C303, including `KindAgent` kind checks), `TestTrackHandlersCallVisible`, `TestNoRawHumanRatingJoin` (C307.3), a money-field scan.
3. **Migration.** `TestMigration_U1`: fixture DB at the previous version with human, device-origin and agent rows; assert counts, every human row `rater='owner'`, agent `critic`, triggers present (`ratings_no_delete`, frozen, `pd_*` with the owner-only WHEN), the pairing CHECKs reject (`human`,`critic`) and (`agent`,`owner`), `owner_ratings` equals the old human rows, a second rater's insert does not enqueue provenance, `user_version` advances. `TestMigration_U1Devices`: existing devices, Subsonic clients and pairing codes get `owner_key='owner'`, the column is NOT NULL, existing device tokens still authenticate as the Owner, `user_version` advances.
4. **Parity.** `servertest.Rig.User(tb, name, role)` (`Human()` stays the Owner), plus `Rig.DeviceFor(tb, name)` and `Rig.SubsonicClientFor(tb, name)`, which pair a device or client as that user through the real pairing path. `mcpgen` parity keeps its principal dimension for agents; Editor and Listener, and their devices and Subsonic clients, get 401 on `/mcp` (`TestEditorListenerMcp401`) and a pre-U1 golden of the Owner agent's tool list stays identical. `whoami.allowed_ops` equals the ops for which `Authorize` passes, per role. Stdio parity.
5. **Acceptance and leak sweep.** Extend the human-only principals matrix with Editor and Listener rows: Editor `create_generation_human`, `approve_batch`, `set_spend_caps`, `publish_record` all 403; Editor `create_generation` ends `awaiting_confirmation` with `editor_batch`.
   Listener sweep modelled on the L1/L2/L3 leak tests and the M3 canary test: canaries are a cost string, Muse text, a discarded take, a private comment, another user's rating, an SSO username, a token prefix, a raw style and a request id. For every GET op over REST (and the SPA path) assert no money keys, no provider or `error_message`,
   no audit principals or token prefixes, no canary; every filter parameter and `q` sent with a canary changes nothing (C308.3b); discarded takes absent via media links, lineage, playlists, Atlas, `compare_tracks`, `status=discarded` search, record slots and Subsonic; every Listener-reachable audio URL (REST links, downloads, Subsonic stream and download, both formats), parsed, is one of the two stripped renditions: no `MM_*` key, no tag other than `TITLE`, no picture or application block, and a sha256 that is never the master's (`TestListenerAudioStripped`; the URL enumeration includes `get_clip` and `get_tape` output, and `export_playlist_zip` and `export_record_tape` are refused or rendition-sourced for a Listener's principals). Ownership tests: `cancel_batch`, `get_export`, `put_device_profile`. Owner derived state (picks, song state, Atlas, heat, unheard) is unchanged by a Listener or Editor rating or play. **Agent rating sweep:** an agent principal calls `get_track` (and `rate_track`, `call_operation`) on a track carrying a Listener rating with a canary verdict and a canary username: the result has `human`, `critic`, `mine` (= critic) and `average`, no `others`, and neither canary appears in the response, in the held audit response row or in the MCP transcript fixture. The filter sweep is the reflection-generated one of C308.3b (every `SearchParams`, `SongFilterIn` and `atlasIn` field decided).
   **Listener audio (D-U1-17, C308.7, C308.8):** `TestListenerFlacClean` builds `flac_clean` from a fixture master carrying `MM_SEED`, `MM_REQUEST_ID`, `MM_RECIPE_ID`, `MM_TAKE_ID`, a picture and an extra comment, and asserts the output's metadata blocks are STREAMINFO, optional SEEKTABLE and PADDING, and a VORBIS_COMMENT with only `TITLE`; the decoded audio equals the master's (`flac -t` and decoded MD5 when `MM_REQUIRE_MEDIA_TOOLS=1`); the file hash differs from the master's; the master file is byte-identical and still 0444 afterwards. Cache tests: a second request does not rebuild (build counter), concurrent requests build once (singleflight), the cap answers 503 `RENDITION_BUSY` past its limit, a title change rebuilds. A Listener asking for the master by any route (`get_media_links`, `get_download_link`, `/media/<master sha>`, Subsonic `raw`) gets a rendition or 404, never the master; the Listener `get_media_links` shape has no `sha256` field. Verify tests: a corrupted file, a missing file and an injected `MM_*` tag are each found and handled per C308.8; a restore with an empty rendition directory rebuilds on demand.
   **Device isolation (D-U1-14):** the Listener sweep runs again with a Listener's paired device and Subsonic client as principal and returns identical results. A Listener's device cannot reach any OO op (403 `OWNER_ONLY`), `get_offline_set`, voice notes, bookmarks or `/mcp`; cannot list or revoke another user's devices (404); and its ratings land on its user's row, never on `owner` or another user (the Owner's `owner_ratings` and derived state are unchanged). An Editor's device cannot approve, publish or change caps and never sees `others`. The Owner's device keeps today's behaviour (golden row). Pairing: a code created by a Listener yields a device owned by that Listener even when redeemed by an unauthenticated client; a request body cannot set the owner key; the 6th active device answers `DEVICE_LIMIT`; the Owner lists and revokes every device; a restart with the user demoted or removed clamps or refuses the device (no live change is asserted).
   **Average rating (D-U1-16):** a Listener, and a Listener's device, gets no `average` and no `others` key; an Editor gets `mine` and `average` without `others`; the Owner gets `others` and `average`. Subsonic: a Listener's client responses (`getSong`, `getAlbum`) contain no `averageRating` and only `userRating` from its own row.
6. **Web unit (bun).** `me` store, `genIntent`, nav and badge tests, approvals, `e2e-coverage`, record, proposal and job-state views, rating shape.
7. **E2E (Playwright, CI).** `MM_USERS` with alice, bob, carol replaces `MM_TRUSTED_USERS` in `web/e2e/server.ts` and the Go test helpers; one Playwright project per user with `extraHTTPHeaders` replaces the single `HEADERS` constant in `web/e2e/env.ts`. Specs: `listener-shell` (no Make or Secure rows, no approvals badge, `/human/*` refused, rates own, discarded take absent), `editor-shell` (no Approve, Reject, Publish or caps; request lands awaiting approval; the Owner approval is driven through the API in serial mode, not a second browser context), `ratings-per-user`, `device-pairing` (a Listener pairs and revokes their own device; the Owner project sees it with the owner column and can revoke it; the Listener never sees the Owner's devices), `user-switch` (two user projects share one browser context: after switching, the queue, inbox markers and device id of the first user are gone, the Listener's `put_device_profile` succeeds, and a take discarded since is not in the restored queue). The `coverage.ts` matrix stays green because every UI op is still exercised by the Owner project.
8. **Docs.** `docs/architecture.md` gets a short "Users and roles" section; `docs/runbook.md` gets the `MM_USERS` bootstrap, restart-to-revoke, the deploy checklist and the per-role Authelia policy (the edge keeps its `^/human/` two-factor rule for every user; Editor accounts should use two factors because an Editor can request spend, Listener accounts may be one-factor until they pair a device, because pairing lives under `/human/` and the edge then prompts for two factors; device ownership and revocation are runbook steps); `README.md`, `docs/connectors.md`, `deploy/compose.example.yml` and `web/e2e/server.ts` move from `MM_TRUSTED_USERS` to `MM_USERS`; **`CLAUDE.md` architecture rule 4 is amended** from "exactly one allow-listed user" to "a configured set of allow-listed users, each mapped to a role"; examples use `alice`, `bob`, `carol` and `mm.example.com`; `make gate` runs.

### C315. Reserved

Reserved for the first amendment found during the contract freeze.

## Rollout and risks

1. **Privilege escalation by `Kind`** (highest). Every `Remote-User` is `KindHuman`; without C302 and C303 an Editor or Listener could approve spend, change caps, arm the provider or publish. Land lane `u1-core` first with a single Owner and the archtest; behaviour is then identical to today. Do not provision a non-owner until `u1-view` and the leak sweep are merged, and keep pairing and Subsonic Owner-only (F1b) until lane D1 and the device isolation tests are merged.
2. **Ratings migration is not additive** (PK change). Mitigation in C306: recreate every trigger, backup first, rehearse on a copy, pairing CHECKs. Orphan raters never block startup.
3. **Silent fan-out** of `source='human'` joins: the view plus the lint test (C307).
4. **Discarded leak by id**: central `Visible`, the archtest, the sweep. Signed links minted earlier live until expiry (accepted).
5. **Cost leak outside spend ops**: pointer redaction, money-field scan, sweep.
6. **Connector path**: the verifier must be changed in the first lane or an Editor could obtain Owner capability through a grant.
7. **Pending-reservation lock-up**: an Editor batch holds its ceiling on the global caps until approved, rejected, withdrawn or 24 h; the Owner can reject; Editor-specific limits are deferred (Q1).
8. **Generated artifact churn** (`openapi.json`, `schema.d.ts`, `whoami`, `TrackRating`, track and song goldens): `make openapi`, commit both, review golden diffs.
9. **Revocation needs a restart.** The role map is read at startup; there is no reload. Demoting or removing a user takes effect on the next restart; the runbook lists it as a step. (Optional reload: Q17.)
10. **Unattended deploy**: run `metamusic config check` first; never deploy a migration unattended. **Rollout checklist (task X1):** (1) read the production `MM_TRUSTED_USERS` value; it must hold exactly one name; (2) deploy with `MM_USERS=<that name>:owner` identical to today, attended, confirm login; (3) back up and rehearse the migration on a `VACUUM INTO` copy (X2); (4) create the Authelia accounts, group and access-control rule (an Authelia user absent from `MM_USERS` gets 401; the `^/human/` two-factor rule stays for everyone); (5) add users to `MM_USERS` one at a time, restarting attended; (6) the device migration is additive and defaults every existing device and Subsonic client to the Owner, so verify one Owner phone still works before a non-owner pairs anything. The OIDC and connector `mm_user` checks use the same map (`config_m4a.go`, `connectors/verifier.go`).
11. **Rename of a user** orphans nothing for the Owner (key `owner`); for others, ratings stay under the old name as inert "former user" rows and the new name starts fresh (no remap tool in v1, Q14).
12. **Media metadata leak**: masters embed seed, request id and recipe id (D-U1-17); Listener media is the two stripped renditions only (`mp3_320_clean` and `flac_clean`), built on demand under a concurrency cap (C308.7, C308.8), so one Listener cannot saturate the CPU.
13. **Prompt injection through ratings**: Listener and Editor verdict text must never reach an agent's context (C306.4, C309); the agent rating sweep pins it.
14. **Devices as an escalation path.** A paired phone of a Listener must not inherit Owner powers (the old kind-based rules did exactly that). Mitigation: role-based predicates (C303), the owner key written only by the server (C301.7, C312.1), `device-editor` and `device-listener` matrix rows, the isolation tests (C314.5), restart-to-revoke plus immediate Owner revoke, a per-user device limit.

## Lanes and tasks

Each lane is sized for one Sonnet builder (target under about 2.5k changed lines excluding generated files). Foundation first and alone; then parallel lanes; Opus verifies each merge. Task checklist: write the named tests first, watch them fail, implement, run `make check` plus the focused package tests, record any amended contract in this file in the same commit.

| Lane | Task | Scope | Depends | Est. size |
|---|---|---|---|---|
| u1-core | **U1-F1** | re-verification step (counts to the revision log), `auth.Role`, `ScopesFor`, `Has` implication, `MM_USERS` parsing, lowercase folding and validation, `metamusic config check`, `proxyPrincipal`, connector verifier and OIDC check, `Meta.OwnerOnly`, `Permitted`/`Authorize`, `validate()`, `x-mm.owner_only`, service guards (C302.6), `ActsAsHuman` split with per-site decisions, `role_predicates_u1_test.go` (incl. `KindAgent`), `servertest.Rig.User`; one Owner in production | none | ~2.4k |
| u1-core | **U1-F1b** | the one mechanical pass over `internal/api/ops_*.go` Meta: `OwnerOnly` tagging per C305 (devices, pairing and Subsonic client ops stay `OwnerOnly` here; D1 lifts that), `listen`/`rate` scope retag (incl. `ops_m2a_test.go` row), sensitive read-op rule; no other lane touches those files until it merges | F1 | ~1.2k |
| u1-core | **U1-F2** | `role_matrix.golden` + `TestRoleMatrixComplete`, audit gate rule (C304.4), `whoami` fields, Editor and Listener 401 on `/mcp` test, parity (Owner agent tool-list golden), regenerated `openapi.json` and `schema.d.ts` | F1b | ~1.5k |
| u1-ratings | **U1-R1** | migration, views, `Rate` per rater, `TrackRating` shape (folded into `get_track.rating`), orphan warning and `library verify` finding, `TestMigration_U1` | F1 | ~1.4k |
| u1-ratings | **U1-R2** | rewrite of every ratings consumer (C307, enumerated by file at lane start), listening readers and index (C306.7), `TestNoRawHumanRatingJoin`; lands before V1 starts on shared files | R1 | ~2.2k |
| u1-view | **U1-V1** | `internal/view`, `library.Visible`, discarded filtering in search, by-id reads, lineage, songs, playlists, crates, records, Atlas, facets, `compare_tracks`; Listener `q` and filter rules (C308.3b); `TestTrackHandlersCallVisible` | F2, R2 | ~2.4k |
| u1-view | **U1-V2** | money and recipe redaction in all types, Muse text and structure, private notes, identity masking (C309), Listener `listen` rate class and audit exemption wiring, Subsonic discarded handling, `get_export` and `put_device_profile` ownership | V1 | ~1.8k |
| u1-view | **U1-V3** | Listener renditions (D-U1-17, C308.7, C308.8): step 0 tag dump of existing `mp3_320`; `mp3_320_clean` and `flac_clean` on-demand build, cache and build cap; tool readiness; `library verify` rendition checks and the restore test; `TTLFor` by role (devices included); download and Subsonic role-aware rendition lookup (never the master); `TestListenerFlacClean`, `TestListenerAudioStripped` | V1 | ~1.6k |
| u1-core | **U1-D1** | device and Subsonic ownership (D-U1-14, C312): migration `<next+1>_device_owner.sql`, principal construction from `owner_key` with role clamp, pairing and Subsonic client creation bound to the caller, own-only list and revoke (Owner all), per-user limit, listen-event owner join (C306.7), lifting the F1b `OwnerOnly` on those ops, `device_profiles` rule, matrix rows, `Rig.DeviceFor` and `Rig.SubsonicClientFor`, `TestMigration_U1Devices`, device isolation unit tests | F2, R2, V3 | ~2.0k |
| u1-editor | **U1-E1** | `editor_batch` approval, `ViaAPI` forcing, deciders guard, reduced `get_budget`/`get_provider_status`/estimates/`CAP_EXCEEDED`, `mayCancel`, transform rules and Editor pool, backlog gate for Editors | F2 | ~1.5k |
| u1-web | **U1-W1** | `me` store, `can()`, per-user browser state reset (C313.7), nav, tabs, routes, `SecureGate`, approvals polling, no-access page, generation request flow for Editors; compiles against the F2-regenerated schema, E1 fields are optional | F2 | ~2.0k |
| u1-web | **U1-W2** | rating UI (mine, others, average), My devices page (pair, list and revoke own; Owner sees all with an owner column), Listener and Editor shells polish, bun unit tests | R1, W1, V2, D1 | ~1.9k |
| u1-int | **U1-INT-B** | Listener leak sweep, acceptance rows, ownership tests, per-user isolation, Owner-derived-state tests, money-field scan, reflection-generated filter sweep, agent rating sweep, device isolation sweep, average-rating shape tests, Listener audio tests (C314.5) | V2, V3, D1, R2, E1 | ~2.0k |
| u1-int | **U1-INT-W** | Playwright projects and specs, `MM_USERS` e2e env, `e2e-map.txt` | W2, INT-B | ~1.0k |
| u1-int | **U1-X1** | docs (architecture, runbook, README, connectors, compose example), `CLAUDE.md` rule 4 amendment, rollout checklist, revision log, roadmap edit | INT-B, INT-W | small |
| u1-int | **U1-X2** | production-copy check: `VACUUM INTO` snapshot, run the migration, `library verify`, rating counts equal before and after, spend-free | X1 (and so INT-B, INT-W) | small |
| u1-int | **U1-X3** | operator review, then create real Editor and Listener accounts one at a time (checklist in Rollout 10) | X2, operator | small |

**Hot-spot ownership** (one owner per file; others ask the owner or wait for the merge):

| Files | Owner |
|---|---|
| `internal/auth/**`, `internal/config/**`, `cmd/metamusic` (config check), `internal/api/registry.go`, `permit.go`, `internal/archtest/role_predicates_u1_test.go`, `internal/servertest/rig.go` | U1-F1 |
| `internal/api/ops_*.go` Meta tags and `ops_m2a_test.go` | U1-F1b only (one pass); afterwards the lane that owns the op's behaviour |
| `internal/api/testdata/role_matrix.golden`, `internal/mcpgen/**`, `ops_system.go`, `internal/gate/**` | U1-F2 |
| `internal/api/types_*.go` | R1 (`TrackRating`), V2 (redaction), E1 (batch and budget fields), one lane at a time in that order |
| `openapi.json`, `web/src/api/schema.d.ts`, `internal/**/testdata` goldens (track, song, role_matrix) | regenerated **only at merge** (rebase, then `make openapi`); the Opus integrator resolves conflicts, no lane hand-edits them |
| `internal/store/migrations/<next>_ratings_per_user.sql`, `internal/judgments/ratings.go`, the ratings consumers (by file list from R2) | U1-R1, then U1-R2; R2 lands before V1 touches the shared files |
| `internal/view/**`, `internal/library/**`, `internal/media/serve/**`, `internal/subsonic/**`, `internal/exports/**` | U1-V1, then U1-V2 (redaction), then U1-V3 (renditions, TTL, Subsonic lookup); U1-D1 touches Subsonic client auth only after V3 merges |
| the device owner migration, device and pairing packages (named by D1), device and Subsonic principal resolution in `internal/auth` (after F1 merges) | U1-D1 |
| `internal/spend/**`, `internal/generation/create.go`, `internal/generation/patches_m5.go`, `internal/transform/**` (role rules), `internal/jobs/service.go` (cancel only) | U1-E1 |
| `web/src/lib/{me.svelte.ts,nav.ts,router.ts}`, `web/src/App.svelte`, `web/src/routes/**`, `web/e2e/**` | U1-W1, then U1-W2, then INT-W |

## Definition of done (U1)

All lanes merged through branch-protected `main`; `make check` and CI heavy tiers green; the Listener leak sweep (including a Listener's paired device) and the role matrix golden green; `library verify` clean on a production copy and rating counts equal before and after; registry classified (every op in the matrix); `openapi.json` and
`schema.d.ts` regenerated; docs, `CLAUDE.md` rule 4 and `make gate` clean; one Editor request approved by the Owner end to end in e2e; the deploy done attended with `metamusic config check` run beforehand.

## Open questions for the operator

Each has the default this plan assumes. Questions marked RESOLVED were answered by the operator on 2026-11-18 and are folded into the decisions, contracts, table, lanes and tests above.

1. **Editor spend limits.** v1 has none beyond the global caps and the Owner's approval of every Editor batch; a pending batch reserves its ceiling until approved, rejected, withdrawn or 24 h. Want an Editor daily pot (default 0), a pending limit of 3, coalesced pushes? Confirm deferral?
2. **Editor cost visibility.** Own estimate and settled cost visible, shared daily-left visible, others' batches visible without cost or creator. Right?
3. **RESOLVED 2026-11-18: phones and Subsonic for Editors and Listeners.** Editors and Listeners may pair their own phone or browser and use Subsonic apps under their own identity (D-U1-14, C312, lane D1): the device carries the pairing user's role, never more, and its ratings attribute to that user; the Owner lists and revokes all. The remainder, an offline set for non-owner phones, is Q22.
4. **Editor and `update_track`.** Editors may edit titles and `to_relisten`; `final` and `discarded` stay Owner-only because discarding changes what Listeners see. Agree? May an Editor edit a published record (assumed no)?
5. **RESOLVED 2026-11-18: average rating for Listeners.** Hidden for Listeners (D-U1-16 confirmed). Editors see their own plus the average; the Owner sees everyone plus the average.
6. **Muses for Editors.** Read-only access assumed; Listener never. Should Editors see Muse text at all, and should they edit Muses (assumed no)?
7. **Lyrics and style for Listeners.** Lyrics, derived style terms and BPM visible; raw recipe hidden; `get_lineage` shown with redaction. Lyrics are already served by Subsonic `getLyrics`; is that acceptable?
8. **Duels, callbacks and picks for Editors.** Owner-only in v1; opening them needs a `rater` on those tables. Ever?
9. **Comments for Listeners.** Hidden as private notes. Any Listener-visible comments?
10. **Playlists for Listeners.** Playlists and crates are shared and edited by Owner and Editors. Do Listeners want their own playlists?
11. **RESOLVED 2026-11-18: second Owner.** Confirmed: exactly one Owner in v1 (D-U1-1).
12. **Local auth.** `MM_AUTH=local` stays Owner-only (dev mode). Acceptable?
13. **OpenAPI exposure.** Should `/openapi.json` show Listeners the full surface or only their permitted ops?
14. **Renames.** If an SSO username changes, the old ratings stay as "former user" rows and the new name starts fresh (no remap tool in v1). Add a remap or alias table?
15. **Notifications.** Push on every Editor request (assumed; coalescing deferred).
16. **RESOLVED 2026-11-18 (deferred): Editors and agents.** Editors use the web app (and their own paired phones, D-U1-14) only; no Editor-minted agent tokens and no MCP path in v1. It stays in the deferred list (D-U1-19).
17. **Reload.** Revocation needs a restart. Add a SIGHUP or file-backed live reload of the user map?
18. **RESOLVED 2026-11-18: Listener media.** Listeners receive MP3 320 and a lossless FLAC, both stripped of every `MM_*` key (D-U1-17, C308.7, C308.8); a Listener never receives the frozen master or its sha256.
19. **Heat and listen events.** Editors do not see the Owner's listen heat (assumed). Show it read-only?
20. **Pick marker and `unheard` for Listeners.** Both hidden in v1 (Owner curation and Owner listening state). Should a Listener see which take is a song's pick, or their own `unheard` filter (bound to their own plays)?
21. **Per-user device limit.** Default 5 active devices and 5 active Subsonic clients per Editor or Listener (422 `DEVICE_LIMIT`; the Owner unlimited). Right number?
22. **Offline set for non-owner phones.** The offline set stays a single Owner set (D-U1-10); a Listener's or Editor's phone can stream (Subsonic, web) but has no offline set. Needs an owner key on the offline set; add it later?

## Revision log

Draft 1 (2026-11-18): design only. Lines cited from subsystem maps are unverified until the owning lane re-reads them.

Revision 2 (2026-11-18, review pass): orphan raters no longer stop startup; revocation is restart-only (no "immediate demotion"); `OwnerOnly` refuses human Editors and Listeners only, non-human delegates pass by scope; `propose_pick` and the critic duel and callback paths are not OO; audit gate predicate corrected for the `listen` retag; transform family classified (Editor allowed, own-only cancel, own pool, backlog gate); Listener query-side oracles and media tag leak closed; archtest covers `KindAgent`; `device_profiles` writes own-only; lowercase folding; ratings pairing CHECKs; ratings lint targets the table; Editor spend cut to approval-always on global caps; `list_users`, `list_ratings`, `MM_RATER_REMAP`, `principal_role`, per-role MCP servers deferred; F1b mechanical tagging pass, generated artifacts only at merge, R2 before V1; rollout checklist and `config check`; `CLAUDE.md` rule 4 amendment. Counts re-verified: 34 `ActsAsHuman` lines, about 25 `FROM/JOIN ratings` lines.

Draft 2 (2026-11-18, review pass, verified in the `u1-design` worktree): agent rating shape (C306.4, C309; `others[]` never to agents); Listener filter allow-list with reflection sweep (C308.3b; `SearchParams`, `SongFilterIn`, `atlasIn` re-read; pick marker and `unheard` hidden, Q20); Listener media `mp3_320_clean` (D-U1-17, C308.8; fresh ingest already uses `-map_metadata -1`, importer-built files unverified so step 0 dumps tags; the `renditions` table is `(track_id, kind)`, kind Go-validated, so no migration; the Dockerfile already asserts ffmpeg and libmp3lame); browser state reset (C313.7; keys `mm:player:v1`, `mm-media-v1`, `mm-offline` confirmed); `get_ladder` classified once as `OwnerOnly` (it is scope `read` in `ops_duels.go`), duplicated notation and test lines removed; `listen` rate class, `log_listen_events` audit exemption and `Visible` on write paths (C304.4, C306.5, C311).

Revision 3 (2026-11-18, operator answers folded in): Q5 average rating hidden for Listeners confirmed (D-U1-16; tests for Listener, Editor and Owner shapes, C306.4, C314.5). Q18 resolved: Listeners get MP3 320 and lossless FLAC, both stripped of every `MM_*` key (D-U1-17; new C308.7 specifies the `flac_clean` rendition key, stream-copy build and on-demand cache, hashes; C308.8 the build cap, `library verify` rendition checks and backup and restore coverage; tests `TestListenerFlacClean` and `TestListenerAudioStripped`; new task U1-V3 split from V2; a Listener never receives the master or its sha256). Q16 resolved: Editors are web app only, no Editor-minted agent tokens, kept in D-U1-19. Q3 resolved: Editors and Listeners pair their own phone or browser and use Subsonic apps under their own identity, replacing D-U1-14's Owner-only rule: owner key on devices, Subsonic clients and pairing codes (C301.11, migration 0018), role inherited and scopes clamped (C301.1, C301.3, C301.7), `OwnerOnly`, `IsOwnerSide` and `CanSeePrivate` read the role not the kind so devices are no longer automatic Owner delegates (D-U1-6, D-U1-7, C302.1, C302.6, C303), media TTL by role including Listener devices (D-U1-13, C308.5), Subsonic rules (C308.6), new self-service devices table and limit in C305, C312 rewritten, My devices page (C313), `device-editor` and `device-listener` matrix rows, isolation, restart and pairing tests (C314), new risk 14, new task U1-D1 and ownership rows, listen-event owner join (C306.7). Q11 confirmed: exactly one Owner in v1 (D-U1-1). Corrections: `get_offline_set` Editor cell changed from Y to N to match D-U1-10; pairing sits under `/human/`, so a Listener who pairs a phone meets the edge two-factor rule (C312.1, C314.8). New open questions Q21 (device limit) and Q22 (offline set for non-owner phones). Offline set, bookmarks, duels, callbacks, voice notes and picks stay Owner-only.

Revision 4 (2026-11-19, lane U1-F1 built; re-verified against `origin/main` before any code). Counts: `ActsAsHuman` 34 non-test lines (definition included) and 42 with tests, as in revision 2. Raw principal-kind checks outside `internal/auth` (`Kind ==`/`!=` `KindHuman`/`KindAgent`, and `case` forms): 30 non-test lines in 23 files, 25 of them agent rules, found by `rg` and now all expressed through predicates. `FROM/JOIN ratings`: 25 non-test lines in 17 files (41 more in tests), unchanged for R2. Amended contracts:
- **C301.1/C301.2.** `Role` is defined in `internal/config` (`config.Role`, `RoleOwner`, `RoleEditor`, `RoleListener`), because `auth` imports `config`; `auth.Role` and the three constants are aliases, so every consumer still writes `auth.Role`. `Config.Users` is `map[string]config.Role`; `Config.TrustedUsersAlias` records alias use for `config check`. Unset `MM_USERS` and `MM_TRUSTED_USERS` leaves `Users` nil (no proxy user can log in, as before, so local mode and tests need no user); the "needs an owner" and "exactly one owner" rules apply to a non-empty map. Setting both variables is refused. `MM_TRUSTED_USERS` with one name is `MM_USERS=<name>:owner` (folded to lowercase); with several names startup refuses, naming the variable.
- **C301.8.** `metamusic config check` prints `users: name:role,...` (names and roles only) and a deprecation warning line when the alias is used; startup shares `config.LoadAll`.
- **C301.4.** The connector verifier takes `Users map[string]config.Role` (not `TrustedUsers`), requires role owner for the granting user and folds the claim to lowercase; the OIDC config check requires a non-empty `Users`.
- **C303.1.** Two predicates were added to the four of D-U1-7: `IsHuman()` (kind is human; used by `Permitted` for `HumanOnly`) and `IsAgent()` (kind is agent; replaces every raw `Kind == KindAgent` outside `auth`). Sites that were "human or device, Owner side" compose `IsPerson() && IsOwnerSide()` (voice notes, pins, comment moderation, duel and callback source, published-record edits, taxonomy decide, tape `accept_warnings`, nextactions human audience); `Publish`, curation settings and `estimateVia`/`create` human path use `IsOwner()`. `IsOwner()` is fail-closed: a human principal with an empty `Role` is not the Owner, so test principals now carry `Role: auth.RoleOwner`. A device has an empty role until lane D1 (an Owner delegate). `RaterKey(p)` lives in `auth` (owner, username, `critic`, "" for system and anonymous).
- **Review fixes (same lane).** `Register` closes the `sensitiveReadOps` ids (get_audit, list_tokens, list_backups, list_import_runs, get_spend, list_ledger, get_provider_status, list_connector_grants) to non-Owners through a gate-only copy of the meta (openapi unchanged) until F1b tags them; `servertest` pins it. `CreatePairingCode` requires the Owner side. `RaterKey` marks the device branch as a D1 hand-off. The archtest case-ban now matches `KindHuman`/`KindAgent` anywhere in a `case` line. Mechanical `ActsAsHuman` replacements were made in `ops_*.go` and `ops_m2a_test.go` (no Meta edits), so F1b rebases on them. Names are folded to lowercase for the alias too: an Owner configured as `Alice` becomes principal `human:alice`; rows keyed `human:Alice` must be checked or renamed before upgrading (operators almost always already use lowercase), and several `MM_TRUSTED_USERS` names now refuse startup (move to `MM_USERS`). Merge F1 only together with or before F1b, and provision no Editor or Listener before F1b and D1.
- **C303.3.** `role_predicates_u1_test.go` bans the old patterns with a regexp scan; its allow-list is empty because `IsAgent` covers every agent-only rule.
- **C301.6.** Followed as written (D-U1-5's "keep the legacy scopes" is read as "also holds them"): the Owner's `whoami` `scopes` now lists six names (`listen`, `rate` added); nothing in the SPA or tests depends on the count except `internal/httpserver/server_test.go`, updated.
- **C302.3.** `validate()` accepts the `listen` and `rate` scopes and `Meta.OwnerOnly` (`owner_only,omitempty`, so `openapi.json` is unchanged until F1b tags ops). The "requires `OwnerOnly`" rule (`validateOwnerOnly`, with the sensitive read-op id list) is implemented and unit-tested but switched off by `enforceOwnerOnlyTags = false` in `internal/api/permit.go`, because the tagging pass over `ops_*.go` belongs to F1b; until then `Permitted` treats `HumanOnly`, cost `approve` and scope `admin` as owner-only on its own. **F1b: tag the ops, extend `sensitiveReadOps` from `openapi.json`, and flip the constant to true.** `whoami`, `ping` and the browse ops are still scope `read` until F1b retags them, so a Listener is refused them for now (accepted: no Listener exists before V1). (Superseded by Revision 5: `whoami`, `ping` and the browse ops are now scope `listen`.)
- **C302.6.** Service guards (`IsOwnerSide`, 403 `OWNER_ONLY` via `problem.OwnerOnly`): `spend.ApproveBatch`, `RejectBatch`, `SetCaps`, `generation.Service.SetCaps`, `jobs.ProviderAdmin.Update`; pins use `IsPerson() && IsOwnerSide()`; publish and curation settings use `IsOwner()`.
- **C314.** `servertest.Rig` always configures bob (editor) and carol (listener) beside the Owner in proxy mode, because the role map is fixed at startup; `Rig.User(tb, name, role)` returns a proxy client for one of them and fails for any other name or role. Not in F1: `TestOrphanRaterDoesNotBlockStartup` (needs the `rater` column, lane R1), the device and Subsonic principal tests (lane D1), the golden matrix and `whoami` fields (F2).

Revision 5 (2026-11-19, lane U1-F1b built). The tagging pass over `ops_*.go` Meta is done and `enforceOwnerOnlyTags` is `true`. Amended contracts:
- **C305 tags.** `OwnerOnly` is set on: `get_ladder`, `get_offline_set`, `get_system_info`, the voice, listen-heat, duel and callback-verdict ops (`create_voice_note`, `get_listen_heat`, `create_duel`, `submit_duel`, `next_duel`, `suggest_duel_pairs`, `list_duels`, `get_duel`, `submit_callback_verdict`), `pin_pick`, `dismiss_pick_proposal`, `decide_taxonomy_proposal`, `map_sound_term`, `publish_record`, `create_muse`, `update_muse`, `assign_muse_songs`, the curation and songwriting settings writes, `create_generation_human`, `approve_batch`, `reject_batch`, the spend, ledger and provider ops, the Administration family (audit, tokens, backups, connectors, imports, `get_device_probe`, `list_device_profiles`) and, until D1, the pairing, device and Subsonic client ops. Not tagged: `propose_pick`, `revoke_own_device`, `revoke_own_token`, `update_track` (its `final` and `discarded` field rule is a service guard for the lane that owns that behaviour), `cancel_batch`, `cancel_job`, `get_budget`, `get_adherence`.
- **Listen and rate retag.** Scope `listen`: the 27 browse ops of the C305 Library browse table plus `whoami` and `ping` (29 in all) (`get_ladder` is `listen` and `OwnerOnly`). Scope `rate`: `rate_track`, `log_listen_events`, `put_device_profile`. `ops_m2a_test.go` rows follow.
- **`sensitiveReadOps` final list:** the eight F1 ids plus `get_import_run`, `get_device_probe`, `list_device_profiles`. The gate-only copy of the meta in `Register` is removed (the tags are real now).
- **`get_provider_status` conflict.** The C305 spend table shows an Editor redacted view of `get_provider_status`, while D-U1-6 and C302.3 list it as sensitive. Fail closed won: it is `OwnerOnly`; the lane that builds the Editor redacted shape must split the redacted view into its own op (or lift the tag with that shape in the same change). `get_budget` already gives an Editor `approval_required` and `generation_enabled`.
- **Audit predicate (pulled forward from F2, one line).** Retagging browse ops to `listen` would have made `internal/gate` audit and hold every human browse GET (it audited any scope other than `read`). `gate.go` now treats `listen` like `read`. `NoAudit`, the `listen` rate class and the `log_listen_events` exemption stay in F2.
- `openapi.json` and `schema.d.ts` are not regenerated here (merge-time artifact, F2); tests read the live registry.


- **Fail-closed holds (F1b review).** Until the redaction and service-guard lanes land, these are also `OwnerOnly` (agents and the Owner side still pass): `get_budget` (no role redaction yet; the Editor `approval_required`/`generation_enabled` view ships with E1), `create_generation` and `create_variant` (`editor_batch` forcing is E1), `update_track` (field-level `final`/`discarded` rule), `put_device_profile` (no `created_by` guard yet, scope stays `rate`), `get_stats`, `list_batches`, `get_batch`, `list_jobs`, `get_job`, `get_track_context`, `get_export`, `update_record`. The lane that adds each redaction or guard lifts the tag with it. This corrects the earlier claim that `get_budget` already redacts for Editors.

Revision 6 (2026-11-19, lane U1-R1 built on `origin/main` 2530c14, merged with F1b 1868f33). Migration number **0017** (`0017_ratings_per_user.sql`), as planned. Amended contracts:
- **C306.1/C306.2.** Followed as written; the new index is `ratings_track_rater` on `(track_id, rater)`. A plain transactional migration is enough: no foreign key points at `ratings`, so the `-- mm:foreign_keys=off` directive is not used, and the copy runs before the triggers exist, so the migration enqueues nothing for provenance. Up-only. Views `owner_ratings` and `critic_ratings` expose every column of the table (including `rater`). `TestMigration_U1` (schema 16 to 17 on the M5 fixture plus a device-origin and a cleared human row) pins counts, raters, triggers (the `pd_*` owner-only WHEN), indexes, the pairing CHECKs, the views, the upsert key and that a second rater does not enqueue provenance.
- **C306.4 (shape, agent and Listener cases).** `library.Ratings` returns the unshaped set (`TrackRating.Raters`, every human rater, tagged `json:"-"`; `Human`/`Mine` hold the Owner's row, `Critic` the agent row) and `library.ShapeRating(viewer, raw)` renders it; `api.maskRating` calls it on every path that returns a `TrackRating` (`get_track`, `update_track`, `rate_track`), so the raw rows never reach a wire. Owner side (the Owner and the Owner's devices): `mine`, `human` (copy of `mine`), `others` (non-Owner raters with stars or a verdict, sorted by name), `average {mean, n}` over every user's stars (mean rounded to two decimals; a cleared rating does not count), `critic`. Editor: `mine`, `human`, `average`, `critic`, no `others`. Listener, and a Listener's device: `mine` and `human` only; `critic` is **null** (not absent: the key keeps the Owner's wire shape) and there is no `average` or `others` key. Agent (an Owner delegate): `mine` is the critic's own row, `human` keeps the Owner's row as before, no `others`, no `average` (the numeric average is withheld from agents too, C306.4 lists only Owner and Editor). `mine` is always present (null when none). `others[].user` is the raw rater key: the "former user" label for a name no longer configured is a web concern (W2); the library does not read configuration. The wire shape of `mine`, `others` and `average` is un-documented in the schema to stay inside the tools/list byte ceiling.
- **C301.7 (`RaterKey`, device branch).** The F1 hand-off is resolved with the fields D1 will set: a person principal (human, device or Subsonic client) whose `Role` is not owner and whose `User` is set rates as `User`; a device with neither (today's Owner devices) rates as `owner`. D1 therefore only has to fill `Role` and `User` from `owner_key`; a Listener's device can then never read or write the Owner's row.
- **C306.5.** `Rate` takes the rater from `RaterKey(p)` and refuses a principal without one (`403 CANNOT_RATE`, nothing written). The `library.Visible` call on the Listener path is V1's (`library.Visible` does not exist yet). F1b (merged) has since retagged `rate_track` to scope `rate` (see Revision 5), so the rollout rule below is now the only guard: no Editor or Listener account until R2 lands.
- **C301.10 (orphan raters).** `judgments.OrphanRaters` lists human raters other than `owner` that the configured user map does not contain. `metamusic serve` logs one `WARN` line per orphan (name and row count); `metamusic library verify` prints `warning orphan_rater <name> <n> ...` and does not count it (exit code unchanged). There is no metrics registry in the code base, so the planned metric is not built. `TestOrphanRaterDoesNotBlockStartup` (cmd) pins the start and the warning line.
- **Not yet safe with several raters (R2).** The existing readers still join `ratings` on `source='human'` (`library` `ratingJoins`, stats facets, songstate, picks, subsonic catalog and the other consumers listed for R2); with only the Owner's rows (all of production until non-owner accounts exist) they are unchanged, and they fan out once a second rater has written. R2 must land before any non-owner account is provisioned, as the sequencing section already says.
- **Test fixtures.** The `librarytest` fixtures also run on older schemas for the upgrade tests, so they write the `rater` column only where it exists (`librarytest/rater.go`); the 20-odd test-only `INSERT INTO ratings` statements in other packages were given an explicit rater (`owner` for the human source, `critic` for the agent one).
- **MCP tools/list ceiling.** `toolsCeilingBytes` re-baselined 115968 to 117300 (measured 117206, +1338): the per-user `TrackRating` appears on `get_track`, `rate_track` and `update_track` (F2 owns `internal/mcpgen`; this is a one-constant change, re-baseline again at merge if the integrator's numbers differ).
- **Generated artifacts not regenerated (ownership table).** `TestToolsListGolden` (mcpgen), `TestTrackDetail_Golden` and `TestTrackDetail_M2aGolden` (library), and `TestOpenAPIGolden` (cmd) drift by exactly the new `mine`, `others`, `average` fields and are regenerated at merge with `make openapi` and the `-update` flags.
- **Review fixes (R1).** `RaterKey` fails closed: a person with an Editor or Listener role and no user has no rater (403 `CANNOT_RATE`); only an empty role or `owner` maps to `owner`. `Rate` refuses before the track lookup. The `rate_track` description no longer says humans rate as the operator. The Subsonic `setRating` edit in `internal/subsonic/annotate.go` (reads the caller's own row through `ShapeRating`) overlaps V1/V2/V3's files; expect a trivial conflict there.
- **Rollout rule.** No Editor or Listener account may be provisioned, and F1b must not retag `rate_track` to scope `rate`, until R2 has switched the `source='human'` readers to the `owner_ratings` view and added `TestNoRawHumanRatingJoin`; R2 starts with that lint.
Revision 7 (2026-11-19, lane U1-F2 built, merged after U1-R1). Amended contracts:
- **C314.1 role matrix.** `internal/api/testdata/role_matrix.golden` has one row per registered op: `op`, `scope`, `hold`, then the nine principal columns `owner, editor, listener, agent-read, agent-write, agent-admin, device, device-editor, device-listener`, each `Y`, `OO` (OWNER_ONLY), `HO` (HUMAN_ONLY) or `SC` (FORBIDDEN_SCOPE), computed by `api.Permitted`. The device rows are synthetic principals (the Owner's device has no role and scopes `read,write`; Editor `listen,read,rate,write`; Listener `listen,rate`, the C301.3 clamp) because real device principals arrive with lane D1. `TestRoleMatrixComplete` fails for an op missing from the golden (fail closed), for a golden row of a removed op, and on any diff; `TestRoleMatrixDelegatesUnchanged` compares the agent and Owner-device columns with an independent pre-U1 rule; `TestRoleMatrixListenerBounds` pins that no Editor or Listener row passes an owner-only or human-only op and that a Listener never passes a scope outside `listen`, `rate`. Regenerate with `go test ./internal/api -run 'TestRoleMatrixComplete$' -update` (the `-update` flag is shared with the m2b fixture test, so always pass `-run`).
- **Fail-closed holds.** `failClosedHolds` in `role_matrix_test.go` names the lane that lifts each F1b hold and prints it in the golden's `hold` column: E1 `get_budget`, `create_generation`, `create_variant`, `update_track`, `update_record`, `list_batches`, `get_batch`, `list_jobs`, `get_job`; V2 `put_device_profile`, `get_export`, `get_stats`, `get_track_context`; D1 the pairing, device and Subsonic client ops. The plan names no lane for the `update_track` field rule and the published-record rule of `update_record`; they are marked `unassigned, E1 proposed` in the golden until the operator assigns a lane. `TestRoleMatrixFailClosedHolds` fails when a held op loses `owner_only`, so the lifting lane must delete its entry and re-bless the golden in the same change.
- **C304.4 gate.** Done: `Meta.NoAudit` (`no_audit`, only scope `rate`, no idempotency, not human-only, enforced by `validate()`) set on `log_listen_events`; the predicate is `!NoAudit && (agent || human_only || scope not in {read, listen})` (an agent calling a `NoAudit` op is not audited either). New `Meta.Rate` value `listen` (scope `listen` or `rate`), set on `rate_track` and `log_listen_events`; the gate adds `ratelimit.Listen` (default 60 per minute, burst 60) to the Human class for Editor and Listener principals only (`!IsOwnerSide()`), so the Owner and agents are unchanged; over limit answers 429 `RATE_LIMITED` with `Retry-After`, `bucket: listen`. These two Meta values are the only edits to `ops_listen.go` and `ops_judgments.go`. The per-batch size cap on `log_listen_events` stays in lane V2/R2 (service level) and is a recorded blocker for them: until then each call is capped at 200 events and an Editor or Listener is bounded only by the listen bucket (60 per minute). Decision: an agent's `log_listen_events` calls are no longer audited either (the op's rows are an attributed log); audit rows for refused calls are skipped too.
- **C304.3 whoami.** `GET /api/me` gains `role` (nullable, null for agents and connector grants), `user` (nullable) and `allowed_ops` (sorted op ids for which `Permitted` passes). The MCP `whoami` output schema changes accordingly (additive).
- **C304.2 golden.** The pre-U1 tool-list golden could not stay byte-identical: `whoami`'s output schema gained the three fields above, and nothing else in `tools_list.golden.json` changed (the diff is 18 added lines). `TestOwnerAgentToolsListGolden` pins that an all-scopes agent gets the same list as the read-scope agent golden. `TestEditorListenerMcp401` pins 401 plus the Bearer challenge for Editor, Listener, the Owner's login and a device on `/mcp` and `/mcp/`.
- **tools/list ceiling.** `toolsCeilingBytes` re-baselined at the merge with U1-R1 (117300) to 117406 (measured 117306 with both lanes, +100 headroom; the whoami fields add 100 bytes on top of R1).
- `openapi.json` and `schema.d.ts` are regenerated (`make openapi`): the `whoami` fields and `x-mm.rate`/`x-mm.no_audit` on the two ops.
