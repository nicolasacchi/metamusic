// Compare page model (C54.5): deltas are b - a, coloured by sign.
import type { components } from "../api/schema";

type Comparison = components["schemas"]["Comparison"];
type Deltas = Comparison["deltas"];

export type Sign = "pos" | "neg" | "zero" | "none";
export type Tone = "good" | "bad" | "neutral";

export interface DeltaRow {
  key: keyof Deltas;
  label: string;
  sign: Sign;
  tone: Tone;
  text: string;
}

const RELATIONS: Record<Comparison["relation"], string> = {
  siblings: "siblings",
  parent_child: "A is the parent of B",
  child_parent: "A is a child of B",
  same_lineage: "same lineage",
  unrelated: "unrelated",
};

// unit, decimals, and whether a higher value is a defect (clipping, silence, loops)
const SPEC: { key: keyof Deltas; label: string; unit: string; dp: number; defect: boolean }[] = [
  { key: "integrated_lufs", label: "Integrated loudness", unit: "LU", dp: 1, defect: false },
  { key: "true_peak_dbtp", label: "True peak", unit: "dB", dp: 1, defect: false },
  { key: "lra_lu", label: "Loudness range", unit: "LU", dp: 1, defect: false },
  { key: "bandwidth_hz", label: "Bandwidth", unit: "Hz", dp: 0, defect: false },
  { key: "duration_s", label: "Duration", unit: "s", dp: 1, defect: false },
  { key: "clipped_s", label: "Clipped", unit: "s", dp: 1, defect: true },
  { key: "internal_silence_s", label: "Internal silence", unit: "s", dp: 1, defect: true },
  { key: "token_loop_span_s", label: "Token loop span", unit: "s", dp: 1, defect: true },
];

function deltaRow(d: Deltas, s: (typeof SPEC)[number]): DeltaRow {
  const v = d[s.key];
  if (v === null) return { key: s.key, label: s.label, sign: "none", tone: "neutral", text: "–" };
  const rounded = Number(v.toFixed(s.dp));
  const sign: Sign = rounded > 0 ? "pos" : rounded < 0 ? "neg" : "zero";
  const tone: Tone = !s.defect || sign === "zero" ? "neutral" : sign === "neg" ? "good" : "bad";
  const text = `${rounded > 0 ? "+" : ""}${rounded.toFixed(s.dp)} ${s.unit}`;
  return { key: s.key, label: s.label, sign, tone, text: sign === "zero" ? `0 ${s.unit}` : text };
}

const counts = (c: { a_wins: number; b_wins: number; ties: number; both_bad: number }) => `${c.a_wins}-${c.b_wins}-${c.ties}-${c.both_bad}`;

export function compareView(c: Comparison) {
  const r = c.recipe_diff;
  const l = c.ladder;
  return {
    relation: RELATIONS[c.relation],
    deltas: SPEC.map((s) => deltaRow(c.deltas, s)),
    flags: { onlyA: c.flags.only_a, onlyB: c.flags.only_b, both: c.flags.both },
    recipe: {
      sameHash: r.same_hash,
      changed: r.changed,
      seed: `${r.seed.a} → ${r.seed.b}`,
      added: r.style_terms.added,
      removed: r.style_terms.removed,
    },
    headToHead: {
      human: `A-B-T-BB ${counts(c.duels.human)} (n=${c.duels.human.n})`,
      blindN: c.duels.human_blind_n,
      critic: `A-B-T-BB ${counts(c.duels.critic)} (n=${c.duels.critic.n})`,
    },
    ladderLine:
      l.p_a_beats_b === null || !l.a || !l.b
        ? "Not enough counted duels to compare on the ladder."
        : `P(A beats B) ${Math.round(l.p_a_beats_b * 100)}%${l.p_tie === null ? "" : `, P(tie) ${Math.round(l.p_tie * 100)}%`} (u ${l.a.u.toFixed(2)} vs ${l.b.u.toFixed(2)})`,
    blindSpots: c.blind_spots,
  };
}
