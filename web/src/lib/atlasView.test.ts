import { describe, expect, test } from "bun:test";
import type { components } from "../api/schema";
import {
  type AtlasFilter,
  axisTicks,
  buildGrid,
  buildModel,
  displayToU,
  filterFromSearch,
  filterToQuery,
  filterToSearch,
  hitTest,
  isHollow,
  legendEntries,
  nearestInDirection,
  panBy,
  pointLines,
  project,
  projectAll,
  rectSelect,
  selectionCap,
  tableRows,
  unproject,
  valueLabel,
  zoomAt,
} from "./atlasView";

type S = components["schemas"];
const tempo: S["AxisInfo"] = { name: "tempo", kind: "numeric", min: 6000, max: 20000, scale: "log2", unit: "BPM" };
const energy: S["AxisInfo"] = { name: "energy", kind: "numeric", min: 0, max: 10000, scale: "linear" };
const family: S["AxisInfo"] = {
  name: "family",
  kind: "categorical",
  categories: [
    { value: "pop", count: 5, lo: 0, hi: 0.25 },
    { value: "rock", count: 3, lo: 0.25, hi: 0.5 },
    { value: "jazz", count: 2, lo: 0.5, hi: 0.75 },
    { value: "(none)", count: 1, lo: 0.75, hi: 1 },
  ],
};
const plot = { left: 40, top: 10, width: 400, height: 300 };
const v1 = { zoom: 1, cx: 0.5, cy: 0.5 };

function pt(id: string, x: number, y: number, extra: Partial<S["Point"]> = {}): S["Point"] {
  return { id, t: id, x, y, c: 0, st: 1, pk: 0, ...extra };
}

describe("axis transforms", () => {
  test("60 BPM is the left edge, 200 the right, log in between", () => {
    expect(displayToU(tempo, 60)).toBe(0);
    expect(displayToU(tempo, 200)).toBe(1);
    const mid = displayToU(tempo, Math.sqrt(60 * 200));
    expect(mid).toBeCloseTo(0.5, 6);
    expect(displayToU(tempo, 30)).toBe(0); // clamped
    expect(displayToU(tempo, 999)).toBe(1);
  });
  test("linear axes", () => {
    expect(displayToU(energy, 0)).toBe(0);
    expect(displayToU(energy, 0.5)).toBeCloseTo(0.5, 9);
    expect(displayToU(energy, 1)).toBe(1);
  });
});

describe("ticks", () => {
  test("tempo log ticks", () => {
    const t = axisTicks(tempo);
    expect(t.map((x) => x.label)).toEqual(["60", "80", "100", "120", "160", "200"]);
    expect(t[0]!.u).toBe(0);
    expect(t[5]!.u).toBe(1);
    expect(t.every((x, i) => i === 0 || x.u > t[i - 1]!.u)).toBe(true);
  });
  test("energy 0 to 1", () => {
    const t = axisTicks(energy);
    expect(t.map((x) => x.label)).toEqual(["0", "0.25", "0.5", "0.75", "1"]);
  });
  test("categorical band labels sit at band centres, (none) last", () => {
    const t = axisTicks(family);
    expect(t.map((x) => x.label)).toEqual(["pop", "rock", "jazz", "(none)"]);
    expect(t[0]!.u).toBeCloseTo(0.125, 9);
    expect(t[3]!.u).toBeCloseTo(0.875, 9);
  });
});

describe("value labels (inverse transform)", () => {
  test("tempo and category", () => {
    expect(valueLabel(tempo, 0)).toBe("60 BPM");
    expect(valueLabel(tempo, 1)).toBe("200 BPM");
    expect(valueLabel(energy, 0.5)).toBe("0.50");
    expect(valueLabel(family, 0.3)).toBe("rock");
    expect(valueLabel(family, 1)).toBe("(none)");
  });
});

describe("view: project, zoom, pan", () => {
  test("project/unproject round trip; y grows upward", () => {
    const [sx, sy] = project(v1, plot, 0, 1);
    expect(sx).toBe(40);
    expect(sy).toBe(10);
    const [u, v] = unproject(v1, plot, ...project(v1, plot, 0.3, 0.8));
    expect(u).toBeCloseTo(0.3, 9);
    expect(v).toBeCloseTo(0.8, 9);
  });
  test("zoomAt keeps the point under the cursor, clamps 1x to 16x", () => {
    const [sx, sy] = project(v1, plot, 0.2, 0.7);
    const z = zoomAt(v1, plot, 4, sx, sy);
    expect(z.zoom).toBe(4);
    const [sx2, sy2] = project(z, plot, 0.2, 0.7);
    expect(sx2).toBeCloseTo(sx, 6);
    expect(sy2).toBeCloseTo(sy, 6);
    expect(zoomAt(z, plot, 100, sx, sy).zoom).toBe(16);
    expect(zoomAt(z, plot, 0.0001, sx, sy)).toEqual(v1);
  });
  test("panBy moves with the finger and cannot leave the map", () => {
    const z = { zoom: 4, cx: 0.5, cy: 0.5 };
    const p = panBy(z, plot, 100, 0);
    expect(p.cx).toBeLessThan(0.5);
    expect(panBy(z, plot, 1e9, -1e9)).toEqual({ zoom: 4, cx: 0.125, cy: 0.125 });
    expect(panBy(v1, plot, 50, 50)).toEqual(v1);
  });
});

describe("hit test", () => {
  const model = buildModel([pt("a", 0.5, 0.5), pt("b", 0.5, 0.52), pt("c", 0.9, 0.1)], { hidePredicted: false });
  const { xs, ys } = projectAll(model, v1, plot);
  const grid = buildGrid(xs, ys, 24);
  test("inside radius hits, nearest wins, empty misses", () => {
    const [ax, ay] = project(v1, plot, 0.5, 0.5);
    expect(hitTest(grid, xs, ys, ax + 3, ay + 3, 12)).toBe(0);
    const [bx, by] = project(v1, plot, 0.5, 0.52);
    expect(hitTest(grid, xs, ys, bx, by - 1, 22)).toBe(1);
    expect(hitTest(grid, xs, ys, 45, 300, 12)).toBe(-1);
  });
  test("a 22 px touch radius reaches what a 6 px one misses", () => {
    const [cx, cy] = project(v1, plot, 0.9, 0.1);
    expect(hitTest(grid, xs, ys, cx + 20, cy, 6)).toBe(-1);
    expect(hitTest(grid, xs, ys, cx + 20, cy, 22)).toBe(2);
    expect(hitTest(grid, xs, ys, cx + 23, cy, 22)).toBe(-1);
  });
  test("radius larger than a cell still finds neighbours cells", () => {
    const [cx, cy] = project(v1, plot, 0.9, 0.1);
    expect(hitTest(grid, xs, ys, cx + 60, cy, 70)).toBe(2);
  });
});

describe("nearest in direction", () => {
  // 5-point plus-shaped fixture around the centre (index 0).
  const m = buildModel([pt("c", 0.5, 0.5), pt("l", 0.2, 0.5), pt("r", 0.8, 0.52), pt("u", 0.5, 0.9), pt("d", 0.48, 0.1)], { hidePredicted: false });
  test("arrow keys walk to the nearest point that way", () => {
    expect(nearestInDirection(m, 0, "left")).toBe(1);
    expect(nearestInDirection(m, 0, "right")).toBe(2);
    expect(nearestInDirection(m, 0, "up")).toBe(3);
    expect(nearestInDirection(m, 0, "down")).toBe(4);
  });
  test("returns -1 at an edge", () => {
    expect(nearestInDirection(m, 1, "left")).toBe(-1);
    expect(nearestInDirection(m, 3, "up")).toBe(-1);
  });
  test("prefers the point in line over a nearer diagonal one", () => {
    const k = buildModel([pt("c", 0.5, 0.5), pt("diag", 0.56, 0.62), pt("line", 0.7, 0.5)], { hidePredicted: false });
    expect(nearestInDirection(k, 0, "right")).toBe(2);
  });
});

describe("rectangle selection", () => {
  const m = buildModel([pt("in", 0.5, 0.5), pt("out", 0.95, 0.05), pt("edge", 0.25, 0.5)], { hidePredicted: false });
  const { xs, ys } = projectAll(m, v1, plot);
  test("inside, outside, and partial overlap decided by the centre", () => {
    const [ex, ey] = project(v1, plot, 0.25, 0.5);
    const rect = { x0: 30, y0: ey - 10, x1: ex + 0.5, y1: ey + 10 }; // right edge just past the centre
    expect(rectSelect(xs, ys, rect)).toEqual([2]);
    const miss = { x0: 30, y0: ey - 10, x1: ex - 0.5, y1: ey + 10 }; // the marker overlaps, the centre does not
    expect(rectSelect(xs, ys, miss)).toEqual([]);
    expect(rectSelect(xs, ys, { x0: 0, y0: 0, x1: 1000, y1: 1000 })).toEqual([0, 1, 2]);
  });
  test("corners in any order", () => {
    const [ix, iy] = project(v1, plot, 0.5, 0.5);
    expect(rectSelect(xs, ys, { x0: ix + 5, y0: iy + 5, x1: ix - 5, y1: iy - 5 })).toEqual([0]);
  });
});

describe("selection cap for New Record", () => {
  test("60 or fewer: no note; beyond: first 60 and a note", () => {
    const ids = Array.from({ length: 60 }, (_, i) => `s${i}`);
    expect(selectionCap(ids)).toEqual({ ids, note: "" });
    const more = selectionCap([...ids, "x", "y"]);
    expect(more.ids).toHaveLength(60);
    expect(more.note).toContain("2 left out");
  });
});

describe("legend palette", () => {
  const legend: S["LegendEntry"][] = Array.from({ length: 14 }, (_, i) => ({ c: i, value: `v${i}`, count: 20 - i }));
  test("rank maps to the palette index, rank >= 12 to other", () => {
    const e = legendEntries(legend);
    expect(e[0]!.cssVar).toBe("var(--atlas-1)");
    expect(e[11]!.cssVar).toBe("var(--atlas-12)");
    expect(e[12]!.cssVar).toBe("var(--atlas-other)");
    expect(e[13]!.cssVar).toBe("var(--atlas-other)");
    expect(e[13]!.value).toBe("v13");
  });
});

describe("markers", () => {
  test("predicted on either axis is hollow; measured, declared, plan are solid", () => {
    expect(isHollow(pt("a", 0, 0, { xb: "m", yb: "m" }))).toBe(false);
    expect(isHollow(pt("a", 0, 0, { xb: "d", yb: "l" }))).toBe(false);
    expect(isHollow(pt("a", 0, 0, { xb: "m", yb: "p" }))).toBe(true);
    expect(isHollow(pt("a", 0, 0, { xb: "p" }))).toBe(true);
    expect(isHollow(pt("a", 0, 0))).toBe(false);
  });
  test("hidePredicted drops hollow points and counts them", () => {
    const pts = [pt("a", 0.1, 0.1, { yb: "p" }), pt("b", 0.2, 0.2, { yb: "m" }), pt("c", 0.3, 0.3)];
    const on = buildModel(pts, { hidePredicted: true });
    expect(on.points.map((p) => p.id)).toEqual(["b", "c"]);
    expect(on.hiddenPredicted).toBe(1);
    const off = buildModel(pts, { hidePredicted: false });
    expect(off.points).toHaveLength(3);
    expect(off.hollow[0]).toBe(1);
  });
  test("dimmed (h = 0) points are flagged", () => {
    const m = buildModel([pt("a", 0, 0, { h: 0 }), pt("b", 0, 0, { h: 1 }), pt("c", 0, 0)], { hidePredicted: false });
    expect([...m.dim]).toEqual([1, 0, 0]);
  });
});

describe("point description and table rows", () => {
  const axes = { x: tempo, y: energy };
  const legend: S["LegendEntry"][] = [{ c: 0, value: "pop", count: 1 }];
  const p = pt("a", 0, 1, { st: 3, pk: 4, xb: "m", yb: "p", t: "Hello" });
  test("tooltip lines name the basis in words", () => {
    const l = pointLines(p, axes);
    expect(l.title).toBe("Hello");
    expect(l.state).toBe("picked");
    expect(l.pick).toBe("pinned");
    expect(l.x).toBe("60 BPM (measured)");
    expect(l.y).toBe("1.00 (predicted)");
  });
  test("table rows mirror the points", () => {
    const [r] = tableRows([p], axes, legend);
    expect(r).toMatchObject({ id: "a", title: "Hello", x: "60 BPM", y: "1.00", basis: "measured / predicted", state: "picked", pick: "pinned", colour: "pop" });
  });
  test("no basis and no pick read as honest words", () => {
    const l = pointLines(pt("n", 0.5, 0.5), axes);
    expect(l.pick).toBe("none");
    expect(l.x).toContain("unknown basis");
  });
});

describe("filter <-> query string", () => {
  const f: AtlasFilter = { x: "tempo", y: "energy", color: "family", dim: true, muse: "m1", session: "s1", states: ["draft", "picked"], sounds: ["family:pop", "family:rock", "mood:dark"], q: "a b", crate: "c1", near: "song1" };
  test("round trip with repeated sound= and state=", () => {
    const s = filterToSearch(f);
    expect(s.match(/sound=/g)).toHaveLength(3);
    expect(filterFromSearch("?" + s)).toEqual(f);
    expect(filterFromSearch(s)).toEqual(f);
  });
  test("defaults produce an empty string and parse back to defaults", () => {
    const d = filterFromSearch("");
    expect(filterToSearch(d)).toBe("");
    expect(d.dim).toBe(false);
    expect(d.sounds).toEqual([]);
  });
  test("unknown states are dropped, duplicates collapsed", () => {
    expect(filterFromSearch("?state=bogus&state=draft&state=draft&sound=a:b&sound=a:b").states).toEqual(["draft"]);
    expect(filterFromSearch("?sound=a:b&sound=a:b").sounds).toEqual(["a:b"]);
  });
  test("API query maps to the op parameters", () => {
    expect(filterToQuery(f)).toEqual({ x: "tempo", y: "energy", color_by: "family", dim: true, muse_id: "m1", session_id: "s1", state: ["draft", "picked"], sound: ["family:pop", "family:rock", "mood:dark"], q: "a b", crate_id: "c1", near_song_id: "song1" });
    expect(filterToQuery(filterFromSearch(""))).toEqual({});
  });
});

describe("performance", () => {
  const mk = (n: number) => Array.from({ length: n }, (_, i) => pt(`s${i}`, (i * 7919 % 10007) / 10007, (i * 104729 % 10009) / 10009, { xb: i % 3 === 0 ? "p" : "m" }));
  test("600-point model builds in < 20 ms", () => {
    const pts = mk(600);
    buildModel(pts, { hidePredicted: false });
    const t = performance.now();
    buildModel(pts, { hidePredicted: false });
    expect(performance.now() - t).toBeLessThan(20);
  });
  test("5 000-point projection plus grid in < 80 ms", () => {
    const m = buildModel(mk(5000), { hidePredicted: false });
    projectAll(m, v1, plot);
    const t = performance.now();
    const { xs, ys } = projectAll(m, { zoom: 3, cx: 0.4, cy: 0.6 }, plot);
    const g = buildGrid(xs, ys, 24);
    hitTest(g, xs, ys, 200, 150, 22);
    expect(performance.now() - t).toBeLessThan(80);
  });
});
