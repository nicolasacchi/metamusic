// Receipts (C262, D-M5-R3-5): a web-only grouping of get_audit pages by principal with a 30 minute gap, money from list_batches.
// Scaffold by M5-F2 (the constant only); lane M5-W2 adds the grouping and ReceiptsTable.

/** A new group starts after this much quiet between two audit rows of the same principal. */
export const RECEIPT_GAP_MS = 30 * 60 * 1000;

import type { components } from "../api/schema";
import type { createApi } from "../api/client";
import { failure, type Res } from "./crateApi";

type S = components["schemas"];
type Entry = S["AuditEntry"];
type Batch = S["BatchSummary"];
type Api = ReturnType<typeof createApi>;

export interface ReceiptGroup {
  /** principal id (kind:id) plus the start of the group */
  key: string;
  principal: { id: string; kind: string; name: string };
  from: string;
  to: string;
  entries: Entry[];
  /** operation -> count, most frequent first */
  ops: { op: string; count: number }[];
  created: number;
  failed: number;
  /** settled spend of the batches this principal created in the group's window, integer micro-USD */
  spendMicro: number;
  batchIds: string[];
}

const ms = (iso: string): number => Date.parse(iso);

/**
 * Groups audit rows by principal: a new group starts after `gapMs` of quiet between two rows of the same principal.
 * A batch lands in the group of its creator whose window (widened by the gap) holds its creation time; a batch with
 * no such group becomes a spend-only group, so money is never dropped. Newest group first.
 */
export function groupReceipts(entries: readonly Entry[], batches: readonly Batch[], gapMs: number = RECEIPT_GAP_MS): ReceiptGroup[] {
  const byPrincipal = new Map<string, Entry[]>();
  for (const e of entries) {
    const l = byPrincipal.get(e.principal.id);
    if (l) l.push(e);
    else byPrincipal.set(e.principal.id, [e]);
  }
  const groups: ReceiptGroup[] = [];
  for (const [pid, rows] of byPrincipal) {
    rows.sort((a, b) => ms(a.at) - ms(b.at) || a.id.localeCompare(b.id));
    let cur: Entry[] = [];
    const flush = () => {
      if (cur.length > 0) groups.push(makeGroup(pid, cur));
      cur = [];
    };
    for (const e of rows) {
      const last = cur[cur.length - 1];
      if (last && ms(e.at) - ms(last.at) > gapMs) flush();
      cur.push(e);
    }
    flush();
  }
  for (const b of [...batches].sort((x, y) => ms(x.created_at) - ms(y.created_at))) {
    const t = ms(b.created_at);
    const g = groups.find((x) => x.principal.id === b.created_by.id && t >= ms(x.from) - gapMs && t <= ms(x.to) + gapMs);
    const spend = b.cost.settled_micro_usd;
    if (g) {
      g.spendMicro += spend;
      g.batchIds.push(b.id);
    } else {
      groups.push({ key: `${b.created_by.id}@${b.created_at}`, principal: b.created_by, from: b.created_at, to: b.created_at, entries: [], ops: [], created: 0, failed: 0, spendMicro: spend, batchIds: [b.id] });
    }
  }
  return groups.sort((a, b) => ms(b.to) - ms(a.to) || a.key.localeCompare(b.key));
}

function makeGroup(pid: string, rows: Entry[]): ReceiptGroup {
  const counts = new Map<string, number>();
  for (const e of rows) counts.set(e.op, (counts.get(e.op) ?? 0) + 1);
  return {
    key: `${pid}@${rows[0]!.at}`,
    principal: rows[0]!.principal,
    from: rows[0]!.at,
    to: rows[rows.length - 1]!.at,
    entries: rows,
    ops: [...counts].map(([op, count]) => ({ op, count })).sort((a, b) => b.count - a.count || a.op.localeCompare(b.op)),
    created: rows.reduce((n, e) => n + e.created_ids.length, 0),
    failed: rows.filter((e) => e.status >= 400).length,
    spendMicro: 0,
    batchIds: [],
  };
}

export interface ReceiptsData {
  groups: ReceiptGroup[];
  /** the page cap was hit: older activity exists */
  truncated: boolean;
  /** list_batches failed: the money column is missing, not zero */
  moneyMissing: boolean;
}

const PAGES = 5;

/** get_audit pages (newest first) and list_batches pages, grouped. A failing batch list only drops the money. */
export async function loadReceipts(api: Api): Promise<Res<ReceiptsData>> {
  try {
    const entries: Entry[] = [];
    let cursor: string | undefined;
    let more = false;
    for (let i = 0; i < PAGES; i++) {
      const r = await api.GET("/api/audit", { params: { query: { limit: 200, ...(cursor ? { cursor } : {}) } } });
      if (!r.data) return failure(r.response.status, r.error, "Could not load the audit log");
      entries.push(...r.data.items);
      cursor = r.data.next_cursor ?? undefined;
      more = cursor !== undefined;
      if (!more) break;
    }
    const batches: Batch[] = [];
    let moneyMissing = false;
    let bc: string | undefined;
    for (let i = 0; i < PAGES; i++) {
      const r = await api.GET("/api/batches", { params: { query: { limit: 100, ...(bc ? { cursor: bc } : {}) } } });
      if (!r.data) {
        moneyMissing = true;
        break;
      }
      batches.push(...r.data.items);
      bc = r.data.next_cursor ?? undefined;
      if (!bc) break;
    }
    return { ok: true, value: { groups: groupReceipts(entries, batches), truncated: more, moneyMissing } };
  } catch {
    return { ok: false, status: 0, code: "NETWORK", message: "Could not load the receipts: network error", stale: false };
  }
}
