// Pure job/batch state helpers for the Jobs, Job and Batch pages (C84.3).
import type { components } from "../api/schema";

export type JobState = components["schemas"]["JobSummary"]["state"];
export type BatchState = components["schemas"]["Batch"]["state"];
export type Tone = "idle" | "busy" | "ok" | "bad" | "warn";
/** Who is looking. The SPA is only ever used by a human or a device; the agent face never shows this UI. */
export interface Viewer {
  kind: "human" | "device" | "agent";
}

export const JOB_STATES: readonly JobState[] = [
  "awaiting_confirmation",
  "queued",
  "submitting",
  "starting",
  "generating",
  "transferring",
  "ingesting",
  "cancelling",
  "failing",
  "completed",
  "failed",
  "cancelled",
  "expired",
];

const TERMINAL: ReadonlySet<JobState> = new Set<JobState>(["completed", "failed", "cancelled", "expired"]);

export const LIVE_STATES: readonly JobState[] = JOB_STATES.filter((s) => !TERMINAL.has(s));

const LABELS: Record<JobState, string> = {
  awaiting_confirmation: "Awaiting approval",
  queued: "Queued",
  submitting: "Submitting",
  starting: "Starting",
  generating: "Generating",
  transferring: "Transferring",
  ingesting: "Ingesting",
  cancelling: "Terminating",
  failing: "Failing",
  completed: "Completed",
  failed: "Failed",
  cancelled: "Cancelled",
  expired: "Expired",
};

export function stateLabel(s: JobState): string {
  return LABELS[s];
}

export function stateTone(s: JobState): Tone {
  switch (s) {
    case "completed":
      return "ok";
    case "failed":
    case "failing":
      return "bad";
    case "cancelled":
    case "expired":
    case "cancelling":
      return "warn";
    case "queued":
    case "awaiting_confirmation":
      return "idle";
    default:
      return "busy";
  }
}

export function isTerminal(s: JobState): boolean {
  return TERMINAL.has(s);
}

/** Terminate is offered on every non-terminal job (cancelling included: cancel is idempotent) to a human or device. */
export function canCancel(job: { state: JobState }, viewer: Viewer): boolean {
  return viewer.kind !== "agent" && !TERMINAL.has(job.state);
}

export function canCancelBatch(batch: { state: BatchState }, viewer: Viewer): boolean {
  return viewer.kind !== "agent" && (batch.state === "active" || batch.state === "awaiting_confirmation");
}

export function isLive(jobs: readonly { state: JobState }[]): boolean {
  return jobs.some((j) => !TERMINAL.has(j.state));
}

export const POLL_MS = 5000;

/** Delay before the next list_jobs poll, or null when no job is live (polling stops). */
export function pollDelayMs(jobs: readonly { state: JobState }[]): number | null {
  return isLive(jobs) ? POLL_MS : null;
}

export interface FollowDeps<J extends { state: JobState }> {
  initial: J;
  /** wait_for_job(since_state); null on failure. */
  wait: (since: JobState) => Promise<J | null>;
  onJob: (j: J) => void;
  onError?: () => void;
  /** False once the page is unmounted. */
  active: () => boolean;
}

/** Long-polls until the job is terminal, the page is unmounted or a wait fails. */
export async function followJob<J extends { state: JobState }>(d: FollowDeps<J>): Promise<void> {
  let cur = d.initial;
  while (d.active() && !TERMINAL.has(cur.state)) {
    const next = await d.wait(cur.state);
    if (!d.active()) return;
    if (next === null) return void d.onError?.();
    cur = next;
    d.onJob(next);
  }
}

/** The Ledger tab fetches its first page once: an empty ledger must not trigger a refetch loop. */
export function shouldLoadLedger(tab: string, loaded: boolean): boolean {
  return tab === "ledger" && !loaded;
}
