// Bulk pod batches (m6-bulk C398.6): pure helpers for the Generate option, the estimate's pod panel, the Batch pod block and
// the Jobs live pods. Pods are off unless the operator has set up the pod backend; every sentence here says so rather than hide it.
import type { components } from "../api/schema";
import { formatMicroUSD } from "./money";

type Pod = components["schemas"]["PodSummary"];
type PodsStatus = components["schemas"]["PodsStatus"];

/** The least MM_POD_MIN_TAKES can be set to; the server's own minimum (default 30) is the real gate and says so when it refuses. */
export const POD_MIN_TAKES_FLOOR = 10;
/** The most MM_POD_MAX_TAKES can be set to. */
export const POD_MAX_TAKES = 100;

/** What of get_provider_status these helpers read (the generated ProviderStatus satisfies it). */
export interface StatusLike {
  backends: { backend: string; generation_enabled: boolean; armed: boolean; pods?: PodsStatus }[];
}

/** The pod backend of the status: the entry that carries a pods block (only a pod backend that was built has one). */
export function podBackendOf(status: StatusLike | null): StatusLike["backends"][number] | null {
  return status?.backends.find((b) => b.pods !== undefined) ?? null;
}

/** True when the status is known and the server has no pod backend at all. */
export function podsOff(status: StatusLike | null): boolean {
  return status !== null && podBackendOf(status) === null;
}

export type OfferKind = "hidden" | "off" | "disabled" | "disarmed" | "toomany" | "ready";
export interface BulkOffer {
  kind: OfferKind;
  /** The sentence to show (empty when hidden). */
  text: string;
  /** The backend to send as options.backend ("" unless ready). */
  backend: string;
  /** An extra caution under a ready offer. */
  note: string;
}

const OFF_TEXT = "Bulk pod batches are off. They stay off until the operator has set up the pod backend on the server.";

/**
 * What the Generate page says about a bulk pod batch. `allowed` is can("get_provider_status"): only the Owner reads the status,
 * so an Editor or Listener never sees any of it. Below the smallest bulk size nothing is shown.
 */
export function bulkOffer(status: StatusLike | null, takes: number, allowed: boolean): BulkOffer {
  const none = { text: "", backend: "", note: "" };
  if (!allowed || status === null || takes < POD_MIN_TAKES_FLOOR) return { kind: "hidden", ...none };
  const b = podBackendOf(status);
  if (!b) return { kind: "off", ...none, text: OFF_TEXT };
  if (!b.generation_enabled) return { kind: "disabled", ...none, text: "The pod backend is set up but generation on it is disabled. The operator enables it." };
  if (!b.armed) return { kind: "disarmed", ...none, text: "The pod backend is set up but not armed. Arm it first (Spend and provider settings); nothing runs on a pod before that." };
  if (takes > POD_MAX_TAKES) return { kind: "toomany", ...none, text: `A bulk pod batch takes at most ${POD_MAX_TAKES} takes; this one has ${takes}.` };
  const safety = b.pods?.safety;
  const note =
    safety !== undefined && safety !== "verified"
      ? "The self-termination check of this pod image is not verified, so the server will refuse an ordinary pod batch until the operator has run it. A pod canary always waits for the Owner's approval."
      : "";
  return { kind: "ready", text: "", backend: b.backend, note };
}

/** An estimate that priced a pod batch carries the pod block (absent on every other estimate). */
export function isPodEstimate(e: { pod?: { boot_s: number; deadline_s: number } | null }): boolean {
  return e.pod !== undefined && e.pod !== null;
}

/** 45 s, 20 min, 2 h, 1 h 30 min. */
export function durationText(seconds: number): string {
  if (seconds < 90) return `${Math.round(seconds)} s`;
  const min = Math.round(seconds / 60);
  if (min < 60) return `${min} min`;
  const h = Math.floor(min / 60);
  const m = min % 60;
  return m === 0 ? `${h} h` : `${h} h ${m} min`;
}

/** A refused pod estimate in words, or null for a code that is not a pod refusal (the caller falls back to its generic text). */
export function bulkFailureText(code: string, details: Record<string, unknown> | undefined): string | null {
  const d = details ?? {};
  const num = (k: string) => (typeof d[k] === "number" ? (d[k] as number) : null);
  if (code === "BULK_TOO_SMALL") return `A bulk pod batch needs at least ${num("minimum") ?? "more"} takes (this one has ${num("takes") ?? "fewer"}): a pod costs the same to start whatever it runs.`;
  if (code === "BATCH_TOO_LARGE" && d.reason === "pod_runtime") return `This batch does not fit one pod's runtime limit${num("max_runtime_s") !== null ? ` of ${durationText(num("max_runtime_s") as number)}` : ""}: split it.`;
  if (code === "BATCH_TOO_LARGE") return `A bulk pod batch takes at most ${num("limit") ?? POD_MAX_TAKES} takes (this one has ${num("takes") ?? "more"}).`;
  if (code === "GENERATION_DISABLED" && d.reason === "pod_safety_unverified") return "Pods cannot run an ordinary batch yet: the self-termination check of the pod image is not verified.";
  if (code === "BACKEND_UNAVAILABLE") return "Bulk pod batches are off: the operator has not set up the pod backend, or this batch has a kind a pod cannot run.";
  if (code === "OWNER_ONLY" || code === "HUMAN_ONLY") return d.reason === "bulk" ? "Only the Owner, at this page, may run a bulk pod batch." : null;
  return null;
}

/** "$1.50 reserved" or "$1.20 settled of $1.50 reserved". */
export function podCostText(p: Pod): string {
  const reserved = `${formatMicroUSD(p.cost.reserved_micro_usd)} reserved`;
  return p.cost.settled_micro_usd === null ? reserved : `${formatMicroUSD(p.cost.settled_micro_usd)} settled of ${reserved}`;
}

export function livePods(items: Pod[]): Pod[] {
  return items.filter((p) => p.state !== "terminated");
}

export type PodToneName = "idle" | "busy" | "ok" | "bad";
export function podTone(p: Pod): PodToneName {
  if (p.error_code || p.state === "orphaned") return "bad";
  if (p.state === "terminated") return "idle";
  if (p.state === "ready" || p.state === "busy") return "ok";
  return "busy";
}

/** One line: state, last poll and deadline. */
export function podLine(p: Pod, now: Date): string {
  let poll = "no poll yet";
  if (p.last_poll_at) {
    const s = Math.max(0, Math.round((now.getTime() - Date.parse(p.last_poll_at)) / 1000));
    poll = s < 60 ? "last poll just now" : `last poll ${durationText(s)} ago`;
  }
  const state = p.error_code ? `${p.state} (${p.error_code})` : p.state;
  return `${state} · ${poll} · deadline ${p.deadline_at.replace("T", " ").slice(0, 16)} UTC`;
}

export function podAlerts(status: StatusLike | null): string[] {
  return podBackendOf(status)?.pods?.alerts ?? [];
}

/** The distinct error codes the pods of a batch carry. */
export function podErrorCodes(items: Pod[]): string[] {
  return [...new Set(items.flatMap((p) => (p.error_code ? [p.error_code] : [])))];
}
