import { describe, expect, test } from "bun:test";
import { lineageRows, type LineageNodeLike } from "./lineageTree";

const n = (id: string, parent: string | null, generation: number, kids = 0): LineageNodeLike => ({ id, parent_id: parent, generation, children_count: kids });

// root
// ├─ a
// │  ├─ a1
// │  └─ a2
// │     └─ a2x
// └─ b
const nodes = [n("root", null, 0, 2), n("a", "root", 1, 2), n("a1", "a", 2), n("a2", "a", 2, 1), n("a2x", "a2", 3), n("b", "root", 1)];

describe("lineageRows", () => {
  test("three generations give depth and connectors", () => {
    const rows = lineageRows(nodes);
    expect(rows.map((r) => [r.node.id, r.depth, r.connector])).toEqual([
      ["root", 0, ""],
      ["a", 1, "├─ "],
      ["a1", 2, "│  ├─ "],
      ["a2", 2, "│  └─ "],
      ["a2x", 3, "│     └─ "],
      ["b", 1, "└─ "],
    ]);
  });
  test("collapsing a node hides its subtree and counts it", () => {
    const rows = lineageRows(nodes, new Set(["a"]));
    expect(rows.map((r) => r.node.id)).toEqual(["root", "a", "b"]);
    const a = rows[1];
    expect(a?.collapsed).toBe(true);
    expect(a?.hasChildren).toBe(true);
    expect(a?.hidden).toBe(3);
    expect(rows[0]?.collapsed).toBe(false);
  });
  test("a node whose parent was cut off by truncation is a top-level row", () => {
    const rows = lineageRows([n("x", "missing", 4), n("y", "x", 5)]);
    expect(rows.map((r) => [r.node.id, r.depth, r.connector])).toEqual([
      ["x", 0, ""],
      ["y", 1, "└─ "],
    ]);
  });
  test("children_count above the loaded children still marks hasChildren", () => {
    const rows = lineageRows([n("r", null, 0, 4)]);
    expect(rows[0]?.hasChildren).toBe(true);
    expect(rows[0]?.hidden).toBe(0);
  });
  test("empty", () => {
    expect(lineageRows([])).toEqual([]);
  });
});
