import { describe, expect, test } from "bun:test";
import { estimate, quota, view } from "./fixtures/t1/transforms";
import {
  canCancel, cpuUseLine, explainRateLimit, estimateLine, fmtBytes, fmtCpu, fmtDuration, inputFacts, isUnavailable, orderKinds, phaseLabel, pollDelayMs,
  progressPercent, quotaMeter, refusalText, resetsIn, rowErrorText, sortTransforms, stateLabel, stateTone, warningNote, warningTone,
} from "./transformView";

const NOW = Date.parse("2026-11-03T20:40:00Z");
const refusal = (status: number, code: string, details: Record<string, unknown> = {}, message = "", retryAfterS?: number) => ({ status, code, message, details, ...(retryAfterS !== undefined ? { retryAfterS } : {}) });

describe("estimate line", () => {
  test("fixture response", () => {
    expect(estimateLine(estimate)).toBe("$0.00 · ≈ 14 s of CPU · 3 min 20 s of audio · 180 MB");
  });
  test("durations and sizes", () => {
    expect(fmtDuration(0)).toBe("0 s");
    expect(fmtDuration(125)).toBe("2 min 5 s");
    expect(fmtDuration(3780)).toBe("1 h 3 min");
    expect(fmtCpu(420)).toBe("420 ms");
    expect(fmtBytes(57_600_000)).toBe("55 MB");
  });
  test("resolved inputs carry their source chips", () => {
    const f = inputFacts(estimate.inputs[0]!);
    expect(f.role).toBe("Source");
    expect(f.chips).toEqual([
      { label: "92 BPM", source: "declared" },
      { label: "8A · Am", source: "measured" },
    ]);
  });
  test("an octave-folded tempo says so; a missing key has no chip", () => {
    const f = inputFacts({ ...estimate.inputs[0]!, key: null, bpm: { value: 184, source: "measured", octave_fold: -1, confidence: 0.4 } });
    expect(f.chips).toEqual([{ label: "184 BPM (halved)", source: "measured" }]);
  });
});

describe("states", () => {
  test("Cancel is visible only for live states and only once", () => {
    expect(canCancel(view("queued"))).toBe(true);
    expect(canCancel(view("running"))).toBe(true);
    expect(canCancel(view("running", { cancel_requested_at: "2026-11-03T10:01:00Z" }))).toBe(false);
    for (const s of ["done", "failed", "cancelled"] as const) expect(canCancel(view(s))).toBe(false);
  });
  test("labels, tones, phases, progress", () => {
    expect(stateLabel("running")).toBe("Running");
    expect(stateTone("failed")).toBe("bad");
    expect(stateTone("done")).toBe("ok");
    expect(phaseLabel("render")).toBe("Rendering");
    expect(phaseLabel("some_new_phase")).toBe("some new phase");
    expect(phaseLabel(null)).toBe("");
    expect(progressPercent(420)).toBe(42);
    expect(progressPercent(5000)).toBe(100);
    expect(progressPercent(-3)).toBe(0);
  });
  test("sorting: running, queued, then newest first", () => {
    const items = [
      view("done", { id: "a", created_at: "2026-11-03T09:00:00Z" }),
      view("queued", { id: "b", created_at: "2026-11-03T08:00:00Z" }),
      view("done", { id: "c", created_at: "2026-11-03T11:00:00Z" }),
      view("running", { id: "d", created_at: "2026-11-03T07:00:00Z" }),
    ];
    expect(sortTransforms(items).map((t) => t.id)).toEqual(["d", "b", "c", "a"]);
    expect(items.map((t) => t.id)).toEqual(["a", "b", "c", "d"]);
  });
  test("polling only while something is live", () => {
    expect(pollDelayMs([view("done")])).toBeNull();
    expect(pollDelayMs([view("done"), view("queued")])).toBe(3000);
  });
  test("kind order puts known kinds first, skips unlisted ones", () => {
    expect(orderKinds(["binaural", "glacier", "future", "morph"])).toEqual(["morph", "glacier", "binaural", "future"]);
  });
  test("cpu line: used when finished, expected before", () => {
    expect(cpuUseLine(view("done"))).toBe("11 s of CPU");
    expect(cpuUseLine(view("queued"))).toBe("≈ 14 s of CPU expected");
  });
});

describe("warnings and errors", () => {
  test("message wins, code is the fallback, cautions are marked", () => {
    expect(warningNote({ code: "downbeat_auto", message: "Guessed." })).toBe("Guessed.");
    expect(warningNote({ code: "downbeat_auto", message: "" })).toBe("downbeat auto");
    expect(warningTone({ code: "beat_phase_weak", message: "" })).toBe("caution");
    expect(warningTone({ code: "downbeat_auto", message: "" })).toBe("info");
  });
  test("row errors", () => {
    expect(rowErrorText(null)).toBe("");
    expect(rowErrorText({ code: "INGEST_FAILED:DUPLICATE_AUDIO", message: "x" })).toContain("identical");
    expect(rowErrorText({ code: "INGEST_FAILED:OTHER", message: "x" })).toContain("could not be added");
    expect(rowErrorText({ code: "LOUDNESS_UNSAFE", message: "x" })).toContain("loudness");
    expect(rowErrorText({ code: "BRAND_NEW", message: "kept" })).toBe("kept");
  });
});

describe("refusals", () => {
  test("501 and 503 transform are the not-available state", () => {
    expect(isUnavailable(refusal(501, "NOT_IMPLEMENTED"))).toBe(true);
    expect(isUnavailable(refusal(503, "UNAVAILABLE", { component: "transform" }))).toBe(true);
    expect(isUnavailable(refusal(503, "UNAVAILABLE", { component: "fluidsynth" }))).toBe(false);
    expect(isUnavailable(refusal(422, "INVALID_TRANSFORM"))).toBe(false);
    expect(refusalText(refusal(501, "NOT_IMPLEMENTED"))).toBe("Transforms are not available on this server yet.");
    expect(refusalText(refusal(503, "UNAVAILABLE", { component: "transform" }))).toContain("switched off");
  });
  test("quota explains the pool and when it resets", () => {
    const t = refusalText(refusal(429, "TRANSFORM_QUOTA_EXCEEDED", { used_ms: 590000, reserved_ms: 10000, cap_ms: 600000, resets_at: "2026-11-03T23:00:00Z" }), NOW);
    expect(t).toContain("allowance is used up");
    expect(t).toContain("10 min of 10 min used");
    expect(t).toContain("resets in 2 h 20 min");
  });
  test("queue full shows counts and the retry hint", () => {
    const t = refusalText(refusal(429, "TRANSFORM_QUEUE_FULL", { running: 1, queued: 8, max: 8 }, "", 30), NOW);
    expect(t).toBe("The transform queue is full. 1 running, 8 queued (limit 8). Try again in 30 s.");
  });
  test("4xx codes", () => {
    expect(refusalText(refusal(422, "TRANSFORM_TOO_LARGE", { limit: "cpu_seconds" }))).toContain("cpu seconds");
    expect(refusalText(refusal(422, "LOSSY_MASTER"))).toContain("lossy");
    expect(refusalText(refusal(422, "TRANSFORM_TAKE_NOT_MOTHER"))).toContain("cannot be used");
    expect(refusalText(refusal(422, "ALIGNMENT_UNRESOLVED", { input: "b", quantity: "downbeat" }))).toContain("downbeat of input b");
    expect(refusalText(refusal(422, "INVALID_TRANSFORM", { field: "fade_in_s" }, "Fade is too long"))).toBe("Fade is too long");
    expect(refusalText(refusal(422, "INVALID_TRANSFORM", { field: "fade_in_s" }))).toBe("Check the fade in s setting.");
    expect(refusalText(refusal(404, "NOT_FOUND"))).toContain("no longer exists");
    expect(refusalText(refusal(403, "FORBIDDEN"))).toBe("You may not do that.");
    expect(refusalText(refusal(429, "RATE_LIMITED", {}, "", 12))).toBe("Too many requests. Try again in 12 s.");
    expect(refusalText(refusal(500, "WHATEVER", {}, "server said"))).toBe("server said");
    expect(refusalText(refusal(0, "NETWORK"))).toBe("The request failed.");
  });
  test("resetsIn is never negative and ignores garbage", () => {
    expect(resetsIn("2026-11-03T20:00:00Z", NOW)).toBe("in 0 s");
    expect(resetsIn("nope", NOW)).toBe("");
  });
});

describe("quota meter", () => {
  test("percentages, label, full flag", () => {
    const m = quotaMeter(quota);
    expect(m).toEqual({ usedPct: 20, reservedPct: 5, label: "2 min of 10 min used · 30 s reserved", full: false });
    expect(quotaMeter({ cap_ms: 100, used_ms: 80, reserved_ms: 80 })).toMatchObject({ usedPct: 80, reservedPct: 20, full: true });
    expect(quotaMeter({ cap_ms: 0, used_ms: 0, reserved_ms: 0 }).usedPct).toBe(0);
  });
});

describe("429 explanation from the estimate", () => {
  const rl = refusal(429, "RATE_LIMITED", {}, "", 20);
  test("an exhausted pool becomes the quota refusal with its reset time", () => {
    const e = { ...estimate, quota: { ...quota, used_ms: 590000, reserved_ms: 10000 } };
    const x = explainRateLimit(rl, e, 8);
    expect(x.code).toBe("TRANSFORM_QUOTA_EXCEEDED");
    expect(refusalText(x, NOW)).toContain("resets in 3 h 20 min");
  });
  test("a full queue becomes the queue refusal", () => {
    const x = explainRateLimit(rl, { ...estimate, queue: { running: 1, queued: 7, position_if_created: 9 } }, 8);
    expect(x.code).toBe("TRANSFORM_QUEUE_FULL");
    expect(refusalText(x, NOW)).toContain("1 running, 7 queued (limit 8). Try again in 20 s.");
  });
  test("otherwise, and for other codes, it is left alone", () => {
    expect(explainRateLimit(rl, estimate, 8)).toBe(rl);
    expect(explainRateLimit(rl, null, 8)).toBe(rl);
    const other = refusal(422, "INVALID_TRANSFORM");
    expect(explainRateLimit(other, estimate, 8)).toBe(other);
  });
});

import { alsoFromRows, engineChip, specHashShort } from "./transformView";
describe("lineage helpers", () => {
  test("also-from rows use the tree's title or a short id, and a role label", () => {
    const rows = alsoFromRows([{ role: "b", track_id: "abcdef123456" }, { role: "score_source", track_id: "t9" }], new Map([["t9", "Nine"]]));
    expect(rows).toEqual([
      { trackId: "abcdef123456", label: "abcdef12", role: "B", inTree: false },
      { trackId: "t9", label: "Nine", role: "Score", inTree: true },
    ]);
    expect(alsoFromRows(undefined, new Map())).toEqual([]);
  });
  test("engine chip and hash prefix", () => {
    expect(engineChip({ kind: "glacier" })).toBe("dsp · Glacier");
    expect(engineChip(null)).toBe("");
    expect(specHashShort("0123456789abcdef")).toBe("01234567");
  });
});
