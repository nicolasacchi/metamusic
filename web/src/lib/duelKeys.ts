// Keyboard map for the Duel page (C54.2).
import type { Slot } from "./ab";

export type Verdict = "a" | "b" | "tie" | "both_bad";

export type KeyActionResult =
  | { type: "select"; slot: Slot }
  | { type: "toggle" }
  | { type: "playpause" }
  | { type: "restart" }
  | { type: "choose"; verdict: Verdict }
  | { type: "submit" };

export interface KeyLike {
  key: string;
  repeat: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
  target: unknown;
  preventDefault(): void;
}

const FORM_TAGS = new Set(["INPUT", "TEXTAREA", "SELECT"]);

function isFormTarget(t: unknown): boolean {
  if (typeof t !== "object" || t === null) return false;
  const { tagName, isContentEditable } = t as { tagName?: unknown; isContentEditable?: unknown };
  return (typeof tagName === "string" && FORM_TAGS.has(tagName.toUpperCase())) || isContentEditable === true;
}

/** Maps a key press to an action, or null when it is not ours; handled keys call preventDefault. */
export function keyAction(e: KeyLike, ctx: { chosen: Verdict | null }): KeyActionResult | null {
  if (e.repeat || e.ctrlKey || e.metaKey || e.altKey || isFormTarget(e.target)) return null;
  let r: KeyActionResult | null = null;
  switch (e.key) {
    case "a":
    case "A":
      r = { type: "select", slot: "a" };
      break;
    case "b":
    case "B":
      r = { type: "select", slot: "b" };
      break;
    case " ":
      r = { type: "toggle" };
      break;
    case "p":
    case "P":
      r = { type: "playpause" };
      break;
    case "r":
    case "R":
      r = { type: "restart" };
      break;
    case "1":
      r = { type: "choose", verdict: "a" };
      break;
    case "2":
      r = { type: "choose", verdict: "b" };
      break;
    case "3":
      r = { type: "choose", verdict: "tie" };
      break;
    case "4":
      r = { type: "choose", verdict: "both_bad" };
      break;
    case "Enter":
      if (ctx.chosen) r = { type: "submit" };
      break;
  }
  if (r) e.preventDefault();
  return r;
}
