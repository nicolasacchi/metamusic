// Plain-language rendering of the mm.pick/1 evidence of a Pick (C100.6).

function num(v: unknown): number | null {
  return typeof v === "number" && Number.isFinite(v) ? v : null;
}

/** One line explaining why a take is the Pick. Unknown or malformed evidence yields "". */
export function pickEvidenceText(evidence: unknown): string {
  if (typeof evidence !== "object" || evidence === null) return "";
  const e = evidence as Record<string, unknown>;
  let text: string;
  switch (e.rule) {
    case "ladder": {
      const n = num(e.duels_n);
      text = n === null ? "ahead after blind duels" : `ahead after ${n} blind ${n === 1 ? "duel" : "duels"}`;
      break;
    }
    case "stars": {
      const stars = num(e.stars);
      const tied = num(e.tied);
      if (stars === null) text = "by stars";
      else text = tied !== null && tied > 1 ? `${stars}★ (newest of ${tied} tied)` : `${stars}★`;
      break;
    }
    case "newest":
      text = "newest take";
      break;
    case "pinned":
      text = "pinned by you";
      break;
    case "none":
      text = "no take";
      break;
    default:
      return "";
  }
  const pi = e.pin_ineligible;
  if (typeof pi === "object" && pi !== null && typeof (pi as Record<string, unknown>).status === "string") {
    text += ` · pin not eligible (${(pi as Record<string, string>).status})`;
  }
  return text;
}
