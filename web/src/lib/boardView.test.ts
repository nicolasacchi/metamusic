import { describe, expect, test } from "bun:test";
import { MAX_TAKES } from "./genIntent";
import {
  buildGrid,
  crateFromSelection,
  recordFromSelection,
  selectedSongIds,
  draftsForEstimate,
  mergeBoards,
  emptySelection,
  filtersFromSearch,
  filtersToQuery,
  filtersToSearch,
  rangeSelect,
  selectAllDrafts,
  toggleChip,
  type BoardFilters,
} from "./boardView";
import { bigBoard, board } from "./fixtures/l1";

describe("buildGrid", () => {
  const g = buildGrid(board);
  test("columns keep the server order with (none) last", () => {
    expect(g.columns.map((c) => c.angle)).toEqual(["remake-long", "acoustic", null]);
    expect(g.columns.map((c) => c.label)).toEqual(["remake-long", "acoustic", "(none)"]);
  });
  test("Muse rows come first and the Unassigned row is last", () => {
    expect(g.rows.map((r) => r.muse?.id ?? null)).toEqual(["muse-a", "muse-b", null]);
    expect(g.rows.at(-1)!.label).toBe("Unassigned");
    expect(g.rows[0]!.label).toBe("Synthetic muse A");
  });
  test("cells are aligned with the columns, by angle", () => {
    const a = g.rows[0]!;
    expect(a.cells.map((c) => c.chips.map((x) => x.id))).toEqual([["song-3", "song-4", "song-5"], ["song-1", "song-2"], []]);
    const b = g.rows[1]!;
    expect(b.cells.map((c) => c.chips.length)).toEqual([0, 0, 1]);
  });
  test("a row's chip order is its cells in column order", () => {
    expect(g.rows[0]!.chipIds).toEqual(["song-3", "song-4", "song-5", "song-1", "song-2"]);
  });
  test("an angle missing from columns still gets a column, before (none)", () => {
    const b = structuredClone(board);
    b.rows[1]!.cells.push({ angle: "demo", chips: [] });
    b.rows[1]!.cells[1]!.chips.push({ ...b.rows[1]!.cells[0]!.chips[0]!, id: "x" });
    const cols = buildGrid(b).columns.map((c) => c.angle);
    expect(cols).toEqual(["remake-long", "acoustic", "demo", null]);
  });
  test("a 600-chip model builds in under 50 ms", () => {
    const big = bigBoard(600);
    buildGrid(big);
    const t = performance.now();
    const m = buildGrid(big);
    expect(performance.now() - t).toBeLessThan(50);
    expect(m.rows.reduce((a, r) => a + r.chipIds.length, 0)).toBe(600);
  });
});

describe("selection", () => {
  const g = buildGrid(board);
  const rowA = g.rows[0]!;
  test("toggle adds and removes and moves the anchor", () => {
    let s = toggleChip(emptySelection(), "song-1");
    expect([...s.ids]).toEqual(["song-1"]);
    expect(s.anchor).toBe("song-1");
    s = toggleChip(s, "song-1");
    expect(s.ids.size).toBe(0);
  });
  test("shift-range selects inside the row between anchor and target", () => {
    let s = toggleChip(emptySelection(), "song-4");
    s = rangeSelect(s, rowA, "song-2");
    expect([...s.ids].sort()).toEqual(["song-1", "song-2", "song-4", "song-5"]);
  });
  test("shift-range with an anchor in another row degrades to a toggle", () => {
    let s = toggleChip(emptySelection(), "song-6");
    s = rangeSelect(s, rowA, "song-2");
    expect([...s.ids].sort()).toEqual(["song-2", "song-6"]);
  });
  test("select all drafts picks only drafts", () => {
    const r = selectAllDrafts(g, emptySelection());
    expect([...r.selection.ids].sort()).toEqual(["song-1", "song-4", "song-6", "song-7"]);
    expect(r.note).toBe("");
  });
  test("select all drafts is capped at 50 with a note", () => {
    const r = selectAllDrafts(buildGrid(bigBoard(600)), emptySelection());
    expect(r.selection.ids.size).toBe(MAX_TAKES);
    expect(r.note).toContain(String(MAX_TAKES));
  });
  test("Estimate skips non-draft chips with a note and keeps draft ids in grid order", () => {
    let s = emptySelection();
    for (const id of ["song-3", "song-4", "song-1", "song-2"]) s = toggleChip(s, id);
    const e = draftsForEstimate(g, s);
    expect(e.draftIds).toEqual(["draft-4", "draft-1"]);
    expect(e.skipped).toBe(2);
    expect(e.note).toContain("2");
    expect(draftsForEstimate(g, emptySelection())).toEqual({ draftIds: [], skipped: 0, note: "" });
  });
});

describe("filters <-> query string", () => {
  const f: BoardFilters = { session: "s1", angle: "acoustic", states: ["draft", "picked"], sounds: ["era:90s", "language:it"], q: "a b", muse: "m1" };
  test("round trip with repeated sound= keys", () => {
    const search = filtersToSearch(f);
    expect(search.match(/sound=/g)).toHaveLength(2);
    expect(filtersFromSearch(search)).toEqual(f);
    expect(filtersFromSearch("?" + search)).toEqual(f);
  });
  test("empty filters give an empty string and defaults", () => {
    expect(filtersToSearch(filtersFromSearch(""))).toBe("");
    expect(filtersFromSearch("")).toEqual({ session: "", angle: "", states: [], sounds: [], q: "", muse: "" });
  });
  test("unknown states are dropped and duplicates collapse", () => {
    expect(filtersFromSearch("state=draft&state=bogus&state=draft&sound=x%3Ay&sound=x%3Ay").states).toEqual(["draft"]);
    expect(filtersFromSearch("sound=x%3Ay&sound=x%3Ay").sounds).toEqual(["x:y"]);
  });
  test("the API query uses chips detail and omits empty filters", () => {
    expect(filtersToQuery(filtersFromSearch(""))).toEqual({ detail: "chips" });
    expect(filtersToQuery(f)).toEqual({ detail: "chips", session_id: "s1", angle: "acoustic", state: ["draft", "picked"], sound: ["era:90s", "language:it"], q: "a b", muse_id: "m1" });
  });
});

describe("mergeBoards", () => {
  test("a next page appends new rows and extends the Unassigned row without duplicating chips", () => {
    const a = structuredClone(board);
    const b = structuredClone(board);
    b.rows = [structuredClone(board.rows[2]!)];
    b.rows[0]!.cells[0]!.chips = [{ ...board.rows[2]!.cells[0]!.chips[0]!, id: "song-8", draft_id: "draft-8" }, board.rows[2]!.cells[0]!.chips[0]!];
    b.rows.push({ ...structuredClone(board.rows[1]!), muse: { id: "muse-c", kind: "brief", title: "C", artist: null } });
    const m = mergeBoards(a, b);
    expect(m.rows.map((r) => r.muse?.id ?? "")).toEqual(["muse-a", "muse-b", "", "muse-c"]);
    const un = m.rows.find((r) => r.muse === null)!;
    expect(un.cells.flatMap((c) => c.chips.map((x) => x.id))).toEqual(["song-7", "song-8"]);
    expect(buildGrid(m).rows.at(-1)!.muse).toBeNull();
  });
});

describe("curation entry points (L2)", () => {
  const g = buildGrid(board);
  test("the selected Song ids come in grid order, ids out of view last", () => {
    const sel = { ids: new Set(["song-1", "song-3", "gone"]), anchor: null };
    expect(selectedSongIds(g, sel)).toEqual(["song-3", "song-1", "gone"]);
  });
  test("Add to Crate uses subject Song and every selected id", () => {
    const sel = { ids: new Set(["song-1", "song-2"]), anchor: null };
    expect(crateFromSelection(g, sel)).toEqual({ subject: "song", ids: ["song-1", "song-2"] });
  });
  test("New Record from selection collects the Song ids, capped at 60 with a note", () => {
    const small = recordFromSelection(g, { ids: new Set(["song-1"]), anchor: null });
    expect(small).toEqual({ songIds: ["song-1"], note: "" });
    const many = new Set(Array.from({ length: 75 }, (_, i) => `x${i}`));
    const r = recordFromSelection(g, { ids: many, anchor: null });
    expect(r.songIds.length).toBe(60);
    expect(r.note).toContain("15 left out");
  });
});
