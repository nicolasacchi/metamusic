// Pure view models for the Duel page and the duel history (C54.3). A playable (open) duel never
// carries gains, loudness, seeds or - when blind - titles, so a template cannot leak them.
import type { components } from "../api/schema";

type Duel = components["schemas"]["Duel"];
type DuelSummary = components["schemas"]["DuelSummary"];
type Verdict = "a" | "b" | "tie" | "both_bad";

export interface PlaySlot {
  key: "a" | "b";
  label: "A" | "B";
  /** Only for non-blind duels. */
  title?: string;
}
export interface PlayView {
  kind: "play";
  id: string;
  blind: boolean;
  loudnessMatched: boolean;
  windowLabel: string;
  aUrl: string;
  bUrl: string;
  slots: [PlaySlot, PlaySlot];
}
export interface RevealRow {
  key: "a" | "b";
  label: "A" | "B";
  title: string | null;
  seed: number | null;
  trackId: string | null;
  gain: string | null;
  lufs: string | null;
  won: boolean;
}
export interface RevealView {
  kind: "reveal";
  id: string;
  outcome: string;
  rows: [RevealRow, RevealRow];
  critics: string[];
  note: string | null;
  compareHref: string | null;
}
export interface ExpiredView {
  kind: "expired";
  id: string;
  notice: string;
}
export type DuelViewModel = PlayView | RevealView | ExpiredView;

export function verdictText(v: Verdict | null | undefined): string {
  switch (v) {
    case "a":
      return "A is better";
    case "b":
      return "B is better";
    case "tie":
      return "Tie";
    case "both_bad":
      return "Both bad";
    default:
      return "No verdict yet";
  }
}

const windowLabel = (w: { duration_s: number }) => `${w.duration_s.toFixed(0)} s window`;

export function duelViewModel(d: Duel): DuelViewModel {
  if (d.state === "expired") return { kind: "expired", id: d.id, notice: "This duel expired." };
  if (d.state === "decided") {
    const verdict = d.human_verdict ?? d.my_verdict;
    const winner = verdict?.winner_track_id ?? null;
    const row = (key: "a" | "b"): RevealRow => {
      const s = d.slots[key];
      return {
        key,
        label: key === "a" ? "A" : "B",
        title: s.track?.title ?? null,
        seed: s.track?.seed ?? null,
        trackId: s.track?.id ?? null,
        gain: s.gain_db === null ? null : `${s.gain_db.toFixed(1)} dB`,
        lufs: s.window_lufs === null ? null : `${s.window_lufs.toFixed(1)} LUFS`,
        won: winner !== null && s.track?.id === winner,
      };
    };
    const a = d.slots.a.track?.id;
    const b = d.slots.b.track?.id;
    return {
      kind: "reveal",
      id: d.id,
      outcome: verdictText(verdict?.verdict),
      rows: [row("a"), row("b")],
      critics: d.critic_verdicts.map((c) => `Critic: ${c.verdict}${c.note ? ` - ${c.note}` : ""}`),
      note: verdict?.note ?? null,
      compareHref: a && b ? `/compare?a=${encodeURIComponent(a)}&b=${encodeURIComponent(b)}` : null,
    };
  }
  const slot = (key: "a" | "b"): PlaySlot => {
    const title = !d.blind ? d.slots[key].track?.title : undefined;
    return { key, label: key === "a" ? "A" : "B", ...(title !== undefined ? { title } : {}) };
  };
  return {
    kind: "play",
    id: d.id,
    blind: d.blind,
    loudnessMatched: d.loudness_matched,
    windowLabel: windowLabel(d.window),
    aUrl: d.slots.a.clip.url,
    bUrl: d.slots.b.clip.url,
    slots: [slot("a"), slot("b")],
  };
}

export interface DuelRow {
  id: string;
  href: string;
  title: string;
  state: DuelSummary["state"];
  outcome: string;
  blind: boolean;
  created: string;
}

export function duelSummaryRow(s: DuelSummary): DuelRow {
  const a = s.slots.a.track?.title;
  const b = s.slots.b.track?.title;
  const named = s.revealed && a !== undefined && b !== undefined;
  const outcome = s.state === "expired" ? "Expired" : s.human_verdict ? verdictText(s.human_verdict.verdict) : "Open";
  return {
    id: s.id,
    href: `/duels/${encodeURIComponent(s.id)}`,
    title: named ? `${a} vs ${b}` : s.blind ? "Blind duel" : "Duel",
    state: s.state,
    outcome,
    blind: s.blind,
    created: s.created_at.slice(0, 10),
  };
}

export function duelPage(p: { items: DuelSummary[]; total: number; next_cursor: string | null }) {
  return { rows: p.items.map(duelSummaryRow), total: p.total, nextCursor: p.next_cursor };
}
