import { describe, expect, test } from "bun:test";
import type { components } from "../api/schema";
import {
  badgeFor,
  buildInbox,
  countNew,
  createHeardTracker,
  flightBatch,
  flightSummary,
  hasSeenMarker,
  headerLine,
  heardKey,
  isHeard,
  newlyCompleted,
  newSince,
  readHeard,
  readSeenAt,
  SEEN_KEY,
  soonestJob,
  sourceLabel,
  writeHeard,
  writeSeenAt,
  type KV,
} from "./inbox";

type Track = components["schemas"]["TrackSummary"];
type Job = components["schemas"]["JobSummary"];
type Batch = components["schemas"]["Batch"];

const track = (id: string, recorded_at: string, over: Partial<Track> = {}): Track => ({ id, title: id, recorded_at, duration_s: 120, ...over }) as Track;
const job = (id: string, state: Job["state"], take_index: number, over: Partial<Job> = {}): Job =>
  ({ id, state, take_index, title: "Night", track_id: state === "completed" ? `t-${id}` : null, error_code: null, updated_at: "2026-10-16T12:00:00Z", ...over }) as Job;
const batch = (id: string, jobs: Job[], state: Batch["state"] = "active"): Batch => ({ id, state, jobs }) as Batch;

function mem(): KV & { m: Map<string, string> } {
  const m = new Map<string, string>();
  return { m, getItem: (k) => m.get(k) ?? null, setItem: (k, v) => void m.set(k, v) };
}

const SEEN = "2026-10-16T10:00:00.000Z";

describe("seen marker", () => {
  test("unset: a week before now; set: the stored time; garbage: ignored", () => {
    const kv = mem();
    const now = new Date("2026-10-17T10:00:00Z");
    expect(readSeenAt(kv, now)).toBe("2026-10-10T10:00:00.000Z");
    expect(hasSeenMarker(kv)).toBe(false);
    expect(writeSeenAt(kv, new Date(SEEN))).toBe(SEEN);
    expect(kv.m.get(SEEN_KEY)).toBe(SEEN);
    expect(readSeenAt(kv, now)).toBe(SEEN);
    kv.setItem(SEEN_KEY, "nope");
    expect(readSeenAt(kv, now)).toBe("2026-10-10T10:00:00.000Z");
  });
  test("a blocked store does not throw", () => {
    const bad: KV = { getItem: () => { throw new Error("blocked"); }, setItem: () => { throw new Error("blocked"); } };
    expect(readSeenAt(bad, new Date(SEEN))).toMatch(/^2026-10-09/);
    expect(() => writeSeenAt(bad, new Date())).not.toThrow();
    expect(readHeard(bad, "k").size).toBe(0);
  });
});

describe("new since", () => {
  const items = [
    track("c", "2026-10-16T12:00:03Z"),
    track("a", "2026-10-16T12:00:01Z"),
    track("edge", SEEN),
    track("old", "2026-10-15T09:00:00Z"),
    track("b", "2026-10-16T12:00:02Z"),
  ];
  test("strictly after the marker, oldest first (take order)", () => {
    expect(newSince(items, SEEN).map((t) => t.id)).toEqual(["a", "b", "c"]);
  });
  test("the count takes the server's inclusive boundary off the total", () => {
    expect(countNew(items, 4, SEEN)).toBe(3);
    expect(countNew(items.slice(0, 2), 80, SEEN)).toBe(80);
  });
});

describe("heard rule", () => {
  test("half the take or 60 s", () => {
    expect(isHeard(0, 100)).toBe(false);
    expect(isHeard(49, 100)).toBe(false);
    expect(isHeard(50, 100)).toBe(true);
    expect(isHeard(59, 300)).toBe(false);
    expect(isHeard(60, 300)).toBe(true);
    expect(isHeard(10, 0)).toBe(false);
  });
  test("tracker counts continuous play, not seeks, and fires once", () => {
    const got: string[] = [];
    const h = createHeardTracker((id) => got.push(id));
    // seek straight to the end of a 100 s take: nothing is credited
    h.sample("t", 0, 100, true);
    h.sample("t", 95, 100, true);
    expect(got).toEqual([]);
    // 50 s of steady play (1 s steps)
    h.sample("u", 0, 100, true);
    for (let s = 1; s <= 49; s++) h.sample("u", s, 100, true);
    expect(got).toEqual([]);
    h.sample("u", 50, 100, true);
    h.sample("u", 51, 100, true);
    expect(got).toEqual(["u"]);
    expect(h.has("u")).toBe(true);
  });
  test("paused samples credit nothing and break the run", () => {
    const got: string[] = [];
    const h = createHeardTracker((id) => got.push(id));
    h.sample("t", 0, 10, true);
    h.sample("t", 1, 10, true);
    h.sample("t", 2, 10, false);
    h.sample("t", 3, 10, true); // first sample after a pause has no previous position
    h.sample("t", 4, 10, true);
    expect(got).toEqual([]);
    h.sample("t", 5, 10, true);
    h.sample("t", 6, 10, true);
    expect(got).toEqual([]);
    h.sample("t", 7, 10, true);
    expect(got).toEqual(["t"]);
  });
  test("persisted per device", () => {
    const kv = mem();
    expect(heardKey("dev-1")).not.toBe(heardKey("dev-2"));
    writeHeard(kv, heardKey("dev-1"), new Set(["x", "y"]));
    expect([...readHeard(kv, heardKey("dev-1"))]).toEqual(["x", "y"]);
    expect(readHeard(kv, heardKey("dev-2")).size).toBe(0);
  });
});

describe("sections", () => {
  const input = {
    tracks: [track("n2", "2026-10-16T12:00:02Z", { duration_s: 180 }), track("n1", "2026-10-16T12:00:01Z", { duration_s: 60 }), track("old", "2026-10-15T00:00:00Z")],
    trackTotal: 3,
    seenAt: SEEN,
    heard: new Set(["n1"]),
    unheardProxy: [track("n1", "2026-10-16T12:00:01Z"), track("d1", "2026-10-01T00:00:00Z"), track("h", "2026-10-02T00:00:00Z")],
    batches: [batch("b1", [job("j2", "generating", 1), job("j1", "completed", 0), job("j3", "queued", 2)]), batch("done", [job("x", "completed", 0)], "finished")],
    approvals: 2,
    openDuels: 3,
    proposals: 1,
    failures: [job("f1", "failed", 0, { error_code: "TIMEOUT" }), job("f0", "failed", 0, { updated_at: "2026-10-01T00:00:00Z" })],
  };
  test("new since: creation order, dots for unheard, minutes", () => {
    const m = buildInbox({ ...input, heard: new Set(["n1", "h"]) });
    expect(m.rows.map((r) => [r.track.id, r.unheard])).toEqual([["n1", false], ["n2", true]]);
    expect(m.playOrder).toEqual(["n1", "n2"]);
    expect(m.count).toBe(2);
    expect(m.seconds).toBe(240);
    expect(m.partial).toBe(false);
  });
  test("still unheard drops what is in New and what was heard", () => {
    const m = buildInbox({ ...input, heard: new Set(["h"]) });
    expect(m.stillUnheard.map((r) => r.track.id)).toEqual(["d1"]);
  });
  test("in flight shows only active batches, with segments in take order", () => {
    const m = buildInbox(input);
    expect(m.inFlight.map((f) => f.id)).toEqual(["b1"]);
    const f = m.inFlight[0]!;
    expect(f.segments.map((s) => s.state)).toEqual(["done", "running", "queued"]);
    expect(flightSummary(f)).toBe("1 ready · 1 generating · 1 queued");
    expect(f.settled).toBe(false);
  });
  test("needs you totals, failures only those since the marker", () => {
    const m = buildInbox(input);
    expect(m.needsYou).toEqual({ approvals: 2, duels: 3, proposals: 1, total: 6 });
    expect(m.failures.map((j) => j.id)).toEqual(["f1"]);
  });
  test("partial when the server has more than was loaded", () => {
    const m = buildInbox({ ...input, trackTotal: 90 });
    expect(m.partial).toBe(true);
    expect(m.count).toBe(89);
  });
  test("soonest job is the one furthest along; completed jobs are skipped", () => {
    expect(soonestJob([batch("b", [job("q", "queued", 0), job("g", "generating", 1), job("c", "completed", 2)])])?.id).toBe("g");
    expect(soonestJob([batch("b", [job("c", "completed", 0)])])).toBeNull();
  });
  test("newly completed includes a batch that just left the active list", () => {
    const before = [batch("b", [job("a", "generating", 0), job("c", "completed", 1)])];
    const after = [batch("b", [job("a", "completed", 0), job("c", "completed", 1)], "finished")];
    expect(newlyCompleted(before, after).map((j) => j.id)).toEqual(["a"]);
    expect(newlyCompleted(after, after)).toEqual([]);
  });
  test("flightBatch settles when nothing is queued or running", () => {
    expect(flightBatch(batch("b", [job("a", "completed", 0), job("f", "failed", 1)])).settled).toBe(true);
  });
});

describe("badge and labels", () => {
  test("unseen takes plus needs-you, capped at 9+", () => {
    expect(badgeFor(0, 0)).toBeNull();
    expect(badgeFor(2, 1)).toBe("3");
    expect(badgeFor(9, 0)).toBe("9");
    expect(badgeFor(7, 3)).toBe("9+");
    expect(badgeFor(-1, 0)).toBeNull();
  });
  test("header and source labels", () => {
    const now = new Date(2026, 9, 17, 9, 0);
    const seen = new Date(2026, 9, 16, 14, 2).toISOString();
    expect(headerLine({ count: 7, seconds: 31 * 60, partial: false }, seen, now)).toBe("Since Fri 14:02 · 7 new takes · 31 min");
    expect(headerLine({ count: 1, seconds: 60, partial: true }, seen, now)).toBe("Since Fri 14:02 · 1 new take · at least 1 min");
    expect(sourceLabel(seen, now)).toBe("New since Fri 14:02");
  });
});
