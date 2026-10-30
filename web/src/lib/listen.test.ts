import { describe, expect, test } from "bun:test";
import { createRecorder, type ListenEvent, type PlayerLike } from "./listen.svelte";

class FakeEl extends EventTarget {
  currentTime = 0;
  duration = 100;
  fire(type: string) {
    this.dispatchEvent(new Event(type));
  }
}

function setup() {
  const el = new FakeEl();
  const player: PlayerLike = { el, state: { trackId: "A", format: "flac", duration: 100 } };
  const out: ListenEvent[] = [];
  let seq = 0;
  const rec = createRecorder({
    player,
    emit: (e) => void out.push(e),
    sessionId: "sess",
    deviceId: () => "dev1",
    id: () => `id${++seq}`,
    now: () => new Date("2026-01-01T00:00:00Z"),
  });
  const at = (s: number, type?: string) => {
    el.currentTime = s;
    el.fire(type ?? "timeupdate");
  };
  const kinds = () => out.map((e) => e.kind);
  return { el, player, out, rec, at, kinds };
}

describe("listen recorder", () => {
  test("play 0 then pause at 30 s gives start + segment [0, 30000]", () => {
    const { el, out, at, kinds } = setup();
    el.fire("play");
    at(30);
    el.fire("pause");
    expect(kinds()).toEqual(["start", "segment"]);
    expect(out[0]).toMatchObject({ track_id: "A", from_ms: 0, session_id: "sess", device_id: "dev1", format: "flac", id: "id1" });
    expect(out[1]).toMatchObject({ from_ms: 0, to_ms: 30000, track_id: "A" });
    expect(out[0]!.client_at).toBe("2026-01-01T00:00:00.000Z");
  });

  test("seek 10 s to 50 s closes the segment and emits seek, then continues from 50", () => {
    const { el, out, at, kinds } = setup();
    el.fire("play");
    at(10);
    el.currentTime = 50;
    el.fire("seeked");
    expect(kinds()).toEqual(["start", "segment", "seek"]);
    expect(out[1]).toMatchObject({ from_ms: 0, to_ms: 10000 });
    expect(out[2]).toMatchObject({ from_ms: 10000, to_ms: 50000 });
    at(60);
    el.fire("pause");
    expect(out[3]).toMatchObject({ kind: "segment", from_ms: 50000, to_ms: 60000 });
  });

  test("ended closes the segment and emits complete", () => {
    const { el, out, at, kinds, rec } = setup();
    el.fire("play");
    at(99);
    el.currentTime = 100;
    el.fire("ended");
    expect(kinds()).toEqual(["start", "segment", "complete"]);
    expect(out[1]).toMatchObject({ from_ms: 0, to_ms: 100000 });
    // the player already advanced; the track change after a complete is not a skip
    rec.sync();
    expect(kinds()).toEqual(["start", "segment", "complete"]);
  });

  test("next at 40 % is a skip", () => {
    const { el, player, out, at, kinds, rec } = setup();
    el.fire("play");
    at(40);
    player.state.trackId = "B";
    rec.sync();
    expect(kinds()).toEqual(["start", "segment", "skip"]);
    expect(out[1]).toMatchObject({ track_id: "A", from_ms: 0, to_ms: 40000 });
    expect(out[2]).toMatchObject({ track_id: "A", kind: "skip" });
  });

  test("next after 95 % heard is not a skip", () => {
    const { el, player, kinds, at, rec } = setup();
    el.fire("play");
    at(95);
    player.state.trackId = "B";
    rec.sync();
    expect(kinds()).toEqual(["start", "segment"]);
  });

  test("a new track's play after a change starts a fresh start event", () => {
    const { el, player, out, at, kinds } = setup();
    el.fire("play");
    at(10);
    player.state.trackId = "B";
    el.currentTime = 0;
    el.fire("play"); // sync() inside the handler closes A first
    expect(kinds()).toEqual(["start", "segment", "skip", "start"]);
    expect(out[3]).toMatchObject({ track_id: "B", from_ms: 0 });
  });

  test("hidden page closes the segment and keeps recording", () => {
    const { el, out, at, kinds, rec } = setup();
    el.fire("play");
    at(20);
    rec.onHidden();
    expect(kinds()).toEqual(["start", "segment"]);
    expect(out[1]).toMatchObject({ from_ms: 0, to_ms: 20000 });
    at(35);
    el.fire("pause");
    expect(out[2]).toMatchObject({ kind: "segment", from_ms: 20000, to_ms: 35000 });
  });

  test("the 30 s tick flushes the open segment", () => {
    const { el, out, at, rec } = setup();
    el.fire("play");
    at(30);
    rec.tick();
    expect(out.at(-1)).toMatchObject({ kind: "segment", from_ms: 0, to_ms: 30000 });
  });

  test("the initial deep-link seek before any play is not recorded", () => {
    const { el, kinds } = setup();
    el.currentTime = 42;
    el.fire("seeked");
    expect(kinds()).toEqual([]);
  });

  test("destroy detaches listeners", () => {
    const { el, kinds, rec } = setup();
    rec.destroy();
    el.fire("play");
    expect(kinds()).toEqual([]);
  });
});

describe("listen recorder: tape items", () => {
  test("a tape current item emits no listen events; the track before it still closes", () => {
    const { el, player, out, at, kinds } = setup();
    el.fire("play");
    at(5);
    player.state.trackId = "tape:abc";
    el.currentTime = 0;
    el.fire("play");
    at(20);
    el.fire("pause");
    el.fire("ended");
    expect(out.every((e) => e.track_id === "A")).toBe(true);
    expect(kinds()).toEqual(["start", "segment", "skip"]);
  });
});
