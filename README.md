# metamusic

A self-hosted, single-user "music lab" around YuE2 (an open lyrics-to-song model):
generate, listen, judge, regenerate. The web/mobile UI and LLM agents (REST and MCP) have strict
parity: every capability is one registered API operation, and REST, MCP tools and the SPA client
are all derived from that registry.

One Go binary embeds a Svelte 5 SPA. GPU work runs on RunPod Serverless.

Status: milestone M4a in progress (remote agents and mobile: hosted connectors, Subsonic clients, offline PWA) on top of M1 to M3 (import and listen, judge and edit, duels, generation). See [docs/plans/2026-10-12-roadmap.md](docs/plans/2026-10-12-roadmap.md)
and [docs/architecture.md](docs/architecture.md).

## Quick start (development)

Requirements: Go 1.27, Bun, bash 4+.

```bash
make tools      # download pinned golangci-lint and gitleaks into ./bin
make hooks      # install git hooks (privacy gate)
make dev        # local auth mode, data in ./data/dev
make check      # gofmt, vet, lint, go test -race, web lint/check/unit
make e2e        # Playwright (chrome + firefox) against a coverage-instrumented binary
make gate       # public-repo privacy gate
```

Every `make` build/test target runs through `scripts/loop`, which appends a metrics row to
`metrics/loop.jsonl` (no command lines, paths or secrets).

## Docker

```bash
(umask 077; mkdir -p deploy/secrets && openssl rand -hex 32 > deploy/secrets/secret_key)
docker compose -f deploy/compose.example.yml up --build
```

Container defaults: `MM_ADDR=0.0.0.0:8080`, `MM_DATA_DIR=/state/data`, `MM_BACKUP_DIR=/state/backup`
(both under the single `/state` volume so backups can hardlink), secrets under `/run/secrets`
(`MM_SECRETS_DIR`). Set `MM_PUBLIC_URL` to the externally visible URL (for example
`https://mm.example.com`). Put a TLS-terminating reverse proxy in front for real use.

## Importing an archive

The inbox is mounted read-only and is never modified. Try it with a synthetic inbox first
(`metamusic seed-synthetic` generates one under the data dir in dev mode):

```bash
metamusic import --dry-run                 # inventory only; writes a report under <data>/import/
# review the report, fill <data>/import/mapping.csv from mapping.todo.csv if needed, then:
metamusic import --plan <data>/import/reports/<report>.json
```

In Docker: `docker compose -f deploy/compose.example.yml exec metamusic metamusic import --dry-run`
(the inbox is bind-mounted read-only at `/state/data/inbox`; files must be readable by uid 1000).
Exit codes: 0 success (warnings allowed), 1 per-file errors, 2 usage/fatal, 3 plan changed,
4 import already running.

## Connecting an agent

Mint a token on `/human/tokens` (a human-only page) and give it the scopes you want to allow.
The secret is shown once.

Claude Code over HTTP:

```bash
claude mcp add --transport http metamusic https://mm.example.com/mcp \
  --header "Authorization: Bearer $MM_TOKEN"
```

Over stdio (no network listener; uses the local data directory, no `MM_AUTH` needed):

```bash
MM_TOKEN=mmp_... MM_DATA_DIR=./data/dev metamusic mcp-stdio
```

## Export, backup and restore

```bash
metamusic export --help                       # songs.jsonl / playlist exports from the CLI
metamusic backup                              # one complete snapshot under MM_BACKUP_DIR
metamusic restore --from <backup>/<snapshot> --into <new-data-dir>
```

Snapshots run daily at `MM_BACKUP_AT` and can be started from the UI or by an agent. See
`docs/runbook.md` for the restore drill and what snapshots do and do not protect.

## Configuration

| Variable | Meaning |
| --- | --- |
| `MM_AUTH` | Required. `local` (built-in password login) or `proxy` (trusted forward-auth). Unset means the server refuses to start. |
| `MM_ADDR` | Listen address. |
| `MM_DATA_DIR` | Data directory (SQLite, media). |
| `MM_SECRETS_DIR` | Directory holding secret files (for example `secret_key`). |
| `MM_PUBLIC_URL` | Externally visible base URL. |
| `MM_TRUSTED_PROXY` | Comma-separated CIDRs allowed to assert `Remote-User` (proxy mode). |
| `MM_USERS` | Allow-listed users for proxy mode as `name:role` pairs, for example `alice:owner,bob:editor,carol:listener`; a bare name means owner. Exactly one owner; names are case-insensitive and fixed at startup (a role change needs a restart). `metamusic config check` validates it. |
| `MM_TRUSTED_USERS` | Deprecated alias of `MM_USERS=<name>:owner`, still accepted: it must hold exactly one name (folded to lowercase), setting both variables is refused, and `metamusic config check` warns when it is used. |
| `MM_BACKUP_DIR` | Backup snapshot directory (default: `backup` next to `MM_DATA_DIR`; must not be inside it). |
| `MM_BACKUP_AT` | Daily snapshot time `HH:MM` in the process time zone (default `03:30`), or `off`. |
| `MM_TZ` | IANA time zone name (for example `Europe/Rome`) that scheduled work such as the daily schedules of later tracks runs in; empty (default) means the process zone. An unknown name stops startup. `MM_BACKUP_AT` keeps using the process zone. `metamusic config check` prints the zone. |
| `MM_EARS_LOAD_S` | Seconds a listening check (Tier B: vocal separation, speech recognition, pitch) is expected to spend loading its models on the GPU worker (default 40, 0..600). Part of the quote of every render with a listening check; the wave W2 canary calibrates it. |
| `MM_EARS_RTF_MILLI` | Expected listening-check seconds per 1000 s of audio for separation plus recognition (default 150, 10..2000). |
| `MM_EARS_TRANSCRIBE_RTF_MILLI` | The same for the vocal pitch contour, added only for takes that have a plan score (default 40, 0..2000). |
| `MM_ANALYZE_FETCH_S` | Seconds an analyze job spends fetching its input audio (default 20, 5..300); used by the quote of an on-demand analyze. |
| `MM_POD_BACKEND` | `0` (default) or `1`: build the bulk pod backend `runpod-pod`. It stays disarmed until the operator arms it, and `runpod-pod.env` (the pod key) must exist during a bulk window only. Needs `MM_POD_IMAGE` and `MM_POD_GPU_TYPES`. |
| `MM_POD_IMAGE` | The worker image of a pod, `<ref>@sha256:<64 hex>` (the digest the serverless endpoint runs). Required with `MM_POD_BACKEND=1`; a tag is refused. |
| `MM_POD_GPU_TYPES` | Ordered RunPod catalog GPU type ids a pod may use (1 to 4, comma separated, the cheapest 24 GB type first). Required with `MM_POD_BACKEND=1`. The pod quote prices the most expensive listed type. |
| `MM_POD_MIN_TAKES` | Smallest pod batch in takes (default 30, 10..100): a smaller batch is not worth a boot. |
| `MM_POD_MAX_TAKES` | Largest pod batch in takes (default 100, 30..100); never below `MM_POD_MIN_TAKES`. |
| `MM_POD_BOOT_S` | Seconds a pod may take to boot before it is deleted (default 1200, 300..3600). With the backend on, `MM_JOB_TTL` must be at least this plus 10 minutes. |
| `MM_POD_IDLE_S` | Seconds an idle pod waits for a take before it is deleted (default 120, 30..600). |
| `MM_POD_SLACK_S` | Slack seconds added to a pod's reserved lifetime (default 300, 60..1800). |
| `MM_POD_MAX_RUNTIME` | Longest a pod may live from its first create, a duration (default `6h`, `1h..12h`); the pod's own absolute deadline. |
| `MM_POD_DEADMAN` | How long a pod may go without a successful poll before it terminates itself (default `10m`, `2m..30m`). |
| `MM_POD_ORPHAN_AFTER` | How long after its first poll a silent pod is considered orphaned and deleted by the server (default `180s`, `60s..900s`). |
| `MM_POD_WINDOW_MAX` | Longest a bulk window stays armed, from arming (default `8h`, `1h..24h`); the backend also disarms itself after 30 minutes with no pod batch in progress. |
| `MM_POD_DISK_GB` | Container disk of a pod in GB (default 40, 20..200). |
| `MM_FALLBACK_AFTER_S` | Seconds of upload transport failures after which the worker switches to the upload fallback bucket (default 120, 60..600). Only used when `r2.env` is configured. |
| `MM_NTFY_URL` | Optional ntfy topic URL for notifications; `https://` unless the host is loopback, no credentials or query. Optional secret file `ntfy_token` holds the publisher token. |
| `MM_RATE_SCALE` | Multiplier (1..1000, default 1) for every rate limit; used by the E2E setup. |
| `MM_TOKEN` | Access token for `metamusic mcp-stdio`. |
| `MM_INBOX` | Read-only directory scanned by `metamusic import` (default `<MM_DATA_DIR>/inbox`). |
| `MM_FFMPEG` | ffmpeg binary (default `ffmpeg`, resolved on `PATH`). |
| `MM_FLAC` | flac binary (default `flac`). |
| `MM_METAFLAC` | metaflac binary (default `metaflac`). |
| `MM_CLIP_CACHE_MB` | Size cap of the rendered-clip cache in MiB (default 4096, minimum 256). See the runbook. |
| `MM_BACKEND` | Generation backend: `none` (default), `fake` (local simulation, no spend) or `runpod`. See the runbook. |
| `MM_RUNNER` | Job runner on/off (default on; `serve --runner=false` overrides). |
| `MM_FAKE_BACKEND` | With `MM_BACKEND=fake` only: `fast` or `slow:<duration>`. |
| `MM_RUNPOD_RECORD_DIR` | Optional existing directory outside `MM_DATA_DIR` for recorded provider fixtures. |
| `MM_TRANSFORM_ENABLED` | CPU sound transforms on/off (default on). Off makes the transform operations answer 503. |
| `MM_TRANSFORM_QUEUE_MAX` | Most live (queued plus running) transforms at once (default 6, 1..32). |
| `MM_TRANSFORM_MAX_CPU_S` | Per-transform CPU ceiling in seconds (default 600, 30..3600). |
| `MM_TRANSFORM_MAX_OUT_S` | Longest transform output in seconds (default 900, 60..3600). |
| `MM_TRANSFORM_MAX_MEM_MB` | Memory estimate ceiling of one transform in MiB (default 1024, 256..8192). |
| `MM_TRANSFORM_AGENT_DAILY_CPU_S` | Rolling 24 h transform CPU pool shared by all agent tokens, in seconds (default 1800, 0..86400; 0 means agents cannot transform). |
| `MM_TRANSFORM_HUMAN_DAILY_CPU_S` | Rolling 24 h transform CPU pool of the operator, in seconds (default 14400, 0..86400). |
| `MM_SOUNDFONTS` | Score renders only: `name=/abs/file.sf2[;name=/abs/file.sf3]` (at most 8, names `^[a-z][a-z0-9_-]{0,31}$`); empty disables score renders. Files are read-only and never copied. |
| `MM_FLUIDSYNTH` | Score renders only: absolute path of the fluidsynth binary (default: `PATH` lookup). |
| `MM_E2E` | `1` enables the E2E-only test hooks; never set in production. |
| `MM_OIDC_ISSUER` | Issuer URL of the OpenID Connect provider for hosted connectors (claude.ai, ChatGPT); unset disables them. Needs an `https://` `MM_PUBLIC_URL`, `MM_OIDC_CLIENTS` and `MM_USERS`. See `docs/connectors.md`. |
| `MM_OIDC_CLIENTS` | Allow-listed connector clients as `<client_id>=<label>[,...]`; only these may connect. |
| `MM_STEPUP_OIDC_CLIENT` | `MM_AUTH=proxy` only. Client id of the OIDC client the app signs in with to demand a fresh second factor (ID token `amr`, `auth_time`) before any `/human/` call; needs `MM_OIDC_ISSUER`. Optional secrets file `stepup_client_secret` (else a public PKCE client). Unset: the edge's two-factor rule is the only gate. See `docs/connectors.md`. |
| `MM_OIDC_JWKS_URL` | Optional override of the provider's signing-key URL (default: discovered from the issuer). |
| `MM_CALLBACK_BACKLOG_MAX` | Agent spend pacing: an agent's estimate or create is refused (409 `CALLBACKS_BACKLOG`) while this many callbacks of one lineage root await a human verdict (default 6, 0..50, 0 disables). Humans are exempt. |
| `MM_ACESTEP` | `1` enables the second engine (ACE-Step 1.5): its registry entry and backend (`fake` under `MM_BACKEND=fake`; RunPod under `MM_BACKEND=runpod` when `RUNPOD_ACESTEP_ENDPOINT_ID` is in `runpod.env`). Default `0`. Needs `MM_BACKEND` `fake` or `runpod`, else the server refuses to start. The engine also needs an operator adoption before it receives money. |
| `MM_ACESTEP_COLD_START_S` | Cold-start allowance of the ACE-Step backend in quotes and queue-delay estimates (default 300, 30..900; the first engine's is `MM_COLD_START_S`). |
| `MM_ACESTEP_MAX_RUNTIME` | Timeout of the ACE-Step endpoint and cap of its takes' `timeout_s` (default `MM_JOB_MAX_RUNTIME`, 30s..45m). |
| `MM_ACESTEP_TASKS` | Task classes the ACE-Step endpoint is provisioned for: `turbo` (default) or `turbo,base`. |
| `MM_ACESTEP_POOLS` | RunPod pool ids the ACE-Step endpoint is provisioned on (default `AMPERE_24,ADA_24`; comma list of `[A-Z0-9_]{2,40}`). |
| `MM_ACESTEP_CANARY_CAPS` | Comma list drawn from `reference,repaint,extend,complete,extract,lrc`: capabilities the real-GPU canary may exercise before `features.verified.json` lists them. Offered to canary batches only (request ids `mm-canary-<YYYYMMDD>-...`); every other request, and every read-only surface, still sees the verified set. `complete` and `extract` also need `MM_ACESTEP_TASKS=turbo,base`. Needs `MM_ACESTEP=1`. Set only for the canary window. Default off. |
| `MM_E2E_ACESTEP_FEATURES` | Test servers only: `all` (with `MM_E2E=1`) offers every ACE-Step capability without a canary. Refused at config load unless `MM_BACKEND=fake`. |
| `MM_OIDC_SCOPES` | Scopes advertised and required for connectors (default `openid metamusic`; must contain `metamusic`). |

Agents authenticate with scoped personal access tokens minted by the CLI
(`metamusic token create --name N --scopes read,write`; tokens expire after 90 days unless `--ttl 7d`, `--ttl 720h` or `--ttl never` says otherwise).

## Privacy

This repository is public. Audio, databases, PDFs, lyrics, secrets and real deployment hostnames
must never be committed; `make gate` (also run by the git hooks and CI) enforces this.

## Licence

[MIT](LICENSE)

Third-party data: the `binaural` transform embeds a derived head-related impulse response table
(`internal/dsp/hrtf/table.q15`) built from the SADIE II database (University of York, Apache-2.0). Notice and credit:
`internal/dsp/hrtf/NOTICE` and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

Third-party engine and models (ACE-Step worker image, not part of the Go binary):

| Component | Licence | Notes |
|---|---|---|
| SADIE II head-related impulse responses | Apache-2.0 | University of York; derived table in `internal/dsp/hrtf/`; credit in `internal/dsp/hrtf/NOTICE` |
| ACE-Step 1.5 (code and model weights) | MIT | code MIT, weights MIT per the model cards; pinned commit in `worker/contract/ACESTEP_COMMIT`; notices in `worker/NOTICE.acestep` |
| Qwen3 (text encoder and language-model base inside the ACE-Step bundle) | Apache-2.0 | Alibaba Cloud; licence text in `worker/licenses/`; credited in `worker/NOTICE.acestep` |
