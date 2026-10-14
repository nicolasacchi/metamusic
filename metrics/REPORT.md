# Loop metrics report

Generated 2026-10-13T11:36:42Z from 422 rows (0 malformed skipped).

| target | runs | failures | median ms (warm, ok) | p90 ms (warm, ok) | compile errors | failing tests |
|---|---:|---:|---:|---:|---:|---:|
| build | 1 | 0 | 35701 | 35701 | 0 | 0 |
| check | 205 | 81 | 37243 | 106629 | 16 | 64 |
| cover | 16 | 7 | 134194 | 157190 | 0 | 0 |
| docker | 4 | 0 | 30263 | 63997 | 0 | 0 |
| e2e | 56 | 20 | 98503 | 128735 | 0 | 29 |
| fixture-db | 4 | 0 | 1007 | 1019 | 0 | 0 |
| gate | 70 | 0 | 58750 | 77710 | 0 | 0 |
| lint | 5 | 4 | 1925 | 1925 | 33 | 0 |
| openapi | 20 | 0 | 1465 | 1977 | 0 | 0 |
| routing | 14 | 4 | 12860 | 22311 | 0 | 7 |
| test | 27 | 2 | 95536 | 143298 | 0 | 2 |

## M1 integration notes (M1-X1)
Merged-data coverage (make cover): internal/... 89.3% (gate 85%), internal/auth 95.7% (95%), internal/media/serve 96.6% (95%). make e2e: 120 passed (chrome, pixel7, iphone15, firefox), about 1.2 min warm. CI cold-tools wall times are recorded by the first CI run on the tools cache.

## M2a integration notes (M2a-X1)
All lane branches were already merged into m2a; X1 verified `make tools hooks check test e2e cover gate routing` and `make docker` on m2a at 9a13c43, with no `STUB(M2a-` markers left in internal/ (one comment in `internal/mcpgen/parity_test.go` mentions the old marker; `stubCases` is empty). `make openapi` produced no diff (openapi.json, schema.d.ts and the tools/list golden were already current).
Measured on the build host (warm): `make check` 116 s on its first run after the merge (cold-ish cache) (the M1 median of 37 s is not met; the Go suite grew with the gate, export, backup and servertest packages, with servertest 20 s and store 32 s as the long poles), `make test` 2:19, `make e2e` 2:03 for 196 tests across chrome, chrome-pixel7, chrome-iphone15 and firefox (target 5 min met), `make cover` about 2.3 min, `make gate` about 1 min, `make docker` 21 s.
The tools/list golden is 85917 bytes pretty-printed; `TestToolsList_Ceiling` enforces the compact 40000-byte ceiling and passes.
Known nit: the e2e run logs two 500 `context canceled` lines for `log_listen_events` when the browser aborts a beacon during page unload; the outbox redelivers, no test fails.

## M2b integration notes (M2b-X1)
All lane branches were already merged into m2b; X1 verified `make tools hooks check test e2e cover gate routing` and `make docker` on m2b at 4084c1f plus the X1 test-speed changes. `make openapi` produced no diff.
Measured on the build host (warm, 8 cores): `make check` 36-40 s (the 30 s target is NOT met, as in M1/M2a; the suite is CPU-bound at about 230 CPU-seconds under -race, spread over api, ears, mcpgen, duels and drafts, and per-test setup cost (SQLite open, OpenAPI marshalling) dominates; X1 memoised the fixture gradient JPEG in `earstest` and made the servertest MCP handler lazy, which saved about 4 s), `make test` about 3 min, `make cover` 3:42 (internal/... 89.6%, auth 96.2%, media/serve 97.0%, gate 95.3%, idem 95.3%, ratelimit 98.1%, ladder 99.5%), `make e2e` 2:05 and 2:10 on two consecutive runs, 219 passed and 26 skipped (desktop-only and device-only specs by design), zero flaky, leak list empty, `make gate` ok, `make routing` ok, `make docker` ok.
Ladder benchmark: `BenchmarkFit_500x5000` about 157 ms/op. Spectrogram quality chosen by the budget ladder: q85 for typical material, q65 for short full-band noise (D-M2b-9), always at most 100 000 bytes.
D-M2b-1..22 were copied into the private design document as "M2b amendments".

## M3-B11 notes (acceptance, property, chaos, leaks)
Added (all skip under `-short` except the pure ones, so `make check` is unchanged): `internal/acceptance/m3_test.go` (`TestAcceptance_M3_*` and `TestAcceptance_Interrupt_*`, each interrupt case on both the fake backend and the serverless backend over runpodtest), `m3_principals_test.go`, `m3_canary_test.go` (`TestCanary_ScriptAgainstFake` over the wired server on the real clock), `TestNoSecretLeak_M3`, the extended registry-derived `TestAcceptance_M2a_AgentPerformsEveryUIAction`, and in `internal/jobs` the spend property suite (`property_test.go`: `within`, `adversarial` and RunPod profiles, a test-side ledger model checked after every committed ledger transaction through `spend.Observer`) and the chaos suite (`chaos_test.go`).
Measured wall time on the build host (8 cores, `-race`): the acceptance package 200 s; the property suite at 80 timelines of 160 ticks 10:39 (2317 CPU-s), at 73 timelines of 100 ticks 10:17, and property plus chaos at 26 timelines 5:06. The plan's 60 timelines exceed the 4-minute budget, so the default `propDefaultTimelines` is lowered to 10 (adversarial a third of it, at least 5; RunPod half, at least 4; about 20 timelines, a little over 4 minutes with the chaos suite); `MM_PROPERTY_TIMELINES` raises it (the weekly run).
Findings, in order of weight:
- `jobs.classifySubmit` tested `errors.Is(err, provider.ErrAccess)` before the `EDGE_BLOCKED` code, and both backends' `EDGE_BLOCKED` errors satisfy `ErrAccess`, so a Cloudflare 1010 failed the job `PROVIDER_AUTH` instead of `EDGE_BLOCKED` (the unit tests used an error shape that is not an `ErrAccess`). Fixed in `internal/jobs/submit.go` (one reordering); `TestChaos_SubmitErrors*` pin it on both backends.
- A batch whose activity crosses a calendar-month end can never be reconciled: C76.5 step 4 forbids a window that crosses the month and requires every batch created before `B` to be quiet at `B`, so at the month boundary `B` never qualifies and the window start never advances; the batch, and every later one, stays outstanding (conservative for the caps, and the stalled push names it after 72 h). Pinned by `TestBilling_BatchStraddlingMonthEndWaits`; the property suite skips its end-state assertions for such a timeline. Needs a design decision (for example: close the pre-boundary window at the month boundary over the batches quiet by then and carry a straddling batch's cost into the following window).
- The fake backend's `cancel_ignored=N` ignores the first cancel only (the re-issue is honoured) and counts polls, and the simulated worker stops itself at its next start poll once the attempt is closed; the plan's "stays cancelling until the 5-minute alert" is therefore tested on the scripted provider (`TestChaos_CancelIgnored/scripted`).
- Coverage on unit data alone (`go test -race -cover`): spend 95.7 %, upload 95.1 %, money 97.4 % (edge tests added), jobs 93.2 % against the 95 % threshold: about 190 uncovered statements, nearly all `return err` branches of storage failures in `billing.go`, `cancel.go`, `lease.go`, `transfer.go`, `poll.go` and `ingest.go`; `TestChaos_StorageFaults` (a random failing call per tick) adds only 0.2 points, so closing the gap takes per-phase fault sweeps (`failCtl`) for the cancel, billing and adoption paths.
