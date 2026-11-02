// Taxonomy proposals (C145.4): impact sentence, decision bodies, result and error text, who may decide. Pure.
import type { components } from "../api/schema";
import type { Viewer } from "./genIntent";
import { FACET_LABELS, type Facet } from "./soundsView";

type Impact = components["schemas"]["TaxonomyImpact"];
type MapEntry = components["schemas"]["MapEntry"];
type DecideResult = components["schemas"]["DecideResult"];
type DecideBody = components["schemas"]["DecideTaxonomyInBody"];

const songs = (n: number): string => `${n} Song${n === 1 ? "" : "s"}`;

/** "14 Songs gain reggae, 2 lose pop; clears 'skank' in 14 Songs". The noun is said once, on the first clause. */
export function impactText(i: Impact): string {
  const clauses: string[] = [];
  let first = true;
  const clause = (n: number, verbOne: string, verbMany: string, what: string) => {
    const head = first ? songs(n) : String(n);
    clauses.push(`${head} ${n === 1 && first ? verbOne : verbMany} ${what}`);
    first = false;
  };
  for (const g of i.gain) clause(g.songs, "gains", "gain", g.value);
  for (const l of i.loss) clause(l.songs, "loses", "lose", l.value);
  let text = clauses.join(", ");
  if (i.unmapped_cleared.length > 0) {
    const cleared = i.unmapped_cleared.map((c) => `'${c.term}' in ${songs(c.songs)}`).join(", ");
    text = (text ? text + "; " : "") + `clears ${cleared}`;
  }
  if (!text) text = "No Songs change";
  if (i.truncated) text += ` (first ${i.songs_scanned} Songs scanned)`;
  return text;
}

/** "add 'skank' → Genre: reggae". */
export function mappingText(m: Pick<MapEntry, "op" | "term" | "facet" | "value">): string {
  return `${m.op} '${m.term}' → ${FACET_LABELS[m.facet as Facet] ?? m.facet}: ${m.value}`;
}

const withNote = (b: DecideBody, note?: string): DecideBody => {
  const n = note?.trim();
  return n ? { ...b, note: n } : b;
};

export const acceptAllBody = (note?: string): DecideBody => withNote({ decision: "accept" }, note);
export const acceptSubsetBody = (idx: ReadonlySet<number>, note?: string): DecideBody =>
  withNote({ decision: "accept", accept_mappings: [...idx].sort((a, b) => a - b) }, note);
export const rejectBody = (note?: string): DecideBody => withNote({ decision: "reject" }, note);

/** The body for "Accept selected": everything selected is "accept all", a subset goes by index, nothing selected is null. */
export function selectionBody(selected: ReadonlySet<number>, total: number, note?: string): DecideBody | null {
  if (selected.size === 0) return null;
  return selected.size >= total ? acceptAllBody(note) : acceptSubsetBody(selected, note);
}

export const STALE_TEXT = "Nothing left to apply — proposal expired";

export function resultText(r: DecideResult): string {
  if (r.proposal.state === "rejected") return "Rejected.";
  const a = r.applied;
  let t = "Accepted.";
  if (a) {
    t = `Applied ${a.mappings_applied} mapping${a.mappings_applied === 1 ? "" : "s"}`;
    t += a.version === null ? " (already effective)" : ` as taxonomy version ${a.version}`;
    t += `; recomputed ${a.recomputed.songs} songs and ${a.recomputed.takes} takes.`;
  }
  if (r.skipped.length > 0) t += ` Skipped ${r.skipped.length} as stale.`;
  return t;
}

export function decideErrorText(code: string | undefined, message: string | undefined): string {
  switch (code) {
    case "TAXONOMY_STALE":
      return STALE_TEXT;
    case "TAXONOMY_PROPOSAL_CLOSED":
      return "This proposal was already decided.";
    case "HUMAN_ONLY":
      return "Only the operator can decide a proposal.";
    default:
      return message || "The decision failed";
  }
}

/** decide_taxonomy_proposal is human-only: agents and devices see the list read-only. */
export const canDecide = (me: Viewer | null): boolean => me !== null && me.kind === "human" && me.scopes.includes("write");
