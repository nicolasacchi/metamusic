import { describe, expect, test } from "bun:test";
import { JOB_STATES, canCancel, canCancelBatch, followJob, isLive, isTerminal, pollDelayMs, shouldLoadLedger, stateLabel, stateTone, type JobState } from "./jobState";

const TERMINAL: JobState[] = ["completed", "failed", "cancelled", "expired"];

describe("jobState", () => {
  test("every state has a label and a tone", () => {
    for (const s of JOB_STATES) {
      expect(stateLabel(s).length).toBeGreaterThan(0);
      expect(["idle", "busy", "ok", "bad", "warn"]).toContain(stateTone(s));
    }
  });

  test("terminal states are exactly completed, failed, cancelled, expired", () => {
    expect(JOB_STATES.filter(isTerminal).sort()).toEqual([...TERMINAL].sort());
  });

  test("canCancel: terminal => false; non-terminal => true for human and device", () => {
    for (const s of JOB_STATES) {
      for (const kind of ["human", "device"] as const) expect(canCancel({ state: s }, { kind })).toBe(!TERMINAL.includes(s));
    }
  });

  test("canCancel: the agent-only UI is never shown", () => {
    for (const s of JOB_STATES) expect(canCancel({ state: s }, { kind: "agent" })).toBe(false);
  });

  test("canCancelBatch follows the batch state", () => {
    expect(canCancelBatch({ state: "active" }, { kind: "human" })).toBe(true);
    expect(canCancelBatch({ state: "awaiting_confirmation" }, { kind: "human" })).toBe(true);
    for (const s of ["finished", "rejected", "expired"] as const) expect(canCancelBatch({ state: s }, { kind: "human" })).toBe(false);
    expect(canCancelBatch({ state: "active" }, { kind: "agent" })).toBe(false);
  });

  test("polling stops when no job is live", () => {
    expect(isLive([{ state: "completed" }, { state: "failed" }])).toBe(false);
    expect(isLive([])).toBe(false);
    expect(isLive([{ state: "completed" }, { state: "generating" }])).toBe(true);
    expect(pollDelayMs([{ state: "cancelled" }])).toBeNull();
    expect(pollDelayMs([{ state: "queued" }])).toBe(5000);
  });
});

describe("followJob", () => {
  const job = (state: JobState) => ({ state });

  test("loops until terminal and reports every job", async () => {
    const seq = ["queued", "generating", "completed"] as const;
    let i = 0;
    const seen: string[] = [];
    const calls: (string | undefined)[] = [];
    await followJob({
      initial: job("queued"),
      wait: async (since) => {
        calls.push(since);
        return job(seq[++i]!);
      },
      onJob: (j) => seen.push(j.state),
      active: () => true,
    });
    expect(seen).toEqual(["generating", "completed"]);
    expect(calls).toEqual(["queued", "generating"]);
  });

  test("does not wait at all for a terminal job", async () => {
    let n = 0;
    await followJob({ initial: job("failed"), wait: async () => (n++, job("failed")), onJob: () => {}, active: () => true });
    expect(n).toBe(0);
  });

  test("stops when unmounted", async () => {
    let n = 0;
    let active = true;
    await followJob({
      initial: job("queued"),
      wait: async () => {
        n++;
        active = false;
        return job("generating");
      },
      onJob: () => {},
      active: () => active,
    });
    expect(n).toBe(1);
  });

  test("a failed wait stops the loop through onError", async () => {
    let err = 0;
    await followJob({ initial: job("queued"), wait: async () => null, onJob: () => {}, onError: () => err++, active: () => true });
    expect(err).toBe(1);
  });
});

describe("shouldLoadLedger", () => {
  test("loads once per visit, never again after a (possibly empty) load", () => {
    expect(shouldLoadLedger("ledger", false)).toBe(true);
    expect(shouldLoadLedger("ledger", true)).toBe(false);
    expect(shouldLoadLedger("jobs", false)).toBe(false);
  });
});
