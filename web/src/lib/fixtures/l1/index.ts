// Typed L1 fixtures (synthetic values only). Hand-maintained JSON; `svelte-check` types them.
import type { components } from "../../../api/schema";
import boardJson from "./board.json";

type S = components["schemas"];

/** Two Muses and the Unassigned row; columns are deliberately out of the display order (the `(none)` column first). */
export const board = boardJson as unknown as S["Board"];

/** A synthetic Board of `n` chips spread over `muses` Muses and three angles (perf and cap tests). */
export function bigBoard(n: number, muses = 200): S["Board"] {
  const angles = ["acoustic", "remake-long", "demo"];
  const rows: S["BoardRow"][] = [];
  const per = Math.max(1, Math.ceil(n / muses));
  let made = 0;
  for (let m = 0; m < muses && made < n; m++) {
    const cells = angles.map((a) => ({ angle: a, chips: [] as S["SongChip"][] }));
    for (let k = 0; k < per && made < n; k++, made++) {
      cells[k % 3]!.chips.push({
        id: `s${made}`,
        title: `Synthetic ${made}`,
        state: made % 2 === 0 ? "draft" : "rendered",
        takes: made % 2,
        pick_source: null,
        pick_take_id: null,
        queued: false,
        draft_id: made % 2 === 0 ? `d${made}` : null,
        tempo_mismatch: false,
      });
    }
    const kept = cells.filter((c) => c.chips.length > 0);
    rows.push({
      muse: { id: `m${m}`, kind: "reference", title: `Muse ${m}`, artist: null },
      songs_total: kept.reduce((a, c) => a + c.chips.length, 0),
      counts: { draft: 0, rendered: 0, judged: 0, picked: 0, queued: 0 },
      cells: kept,
      chips_more: false,
      next_unassigned_cursor: null,
    });
  }
  return {
    generated_at: "2026-01-01T00:00:00.000Z",
    taxonomy_version: 1,
    columns: angles.map((a) => ({ angle: a, songs: Math.ceil(n / 3) })),
    rows,
    totals: { muses, songs: n, draft: 0, rendered: 0, judged: 0, picked: 0, queued: 0 },
    next_cursor: null,
  };
}
