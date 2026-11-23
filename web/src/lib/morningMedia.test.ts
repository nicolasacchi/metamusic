import { beforeEach, describe, expect, test } from "bun:test";
import { forgetCrates, morningLinks, morningMeta, rememberCrate } from "./morningMedia";
import type { MorningCrate } from "./morningCrate";

const NOW = Date.parse("2026-11-23T08:00:00Z");
function crate(url = "/media/a.flac?x=1", exp = "2026-11-23T20:00:00Z"): MorningCrate {
  return {
    adjacency_relaxed: false,
    anchors: 0,
    built_at: null,
    chapters: [{ anchor: null, audio_start_ms: 4000, end_ms: 100, gain_db: null, limited: null, lufs_after: null, markers: 0, n: 1, side: 0, skipped_at_ms: null, stars: null, start_ms: 0, strong_markers: 0, track: null, verdict: "unheard" }],
    created_at: "2026-11-23T07:00:00Z",
    duration_ms: 100,
    error_code: null,
    id: "c1",
    playlist_id: null,
    revealed: false,
    revealed_at: null,
    sides: [{ bytes: 10, chapters: 1, duration_ms: 100, expires_at: exp, label: "Side A", removed: false, sha256: "a".repeat(64), side: 0, url }],
    state: "ready",
    takes: 1,
    trigger: "scheduled",
  };
}

beforeEach(() => forgetCrates());

describe("morningLinks", () => {
  test("uses the remembered view: no request, one lossless link", async () => {
    rememberCrate(crate());
    let reads = 0;
    const l = await morningLinks("morning:c1:0", async () => (reads++, null), () => NOW);
    expect(reads).toBe(0);
    expect(l?.flac?.url).toBe("/media/a.flac?x=1");
    expect(l?.track_id).toBe("morning:c1:0");
    expect(l?.lossy).toBe(false);
    expect(l?.ttl_s).toBe(12 * 3600);
  });

  test("a second request for the same side (a media error) re-fetches the crate once, and uses the fresh link", async () => {
    rememberCrate(crate());
    let reads = 0;
    const get = async () => (reads++, crate("/media/a.flac?x=2"));
    await morningLinks("morning:c1:0", get, () => NOW);
    const l = await morningLinks("morning:c1:0", get, () => NOW);
    expect(reads).toBe(1);
    expect(l?.flac?.url).toBe("/media/a.flac?x=2");
  });

  test("an unknown crate or an expired link is read; a failed read is null", async () => {
    expect(await morningLinks("morning:c1:0", async () => null, () => NOW)).toBeNull();
    rememberCrate(crate("/media/a.flac", "2026-11-23T07:59:00Z"));
    const l = await morningLinks("morning:c1:0", async () => crate("/media/fresh.flac"), () => NOW);
    expect(l?.flac?.url).toBe("/media/fresh.flac");
  });

  test("a swept side has no link; a bad item has none", async () => {
    const c = crate();
    c.sides[0] = { ...c.sides[0]!, removed: true, url: null };
    rememberCrate(c);
    expect(await morningLinks("morning:c1:0", async () => c, () => NOW)).toBeNull();
    expect(await morningLinks("tape:x", async () => c, () => NOW)).toBeNull();
  });
});

test("morningMeta is blind", async () => {
  rememberCrate(crate());
  expect(await morningMeta("morning:c1:0")).toEqual({ title: "Take 1 of 1", album: "Side A" });
  expect(await morningMeta("morning:zz:0")).toBeNull();
});
