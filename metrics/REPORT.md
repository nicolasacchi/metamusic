# Loop metrics report

Generated 2026-10-12T21:50:28Z from 181 rows (0 malformed skipped).

| target | runs | failures | median ms (warm, ok) | p90 ms (warm, ok) | compile errors | failing tests |
|---|---:|---:|---:|---:|---:|---:|
| build | 1 | 0 | 35701 | 35701 | 0 | 0 |
| check | 81 | 24 | 33035 | 52572 | 8 | 3 |
| cover | 3 | 1 | 48403 | 48403 | 0 | 0 |
| docker | 1 | 0 | 30263 | 30263 | 0 | 0 |
| e2e | 25 | 11 | 39217 | 74416 | 0 | 2 |
| fixture-db | 2 | 0 | 824 | 824 | 0 | 0 |
| gate | 45 | 0 | 56344 | 74746 | 0 | 0 |
| lint | 5 | 4 | 1925 | 1925 | 33 | 0 |
| openapi | 8 | 0 | 1233 | 1415 | 0 | 0 |
| routing | 6 | 0 | 12860 | 20272 | 0 | 0 |
| test | 4 | 0 | 33117 | 45812 | 0 | 0 |

## M1 integration notes (M1-X1)
Merged-data coverage (make cover): internal/... 89.3% (gate 85%), internal/auth 95.7% (95%), internal/media/serve 96.6% (95%). make e2e: 120 passed (chrome, pixel7, iphone15, firefox), about 1.2 min warm. CI cold-tools wall times are recorded by the first CI run on the tools cache.
