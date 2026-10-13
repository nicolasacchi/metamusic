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
