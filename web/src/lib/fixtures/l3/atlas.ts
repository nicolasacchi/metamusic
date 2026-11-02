// Typed Atlas fixtures (synthetic values only) for the page tests.
import type { components } from "../../../api/schema";

type S = components["schemas"];

/** `n` Songs on a tempo x energy map; every fifth one is predicted on energy (hollow, hidden by default). */
export function atlasPage(n = 10, extra: Partial<S["Atlas"]> = {}): S["Atlas"] {
  const points: S["Point"][] = Array.from({ length: n }, (_, i) => ({
    id: `song-${i + 1}`,
    t: `Synthetic song ${i + 1}`,
    x: (i % 10) / 10 + 0.05,
    y: Math.floor(i / 10) / 10 + 0.05,
    c: i % 3,
    st: i % 4,
    pk: 0,
    xb: "m",
    yb: i % 5 === 4 ? "p" : "m",
  }));
  const predicted = points.filter((p) => p.yb === "p").length;
  return {
    axes: {
      x: { name: "tempo", kind: "numeric", unit: "BPM", scale: "log2", min: 6000, max: 20000 },
      y: { name: "energy", kind: "numeric", scale: "linear", min: 0, max: 10000 },
    },
    color_by: "state",
    def: "atlas/v1",
    generated_at: "2026-01-01T00:00:00Z",
    guides: [],
    layout_token: "tok",
    legend: [
      { c: 0, value: "draft", count: Math.ceil(n / 3) },
      { c: 1, value: "rendered", count: Math.ceil((n - 1) / 3) },
      { c: 2, value: "judged", count: Math.floor(n / 3) },
    ],
    muses: [],
    next_cursor: null,
    pending: false,
    points,
    taxonomy_version: 1,
    totals: {
      basis: { x: { d: 0, l: 0, m: n, p: 0 }, y: { d: 0, l: 0, m: n - predicted, p: predicted } },
      hit: n,
      missing: { x: 0, y: 0 },
      plotted: n,
      scope: n + 2,
      scope_total: n + 2,
      truncated: false,
    },
    ...extra,
  };
}
