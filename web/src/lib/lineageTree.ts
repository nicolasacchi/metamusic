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

// E2: engine badges and the reference edge. The edge is computed from the node's `reference` (recipe.ref), never stored.
export interface EngineNodeLike {
  id: string;
  title: string;
  engine?: string;
  cross_engine?: boolean;
  stems?: number;
  reference?: { track_id: string; from_s: number; to_s: number };
}

export interface NodeBadge {
  kind: "engine" | "cross" | "stems";
  text: string;
  title: string;
}

const ENGINE_LABELS: Record<string, string> = { yue2: "YuE2", acestep15: "ACE-Step", dsp: "DSP" };

/** Badges of a node: the engine unless it is the default (absent or yue2), cross-engine edits, and a stems chip. */
export function nodeBadges(n: EngineNodeLike): NodeBadge[] {
  const out: NodeBadge[] = [];
  if (n.engine && n.engine !== "yue2") out.push({ kind: "engine", text: ENGINE_LABELS[n.engine] ?? n.engine, title: "Rendered by this engine" });
  if (n.cross_engine) out.push({ kind: "cross", text: "cross engine", title: "Edited from a take made by another engine" });
  if (n.stems && n.stems > 0) out.push({ kind: "stems", text: `${n.stems} stem${n.stems === 1 ? "" : "s"}`, title: "Stems extracted from this take" });
  return out;
}

export interface ReferenceEdge {
  fromId: string;
  toId: string;
  /** The referenced take is among the loaded nodes. */
  inTree: boolean;
  label: string;
}

/** Dashed reference edges (reference take, then the render that followed it), in node order. */
export function referenceEdges(nodes: readonly EngineNodeLike[]): ReferenceEdge[] {
  const ids = new Map(nodes.map((n) => [n.id, n.title]));
  const r1 = (n: number) => Math.round(n * 10) / 10;
  return nodes.flatMap((n) =>
    n.reference
      ? [{ fromId: n.reference.track_id, toId: n.id, inTree: ids.has(n.reference.track_id), label: `${ids.get(n.reference.track_id) ?? "reference take"} ${r1(n.reference.from_s)}–${r1(n.reference.to_s)} s` }]
      : [],
  );
}
