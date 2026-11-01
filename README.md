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
| `MM_TRUSTED_USERS` | The single allow-listed user for proxy mode. |
| `MM_BACKUP_DIR` | Backup snapshot directory (default: `backup` next to `MM_DATA_DIR`; must not be inside it). |
| `MM_BACKUP_AT` | Daily snapshot time `HH:MM` in the process time zone (default `03:30`), or `off`. |
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
| `MM_E2E` | `1` enables the E2E-only test hooks; never set in production. |
| `MM_OIDC_ISSUER` | Issuer URL of the OpenID Connect provider for hosted connectors (claude.ai, ChatGPT); unset disables them. Needs an `https://` `MM_PUBLIC_URL`, `MM_OIDC_CLIENTS` and `MM_TRUSTED_USERS`. See `docs/connectors.md`. |
| `MM_OIDC_CLIENTS` | Allow-listed connector clients as `<client_id>=<label>[,...]`; only these may connect. |
| `MM_OIDC_JWKS_URL` | Optional override of the provider's signing-key URL (default: discovered from the issuer). |
| `MM_OIDC_SCOPES` | Scopes advertised and required for connectors (default `openid metamusic`; must contain `metamusic`). |

Agents authenticate with scoped personal access tokens minted by the CLI
(`metamusic token create --name N --scopes read,write`; tokens expire after 90 days unless `--ttl 7d`, `--ttl 720h` or `--ttl never` says otherwise).

## Privacy

This repository is public. Audio, databases, PDFs, lyrics, secrets and real deployment hostnames
must never be committed; `make gate` (also run by the git hooks and CI) enforces this.

## Licence

[MIT](LICENSE)
