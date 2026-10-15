import { describe, expect, test } from "bun:test";
import { formatBytes, mergeRows, syncMessage } from "./offlineView";
import type { LocalItem, OfflineSet } from "../api/offline";

const local = (id: string, title = "L" + id): LocalItem => ({ track_id: id, title, album: "A", duration_s: 5, format: "flac", bytes: 10, src: "/media/x.flac", coverSrc: null, contentType: "audio/flac" });
const set = (ids: string[]): OfflineSet => ({
  items: ids.map((id, i) => ({ track_id: id, title: "S" + id, album: "A", duration_s: 7, position: i, status: "final", lossy: false, flac: null, mp3: null })),
  limit_items: 100,
  name: "Offline",
  playlist_id: "p",
  revision: 3,
  totals: { flac_bytes: 0, items: ids.length, mp3_bytes: 0 },
  updated_at: "x",
});

describe("mergeRows", () => {
  test("online: set order, local state merged in, local-only items kept", () => {
    const rows = mergeRows(set(["a", "b"]), [local("b"), local("z")], (id) => (id === "b" || id === "z" ? "complete" : "none"));
    expect(rows.map((r) => [r.track_id, r.status, r.inSet])).toEqual([
      ["a", "none", true],
      ["b", "complete", true],
      ["z", "complete", false],
    ]);
    expect(rows[1]!.title).toBe("Sb"); // the set's title wins while online
  });
  test("offline (no set): only the downloaded items, from local data", () => {
    const rows = mergeRows(null, [local("b", "Beta"), local("a", "Alpha")], () => "complete");
    expect(rows.map((r) => r.title)).toEqual(["Alpha", "Beta"]);
    expect(rows.every((r) => !r.inSet)).toBe(true);
  });
});

describe("formatBytes", () => {
  test("units", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(1536)).toBe("1.5 KiB");
    expect(formatBytes(5 * 1024 * 1024)).toBe("5.0 MiB");
    expect(formatBytes(3 * 1024 ** 3)).toBe("3.0 GiB");
  });
});

describe("syncMessage", () => {
  test("summarises downloads, evictions and stops", () => {
    expect(syncMessage({ downloaded: ["a"], failed: [], evicted: ["b", "c"], stopped: null })).toBe("Downloaded 1, removed 2.");
    expect(syncMessage({ downloaded: [], failed: [], evicted: [], stopped: null })).toBe("Already up to date.");
    expect(syncMessage({ downloaded: [], failed: [{ track_id: "a", error: "x" }], evicted: [], stopped: "quota" })).toBe(
      "Stopped: not enough free storage on this device. 1 failed.",
    );
  });
});
