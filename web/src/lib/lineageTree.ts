// Pure: lineage nodes (pre-order, as get_lineage returns them) -> display rows with depth and
// box-drawing connectors (C84.5). Depth comes from the parent chain, not from `generation`, so a tree
// cut by `limit`/the byte cap still renders: a node whose parent is missing becomes a top-level row.

export interface LineageNodeLike {
  id: string;
  parent_id: string | null;
  generation: number;
  children_count: number;
}

export interface LineageRow<N extends LineageNodeLike> {
  node: N;
  depth: number;
  /** Indentation + branch glyph, e.g. "│  └─ " (empty for a top-level row). */
  connector: string;
  hasChildren: boolean;
  collapsed: boolean;
  /** Descendants hidden by collapsing this row (0 when expanded). */
  hidden: number;
}

export function lineageRows<N extends LineageNodeLike>(nodes: readonly N[], collapsed: ReadonlySet<string> = new Set()): LineageRow<N>[] {
  const ids = new Set(nodes.map((x) => x.id));
  const kids = new Map<string, N[]>();
  const tops: N[] = [];
  for (const x of nodes) {
    if (x.parent_id !== null && ids.has(x.parent_id) && x.parent_id !== x.id) {
      const list = kids.get(x.parent_id);
      if (list) list.push(x);
      else kids.set(x.parent_id, [x]);
    } else tops.push(x);
  }
  const rows: LineageRow<N>[] = [];
  const seen = new Set<string>();

  function size(x: N): number {
    let c = 0;
    for (const k of kids.get(x.id) ?? []) c += 1 + size(k);
    return c;
  }
  function walk(x: N, depth: number, indent: string, last: boolean, top: boolean) {
    if (seen.has(x.id)) return; // defensive: a cycle never loops
    seen.add(x.id);
    const children = kids.get(x.id) ?? [];
    const isCollapsed = collapsed.has(x.id) && children.length > 0;
    rows.push({
      node: x,
      depth,
      connector: top ? "" : indent + (last ? "└─ " : "├─ "),
      hasChildren: children.length > 0 || x.children_count > 0,
      collapsed: isCollapsed,
      hidden: isCollapsed ? size(x) : 0,
    });
    if (isCollapsed) return;
    const next = top ? "" : indent + (last ? "   " : "│  ");
    children.forEach((k, i) => walk(k, depth + 1, next, i === children.length - 1, false));
  }
  for (const t of tops) walk(t, 0, "", true, true);
  return rows;
}
