// Queue editing in the player store: remove / move / clearUpcoming / enqueue invariants, "Playing from", replace + undo.
import { describe, expect, test } from "bun:test";
import { Player, type DeviceHooks, type MediaLinks } from "./player.svelte";

class FakeAudio extends EventTarget {
  src = "";
  currentTime = 0;
  duration = NaN;
  paused = true;
  readyState = 0;
  error = null;
  play() {
    this.paused = false;
    return Promise.resolve();
  }
  pause() {
    this.paused = true;
  }
  load() {}
}

function links(id: string): MediaLinks {
  return {
    track_id: id,
    expires_at: new Date(Date.now() + 12 * 3600_000).toISOString(),
    ttl_s: 12 * 3600,
    cover: null,
    lossy: false,
    flac: { url: `/media/${id}.flac`, bytes: 1, content_type: "audio/flac", sample_rate: 44100, bits_per_sample: 16, sha256: "x" },
    mp3: { url: `/media/${id}.mp3`, bytes: 1, content_type: "audio/mpeg", bitrate_kbps: 192, sha256: "y" },
  };
}

function rig() {
  const el = new FakeAudio();
  const notes: { message: string; action?: { label: string; run(): void } }[] = [];
  const device: DeviceHooks = { quality: () => "lossless", connection: () => undefined, recordFailedProbe() {} };
  const p = new Player({
    fetchLinks: async (id) => links(id),
    device,
    now: () => Date.now(),
    fetchMeta: async (id) => ({ title: `Title ${id}`, album: "" }),
    notice: (message, action) => void notes.push({ message, ...(action ? { action } : {}) }),
  });
  p.attach(el as unknown as HTMLAudioElement);
  const flush = () => new Promise((r) => setTimeout(r, 0));
  return { p, el, notes, flush };
}

/** Queue a..f with `cur` current and the first `qn` entries after it as an explicit "play next" run. */
function seeded(cur = "b", qn = 0) {
  const r = rig();
  r.p.play(cur, ["a", "b", "c", "d", "e", "f"]);
  const ids = ["c", "d", "e", "f"];
  // playNext appends to the run, so forward order keeps the queue abcdef
  for (const id of ids.slice(0, qn)) r.p.playNext(id);
  return r;
}

const q = (p: Player) => p.state.queue.join("");

describe("remove(i)", () => {
  // queue abcdef, current b (index 1), qn = size of the "play next" run c.. after it
  const cases: { name: string; qn: number; i: number; want: string; index: number; ok: boolean }[] = [
    { name: "upcoming entry", qn: 0, i: 3, want: "abcef", index: 1, ok: true },
    { name: "first upcoming entry", qn: 0, i: 2, want: "abdef", index: 1, ok: true },
    { name: "history entry shifts the index", qn: 0, i: 0, want: "bcdef", index: 0, ok: true },
    { name: "the current track is refused", qn: 0, i: 1, want: "abcdef", index: 1, ok: false },
    { name: "out of range is refused", qn: 0, i: 9, want: "abcdef", index: 1, ok: false },
    { name: "negative is refused", qn: 0, i: -1, want: "abcdef", index: 1, ok: false },
    { name: "an entry of the play-next run", qn: 2, i: 2, want: "abdef", index: 1, ok: true },
  ];
  for (const c of cases) {
    test(c.name, () => {
      const { p } = seeded("b", c.qn);
      const got = p.remove(c.i);
      expect(got).toBe(c.ok);
      expect(q(p)).toBe(c.want);
      expect(p.state.index).toBe(c.index);
      expect(p.state.queue[p.state.index]).toBe("b");
    });
  }

  test("removing the next track makes next() skip it, and the run shrinks", () => {
    const { p } = seeded("b", 2); // b, c, d (run), e, f
    expect(p.remove(2)).toBe(true); // c
    p.next();
    expect(p.state.trackId).toBe("d");
    // the run counter followed: after d, the next entry is not part of a run any more
    p.playNext("f");
    expect(q(p)).toBe("abdfe");
  });

  test("announces", () => {
    const { p, notes } = rig();
    p.play("b", ["a", "b", "c"]);
    p.remove(2);
    return new Promise((r) => setTimeout(r, 0)).then(() => expect(notes.map((n) => n.message)).toContain("Removed from the queue: Title c"));
  });
});

describe("move(from, to)", () => {
  const cases: { name: string; qn: number; from: number; to: number; want: string; ok: boolean }[] = [
    { name: "down", qn: 0, from: 2, to: 3, want: "abdcef", ok: true },
    { name: "up", qn: 0, from: 4, to: 3, want: "abcedf", ok: true },
    { name: "to the end", qn: 0, from: 2, to: 5, want: "abdefc", ok: true },
    { name: "the first upcoming entry cannot pass the current track", qn: 0, from: 2, to: 1, want: "abcdef", ok: false },
    { name: "the current track cannot move", qn: 0, from: 1, to: 3, want: "abcdef", ok: false },
    { name: "history cannot move", qn: 0, from: 0, to: 3, want: "abcdef", ok: false },
    { name: "same place", qn: 0, from: 3, to: 3, want: "abcdef", ok: false },
    { name: "out of range", qn: 0, from: 2, to: 9, want: "abcdef", ok: false },
    { name: "out of the play-next run", qn: 2, from: 2, to: 4, want: "abdecf", ok: true },
  ];
  for (const c of cases) {
    test(c.name, () => {
      const { p } = seeded("b", c.qn);
      expect(p.move(c.from, c.to)).toBe(c.ok);
      expect(q(p)).toBe(c.want);
      expect(p.state.queue[p.state.index]).toBe("b");
      expect(p.state.index).toBe(1);
    });
  }

  test("moving a rest entry to the front of the run plays it next", () => {
    const { p } = seeded("b", 1); // b, c(run), d, e, f
    expect(p.move(5, 2)).toBe(true);
    expect(q(p)).toBe("abfcde");
    p.next();
    expect(p.state.trackId).toBe("f");
  });

  test("the run counter follows moves in and out of it", () => {
    const { p } = seeded("b", 2); // b, c, d (run), e, f
    expect(p.move(4, 2)).toBe(true); // e -> right after b, inside the run: run = e c d
    expect(q(p)).toBe("abecdf");
    p.playNext("z"); // joins the end of the run of three
    expect(q(p)).toBe("abecdzf");
  });
});

describe("clearUpcoming()", () => {
  test("keeps history and the current track, drops the rest and the source", async () => {
    const { p } = rig();
    let asked = 0;
    p.play("b", ["a", "b", "c", "d"], { more: async () => (asked++, ["x"]) });
    expect(p.clearUpcoming()).toBe(true);
    expect(q(p)).toBe("ab");
    expect(p.hasNext).toBe(false);
    await new Promise((r) => setTimeout(r, 0));
    expect(q(p)).toBe("ab"); // the source does not refill it
    expect(asked).toBe(0);
    expect(p.clearUpcoming()).toBe(false);
  });
  test("a following playNext starts a new run at the front", () => {
    const { p } = seeded("b", 1);
    p.clearUpcoming();
    p.playNext("z");
    expect(q(p)).toBe("abz");
  });
});

describe("enqueue(id) is add-to-queue", () => {
  test("appends a new track", () => {
    const { p } = seeded("b");
    p.enqueue("z");
    expect(q(p)).toBe("abcdefz");
  });
  test("moves an already queued track to the end", () => {
    const { p } = seeded("b");
    p.enqueue("c");
    expect(q(p)).toBe("abdefc");
    expect(p.state.index).toBe(1);
  });
  test("a history entry moved to the end shifts the index", () => {
    const { p } = seeded("b");
    p.enqueue("a");
    expect(q(p)).toBe("bcdefa");
    expect(p.state.index).toBe(0);
    expect(p.state.trackId).toBe("b");
  });
  test("the current track is left alone", () => {
    const { p } = seeded("b");
    p.enqueue("b");
    expect(q(p)).toBe("abcdef");
  });
  test("an entry leaving the play-next run shrinks it", () => {
    const { p } = seeded("b", 2); // b, c, d | e, f
    p.enqueue("c");
    p.playNext("z"); // run is now d, then z joins after it
    expect(q(p)).toBe("abdzef" + "c");
  });
  test("announces", async () => {
    const { p, notes, flush } = rig();
    p.play("a", ["a", "b"]);
    p.enqueue("z");
    await flush();
    expect(notes.at(-1)?.message).toBe("Added to queue: Title z");
    p.enqueue("b");
    await flush();
    expect(notes.at(-1)?.message).toBe("Moved to the end of the queue: Title b");
  });
});

describe("source", () => {
  test("PlayOptions.source feeds state.source and survives a reposition within the queue", () => {
    const { p } = rig();
    p.play("a", ["a", "b", "c"], { source: { label: "New since Tue", href: "/new" } });
    expect(p.state.source).toEqual({ label: "New since Tue", href: "/new" });
    p.play("c");
    expect(p.state.source).toEqual({ label: "New since Tue", href: "/new" });
    p.play("z", ["z"]);
    expect(p.state.source).toBeNull();
  });
});

describe("queue replacement and undo", () => {
  test("play with undo over a loaded queue offers Undo, which puts the queue back", async () => {
    const { p, notes, el } = rig();
    p.play("b", ["a", "b", "c", "d"], { source: { label: "Library", href: "/" } });
    el.currentTime = 7;
    p.state.current = 7;
    p.play("z", ["z", "y"], { undo: true });
    expect(q(p)).toBe("zy");
    const n = notes.at(-1)!;
    expect(n.message).toBe("Queue replaced");
    expect(n.action?.label).toBe("Undo");
    n.action!.run();
    expect(q(p)).toBe("abcd");
    expect(p.state.trackId).toBe("b");
    expect(p.state.index).toBe(1);
    expect(p.state.source).toEqual({ label: "Library", href: "/" });
    expect(p.state.current).toBe(7);
  });
  test("no offer for the first queue, a reposition inside the queue, or without the flag", () => {
    const { p, notes } = rig();
    p.play("a", ["a", "b"], { undo: true });
    p.play("b", undefined, { undo: true });
    p.play("q", ["q"]);
    expect(notes.map((n) => n.message)).toEqual([]);
  });
});
