import { describe, expect, test } from "bun:test";
import { chapterRows, lossyBanner, nextPollMs, planRows, POLL_MS, POLL_GIVE_UP_MS, progressText, tapeErrorText } from "./tapeView";
import type { components } from "../api/schema";

type Tape = components["schemas"]["Tape"];
const item = (n: number, over: Partial<Tape["items"][number]> = {}): Tape["items"][number] => ({
  slot_id: `sl-${n}`, take_id: `t-${n}`, position: n, gain_db: -2.5, status: "matched", predicted_lufs: -14, lossy: false, resampled: false, upmixed: false, downmixed: false, ...over,
});
const tape = (over: Partial<Tape> = {}): Tape => ({
  id: "tp-1", record_id: "rec-1", record_revision: 4, state: "ready", reused: false, progress: null, target_lufs: -14, spread_lu: { before: 6, after: 0.4 },
  items: [item(1), item(2, { lossy: true, status: "cut_clamped", gain_db: 3 })], chapters: [{ index: 1, slot_id: "sl-1", start_ms: 0, samples: 100 }, { index: 2, slot_id: "sl-2", start_ms: 181_500, samples: 100 }],
  warnings: [], accepted_warnings: false, contains_lossy: true, lossy_sources: 1, total_ms: 400_000, bytes: 1234, sha256: null,
  download: { url: "/media/t.flac", filename: "t.flac", bytes: 1234, expires_at: "2099-01-01T00:00:00Z" }, error: null, created_at: "2026-10-15T12:00:00Z", started_at: null, finished_at: null, ...over,
});

describe("poll policy", () => {
  test("1 s while queued or running", () => {
    expect(POLL_MS).toBe(1000);
    expect(nextPollMs("queued", 0)).toBe(1000);
    expect(nextPollMs("running", 60_000)).toBe(1000);
  });
  test("stops on terminal states", () => {
    for (const s of ["ready", "failed", "expired"] as const) expect(nextPollMs(s, 0)).toBeNull();
  });
  test("gives up at 15 minutes", () => {
    expect(POLL_GIVE_UP_MS).toBe(15 * 60_000);
    expect(nextPollMs("running", POLL_GIVE_UP_MS - 1)).toBe(1000);
    expect(nextPollMs("running", POLL_GIVE_UP_MS)).toBeNull();
  });
});

describe("progress text", () => {
  test("per phase", () => {
    expect(progressText(tape({ state: "queued", progress: null }))).toBe("Queued");
    expect(progressText(tape({ state: "running", progress: null }))).toBe("Starting");
    const p = (phase: "decode" | "encode" | "rewrite" | "verify") => progressText(tape({ state: "running", progress: { phase, item: 2, items: 5 } }));
    expect(p("decode")).toBe("Decoding track 2 of 5");
    expect(p("encode")).toBe("Encoding track 2 of 5");
    expect(p("rewrite")).toBe("Writing the file");
    expect(p("verify")).toBe("Verifying the file");
  });
  test("terminal states", () => {
    expect(progressText(tape())).toBe("Ready");
    expect(progressText(tape({ state: "expired" }))).toContain("expired");
    expect(progressText(tape({ state: "failed", error: { code: "TAPE_TOO_LONG", message: "Too long" } }))).toBe("Failed: Too long");
  });
});

describe("plan table, banner, chapters", () => {
  const titles = new Map([["sl-1", "Synthetic take 1"], ["sl-2", "Synthetic take 2"]]);
  test("plan rows", () => {
    const rows = planRows(tape(), titles);
    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({ position: 1, title: "Synthetic take 1", gain: "-2.5 dB", status: "matched", flags: [] });
    expect(rows[1]!.gain).toBe("+3.0 dB");
    expect(rows[1]!.status).toBe("cut limited");
    expect(rows[1]!.flags).toContain("lossy");
  });
  test("lossy banner", () => {
    expect(lossyBanner(tape())).toContain("1 track");
    expect(lossyBanner(tape({ lossy_sources: 2 }))).toContain("2 tracks");
    expect(lossyBanner(tape({ contains_lossy: false, lossy_sources: 0 }))).toBeNull();
  });
  test("chapter list from chapters", () => {
    const c = chapterRows(tape(), titles);
    expect(c).toEqual([{ index: 1, title: "Synthetic take 1", offset: "0:00", start_ms: 0 }, { index: 2, title: "Synthetic take 2", offset: "3:01", start_ms: 181_500 }]);
    expect(chapterRows(tape({ chapters: null }), titles)).toEqual([]);
  });
  test("error text", () => {
    expect(tapeErrorText(501, "NOT_IMPLEMENTED", "")).toContain("not available yet");
    expect(tapeErrorText(409, "TAPE_NEEDS_REVIEW", "x")).toContain("human");
    expect(tapeErrorText(409, "TAPE_QUOTA", "Quota")).toBe("Quota");
  });
});
