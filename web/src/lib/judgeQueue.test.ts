import { describe, expect, test } from "bun:test";
import {
  ADVANCE_MS,
  UNDO_MS,
  advanceAfterRating,
  append,
  cardView,
  chorusStart,
  currentTake,
  isDone,
  mediaMask,
  moveTo,
  needsMore,
  positionLabel,
  ratedMessage,
  skip,
  sourceFromSearch,
  sourceHref,
  sourceLabel,
  startQueue,
  takeLabel,
  undoRating,
  type JudgeTake,
} from "./judgeQueue";
import { songLine } from "./songLine";

const take = (id: string, over: Partial<JudgeTake> = {}): JudgeTake => ({
  id,
  title: `Title ${id}`,
  duration_s: 245,
  stars: null,
  status: "draft",
  song_id: "s1",
  song_title: "Night Drive",
  song_angle: "remake-long",
  is_pick: false,
  ...over,
});
const five = () => ["a", "b", "c", "d", "e"].map((id) => take(id));

describe("timing", () => {
  test("700 ms to advance, 5 s of Undo", () => {
    expect(ADVANCE_MS).toBe(700);
    expect(UNDO_MS).toBe(5000);
  });
});

describe("advance", () => {
  test("a rating moves the card on and fills the undo slot", () => {
    let s = startQueue(five());
    expect(currentTake(s)?.id).toBe("a");
    s = advanceAfterRating(s, { id: "a", prev: null, index: 0 });
    expect(currentTake(s)?.id).toBe("b");
    expect(s.undo).toEqual({ id: "a", prev: null, index: 0 });
    expect(s.rated).toBe(1);
  });

  test("five ratings in a row use the list up without leaving it", () => {
    let s = startQueue(five());
    for (const [i, t] of five().entries()) {
      expect(currentTake(s)?.id).toBe(t.id);
      s = advanceAfterRating(s, { id: t.id, prev: null, index: i });
    }
    expect(isDone(s)).toBe(true);
    expect(currentTake(s)).toBeNull();
    expect(s.rated).toBe(5);
    expect(positionLabel(s)).toBe("5 of 5");
  });

  test("skip moves on without rating and keeps the last undo", () => {
    let s = startQueue(five());
    s = advanceAfterRating(s, { id: "a", prev: 2, index: 0 });
    s = skip(s);
    expect(currentTake(s)?.id).toBe("c");
    expect(s.undo?.id).toBe("a");
    expect(s.rated).toBe(1);
  });

  test("skip stops at the end of the list", () => {
    let s = startQueue([take("a")]);
    s = skip(skip(s));
    expect(s.index).toBe(1);
    expect(isDone(s)).toBe(true);
  });

  test("the card follows the player to another take of the list", () => {
    const s = startQueue(five());
    expect(currentTake(moveTo(s, "d"))?.id).toBe("d");
    expect(moveTo(s, "zzz")).toBe(s);
  });

  test("position label and the page-ahead rule", () => {
    const s = startQueue(five());
    expect(positionLabel(s)).toBe("1 of 5");
    expect(positionLabel(s, true)).toBe("1 of 5+");
    expect(needsMore(s, true)).toBe(false);
    expect(needsMore(advanceAfterRating(advanceAfterRating(s, { id: "a", prev: null, index: 0 }), { id: "b", prev: null, index: 1 }), true)).toBe(true);
    expect(needsMore(s, false)).toBe(false);
  });

  test("append ignores takes already in the list", () => {
    const s = startQueue(five());
    const t = append(s, [take("e"), take("f")]);
    expect(t.takes.map((x) => x.id)).toEqual(["a", "b", "c", "d", "e", "f"]);
    expect(append(t, [take("f")])).toBe(t);
  });
});

describe("undo", () => {
  test("goes back to the rated take and returns the rating to restore", () => {
    let s = startQueue(five());
    s = advanceAfterRating(s, { id: "a", prev: 2, index: 0 });
    s = advanceAfterRating(s, { id: "b", prev: null, index: 1 });
    const r = undoRating(s);
    expect(r.entry).toEqual({ id: "b", prev: null, index: 1 });
    expect(currentTake(r.state)?.id).toBe("b");
    expect(r.state.undo).toBeNull();
    expect(r.state.rated).toBe(1);
  });

  test("only the latest rating can be undone", () => {
    const s = advanceAfterRating(startQueue(five()), { id: "a", prev: 4, index: 0 });
    const once = undoRating(s);
    expect(once.entry?.prev).toBe(4);
    const twice = undoRating(once.state);
    expect(twice.entry).toBeNull();
    expect(twice.state).toBe(once.state);
  });

  test("undo after a skip still returns to the rated take", () => {
    let s = advanceAfterRating(startQueue(five()), { id: "a", prev: null, index: 0 });
    s = skip(s);
    expect(currentTake(undoRating(s).state)?.id).toBe("a");
  });

  test("the rated message names no take", () => {
    expect(ratedMessage(5)).toBe("Rated 5 stars");
    expect(ratedMessage(1)).toBe("Rated 1 star");
    expect(ratedMessage(null)).toBe("Rating cleared");
  });
});

describe("blind identity scrub", () => {
  test("labels run Take A..Z, AA", () => {
    expect([0, 1, 2, 25, 26, 27].map(takeLabel)).toEqual(["Take A", "Take B", "Take C", "Take Z", "Take AA", "Take AB"]);
  });

  test("a blind card carries no title, Song, angle, cover or waveform", () => {
    const t = take("a", { title: "Secret Title", song_title: "Secret Song", song_angle: "secret-angle", is_pick: true });
    const v = cardView(t, 1, true);
    expect(v).toEqual({ title: "Take B", songLine: "", cover: false, waveform: false, openTake: false });
    expect(JSON.stringify(v)).not.toMatch(/Secret|secret/);
  });

  test("an open card shows the title and Song / angle", () => {
    const v = cardView(take("a"), 0, false);
    expect(v.title).toBe("Title a");
    expect(v.songLine).toBe("Night Drive · remake-long");
    expect(v.cover && v.waveform && v.openTake).toBe(true);
  });

  test("the Media Session mask is the position label, only in blind mode and only for takes of the list", () => {
    const ids = ["a", "b", "c"];
    expect(mediaMask(true, ids, "b")).toBe("Take B");
    expect(mediaMask(false, ids, "b")).toBeNull();
    expect(mediaMask(true, ids, "other")).toBeNull();
    expect(mediaMask(true, ids, "")).toBeNull();
  });
});

describe("chorus", () => {
  test("the first timed chorus, by kind or label", () => {
    expect(chorusStart([{ kind: "verse", start_s: 10 }, { kind: "chorus", start_s: 48 }, { kind: "chorus", start_s: 90 }])).toBe(48);
    expect(chorusStart([{ kind: "section", label: "Chorus 1", start_s: 33.5 }])).toBe(33.5);
  });
  test("none when absent or untimed (the button is hidden)", () => {
    expect(chorusStart([])).toBeNull();
    expect(chorusStart([{ kind: "verse", start_s: 0 }])).toBeNull();
    expect(chorusStart([{ kind: "chorus", start_s: null }])).toBeNull();
  });
});

describe("source", () => {
  test("default, New inbox, Song and Muse", () => {
    expect(sourceFromSearch("")).toEqual({ kind: "unrated" });
    expect(sourceFromSearch("?from=new")).toEqual({ kind: "new" });
    expect(sourceFromSearch("?song=s9")).toEqual({ kind: "song", id: "s9" });
    expect(sourceFromSearch("?muse=m1")).toEqual({ kind: "muse", id: "m1" });
    expect(sourceFromSearch("?from=weird")).toEqual({ kind: "unrated" });
  });
  test("href round-trips", () => {
    for (const q of ["", "?from=new", "?song=s%2F9", "?muse=m1"]) {
      const s = sourceFromSearch(q);
      expect(sourceFromSearch(sourceHref(s).replace("/judge", ""))).toEqual(s);
    }
  });
  test("labels", () => {
    expect(sourceLabel({ kind: "new" })).toBe("Judge new");
    expect(sourceLabel({ kind: "song", id: "s" }, "Night Drive")).toBe("Judge Night Drive");
    expect(sourceLabel({ kind: "unrated" })).toBe("Judge new unrated");
  });
});

describe("songLine", () => {
  test("is null-safe", () => {
    expect(songLine({ song_title: "Night Drive", song_angle: "remake-long" })).toBe("Night Drive · remake-long");
    expect(songLine({ song_title: "Night Drive", song_angle: null })).toBe("Night Drive");
    expect(songLine({ song_title: null, song_angle: "remake-long" })).toBe("remake-long");
    expect(songLine({ song_title: null, song_angle: null })).toBe("");
    expect(songLine({})).toBe("");
    expect(songLine({ song_title: "  ", song_angle: "" })).toBe("");
  });
});
