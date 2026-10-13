# Loop metrics report

Generated 2026-10-13T04:50:13Z from 285 rows (0 malformed skipped).

| target | runs | failures | median ms (warm, ok) | p90 ms (warm, ok) | compile errors | failing tests |
|---|---:|---:|---:|---:|---:|---:|
| build | 1 | 0 | 35701 | 35701 | 0 | 0 |
| check | 127 | 47 | 37243 | 105269 | 10 | 22 |
| cover | 10 | 5 | 53161 | 134194 | 0 | 0 |
| docker | 3 | 0 | 30263 | 30263 | 0 | 0 |
| e2e | 39 | 16 | 74185 | 122814 | 0 | 22 |
| fixture-db | 3 | 0 | 1007 | 1007 | 0 | 0 |
| gate | 56 | 0 | 56344 | 75908 | 0 | 0 |
| lint | 5 | 4 | 1925 | 1925 | 33 | 0 |
| openapi | 15 | 0 | 1415 | 1806 | 0 | 0 |
| routing | 13 | 4 | 12860 | 20272 | 0 | 7 |
| test | 13 | 0 | 72274 | 127531 | 0 | 0 |

## M1 integration notes (M1-X1)
Merged-data coverage (make cover): internal/... 89.3% (gate 85%), internal/auth 95.7% (95%), internal/media/serve 96.6% (95%). make e2e: 120 passed (chrome, pixel7, iphone15, firefox), about 1.2 min warm. CI cold-tools wall times are recorded by the first CI run on the tools cache.

## M2a integration notes (M2a-X1)
All lane branches were already merged into m2a; X1 verified `make tools hooks check test e2e cover gate routing` and `make docker` on m2a at 9a13c43, with no `STUB(M2a-` markers left in internal/ (one comment in `internal/mcpgen/parity_test.go` mentions the old marker; `stubCases` is empty). `make openapi` produced no diff (openapi.json, schema.d.ts and the tools/list golden were already current).
Measured on the build host (warm): `make check` 116 s on its first run after the merge (cold-ish cache) (the M1 median of 37 s is not met; the Go suite grew with the gate, export, backup and servertest packages, with servertest 20 s and store 32 s as the long poles), `make test` 2:19, `make e2e` 2:03 for 196 tests across chrome, chrome-pixel7, chrome-iphone15 and firefox (target 5 min met), `make cover` about 2.3 min, `make gate` about 1 min, `make docker` 21 s.
The tools/list golden is 85917 bytes pretty-printed; `TestToolsList_Ceiling` enforces the compact 40000-byte ceiling and passes.
Known nit: the e2e run logs two 500 `context canceled` lines for `log_listen_events` when the browser aborts a beacon during page unload; the outbox redelivers, no test fails.
