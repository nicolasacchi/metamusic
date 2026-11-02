// What next (C145.5): grouping of list_next_actions items and the link each item opens. Pure; the component owns the network.
// Texts come from the server and name Muses by the last 8 characters of their id, never titles; they are rendered as text nodes.
import type { components } from "../api/schema";
import { draftsIntent, type GenerationIntent } from "./genIntent";

type Action = components["schemas"]["NextAction"];
type Page = components["schemas"]["NextActionsPage"];

export interface ActionGroup {
  code: string;
  items: Action[];
  /** Items of this code beyond the per-code cap, from the server's groups. */
  more: number;
}

/** Items grouped by code in first-seen (priority) order; `more` comes from the server's groups. */
export function groupActions(page: Pick<Page, "items" | "groups"> | null): ActionGroup[] {
  if (!page) return [];
  const more = new Map(page.groups.map((g) => [g.code, g.more]));
  const out = new Map<string, ActionGroup>();
  for (const it of page.items) {
    let g = out.get(it.code);
    if (!g) {
      g = { code: it.code, items: [], more: more.get(it.code) ?? 0 };
      out.set(it.code, g);
    }
    g.items.push(it);
  }
  return [...out.values()];
}

export const moreText = (n: number): string => (n > 0 ? `${n} more like this` : "");

/** Human-audience items are the ones that wait for the operator. */
export const audienceLabel = (a: Pick<Action, "audience">): string => (a.audience === "human" ? "needs you" : "");

export type HintLink = { kind: "href"; href: string } | { kind: "intent"; href: "/human/generate"; intent: GenerationIntent };

const enc = encodeURIComponent;
const str = (v: unknown): string => (typeof v === "string" && v !== "" ? v : "");
const href = (h: string): HintLink => ({ kind: "href", href: h });

function subjectLink(s: Action["subject"]): HintLink | null {
  const id = s.id ?? "";
  switch (s.kind) {
    case "muse":
      return id ? href(`/muses/${enc(id)}`) : null;
    case "song":
      return id ? href(`/songs/${enc(id)}`) : null;
    case "record":
      return id ? href(`/records/${enc(id)}`) : null;
    case "taxonomy":
      return href("/sounds?tab=proposals");
    default:
      return null;
  }
}

/**
 * Where an item opens. Hints name free ops; each becomes an in-app page. Generation never starts here: a from_draft
 * estimate hands a drafts intent to the full-page /human/generate flow (no /api/ call). Without a usable hint the
 * subject's own page is used.
 */
export function hintLink(a: Pick<Action, "hint" | "subject">): HintLink | null {
  const h = a.hint;
  if (h) {
    const args = h.args ?? {};
    switch (h.op) {
      case "get_muse": {
        const id = str(args.id);
        if (id) return href(`/muses/${enc(id)}`);
        break;
      }
      case "get_song": {
        const id = str(args.id);
        if (id) return href(`/songs/${enc(id)}`);
        break;
      }
      case "get_record": {
        const id = str(args.id);
        if (id) return href(`/records/${enc(id)}`);
        break;
      }
      case "suggest_sequence": {
        const id = str(args.record_id);
        if (id) return href(`/records/${enc(id)}?sequence=arc`);
        break;
      }
      case "list_songs": {
        const id = str(args.muse_id);
        if (id) return href(`/board?muse=${enc(id)}`);
        break;
      }
      case "create_duel":
        return a.subject.kind === "song" && a.subject.id ? href(`/songs/${enc(a.subject.id)}`) : null;
      case "propose_taxonomy":
      case "get_sound_taxonomy":
        return href("/sounds");
      case "estimate_generation": {
        const items = args.items;
        const first: unknown = Array.isArray(items) ? items[0] : undefined;
        if (typeof first === "object" && first !== null && (first as { mode?: unknown }).mode === "from_draft") {
          const ids = (first as { draft_ids?: unknown }).draft_ids;
          const drafts = Array.isArray(ids) ? ids.filter((x): x is string => typeof x === "string" && x !== "") : [];
          if (drafts.length > 0) return { kind: "intent", href: "/human/generate", intent: draftsIntent(drafts) };
        }
        return null;
      }
    }
  }
  return subjectLink(a.subject);
}
