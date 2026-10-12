# metamusic

A self-hosted, single-user "music lab" around YuE2 (an open lyrics-to-song model):
generate, listen, judge, regenerate. The web/mobile UI and LLM agents (REST and MCP) have strict
parity: every capability is one registered API operation, and REST, MCP tools and the SPA client
are all derived from that registry.

One Go binary embeds a Svelte 5 SPA. GPU work runs on RunPod Serverless.

Status: milestone M0 (skeleton). See [docs/plans/2026-10-12-roadmap.md](docs/plans/2026-10-12-roadmap.md)
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

Container defaults: `MM_ADDR=0.0.0.0:8080`, `MM_DATA_DIR=/data`, secrets under `/run/secrets`
(`MM_SECRETS_DIR`). Set `MM_PUBLIC_URL` to the externally visible URL (for example
`https://mm.example.com`). Put a TLS-terminating reverse proxy in front for real use.

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

Agents authenticate with scoped personal access tokens minted by the CLI
(`metamusic token create --name N --scopes read,write`).

## Privacy

This repository is public. Audio, databases, PDFs, lyrics, secrets and real deployment hostnames
must never be committed; `make gate` (also run by the git hooks and CI) enforces this.

## Licence

[MIT](LICENSE)
