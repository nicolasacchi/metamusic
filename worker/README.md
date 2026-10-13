# metamusic-worker

RunPod serverless worker for metamusic. It speaks the `mm.worker/1` contract (see
`docs/plans/2026-10-14-m3-generate.md`, C73/C83): it announces itself to the app through a signed start URL,
renders one take with YuE2, uploads the allowlisted artifacts through signed capability URLs and returns a
small JSON output.

Layout: `src/mmworker/` (package, JSON schemas under `schema/`), `tests/` (pytest, no torch, no network).

```
cd worker && uv run pytest --cov=mmworker --cov-fail-under=90
```

Modes (`python -m mmworker`, env `MODE`): `serverless`, `selftest`; `agent` exits 2 (arrives in M6).
The runtime dependency set contains no torch and no yue2: the image installs `yue2-infer` explicitly.
