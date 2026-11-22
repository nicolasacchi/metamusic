import { describe, expect, test } from "bun:test";
import type { components } from "../api/schema";
import { activeLine, activeWord, cerText, humanMark, lineKey, lineTitle, markBody, mergePages, sectionsOf, toggleMark, wordTitle, type Line, type Page } from "./karaoke";

type Word = components["schemas"]["DictionWord"];

const word = (i: number, start: number | null, end: number | null, match: Word["match"] = "exact", over: Partial<Word> = {}): Word => ({
  i,
  text: `w${i}`,
  start_s: start,
  end_s: end,
  timing: start === null ? null : "heard",
  match,
  heard: "",
  ...over,
});
const line = (section: number, idx: number, start: number | null, end: number | null, over: Partial<Line> = {}): Line => ({
  section_idx: section,
  line_idx: idx,
  text: `riga ${section}.${idx}`,
  start_s: start,
  end_s: end,
  timing: start === null ? null : "heard",
  cer: 0.1,
  class: "clear",
  class_source: "asr",
  mark: { human: null, critic: null },
  words: [],
  ...over,
});
const page = (lines: Line[], over: Partial<Page> = {}): Page => ({
  track_id: "t1",
  basis: "asr",
  lang: "it",
  thresholds: { clear: 0.15, mixed: 0.4 },
  lines,
  lexicon_hits: [],
  truncated: false,
  next_cursor: null,
  ...over,
});

describe("activeLine", () => {
  const lines = [line(0, 0, 1, 3), line(0, 1, 3, 6), line(1, 0, null, null), line(1, 1, 10, 12), line(1, 2, 12, null)];
  test("none before the first line, none in a gap after a line's end, none with no playhead", () => {
    expect(activeLine(lines, 0.5)).toBe(-1);
    expect(activeLine(lines, 7)).toBe(-1);
    expect(activeLine(lines, null)).toBe(-1);
    expect(activeLine([], 4)).toBe(-1);
  });
  test("the line whose time holds the playhead; a shared boundary belongs to the later line", () => {
    expect(activeLine(lines, 1)).toBe(0);
    expect(activeLine(lines, 2.9)).toBe(0);
    expect(activeLine(lines, 3)).toBe(1);
    expect(activeLine(lines, 11)).toBe(3);
  });
  test("untimed lines are skipped, and a line without an end stays active", () => {
    expect(activeLine(lines, 12.5)).toBe(4);
    expect(activeLine(lines, 400)).toBe(4);
  });
  test("binary search agrees with a scan on many lines", () => {
    const many = Array.from({ length: 500 }, (_, i) => line(0, i, i * 2, i * 2 + 1.5));
    for (const t of [0, 0.7, 1.6, 2, 3.9, 500, 998.2, 999.9, 1001]) {
      const want = many.findIndex((l) => t >= (l.start_s ?? 0) && t < (l.end_s ?? Infinity));
      expect(activeLine(many, t), String(t)).toBe(want);
    }
  });
});

describe("activeWord", () => {
  const l = line(0, 0, 0, 6, { words: [word(0, 0, 1), word(1, 1.2, 2), word(2, null, null, "dropped"), word(3, 3, 4)] });
  test("the last word that has started, only inside its line", () => {
    expect(activeWord(l, 0.5)).toBe(0);
    expect(activeWord(l, 1.1)).toBe(0);
    expect(activeWord(l, 1.5)).toBe(1);
    expect(activeWord(l, 3.5)).toBe(3);
    expect(activeWord(l, 5)).toBe(3);
    expect(activeWord(l, null)).toBe(-1);
  });
  test("an estimated line has no word highlight", () => {
    expect(activeWord({ ...l, timing: "estimated", words: [] }, 2)).toBe(-1);
    expect(activeWord({ ...l, timing: "estimated" }, 2)).toBe(-1);
  });
});

describe("marks", () => {
  const l = line(2, 3, 0, 1);
  test("humanMark reads the person's mark, none when absent", () => {
    expect(humanMark(l)).toBe("none");
    expect(humanMark({ ...l, mark: { human: "clear", critic: "unclear" } })).toBe("clear");
    expect(humanMark({ ...l, mark: { human: "none", critic: null } })).toBe("none");
  });
  test("toggleMark: pressing the active mark withdraws it", () => {
    expect(toggleMark(l, "unclear")).toBe("unclear");
    expect(toggleMark({ ...l, mark: { human: "unclear", critic: null } }, "unclear")).toBe("none");
    expect(toggleMark({ ...l, mark: { human: "unclear", critic: null } }, "clear")).toBe("clear");
  });
  test("markBody addresses the line by its indexes", () => {
    expect(markBody(l, "clear")).toEqual({ section_idx: 2, line_idx: 3, mark: "clear" });
    expect(lineKey(l)).toBe("2:3");
  });
});

describe("text", () => {
  test("cerText rounds to a whole percent and says unknown for null", () => {
    expect(cerText(0.126)).toBe("13 % off");
    expect(cerText(0)).toBe("0 % off");
    expect(cerText(null)).toBe("not measured");
  });
  test("lineTitle states the class, who decided it and the timing", () => {
    expect(lineTitle(line(0, 0, 1, 2))).toBe("clear (recogniser) · 10 % off");
    expect(lineTitle(line(0, 0, 1, 2, { class: "unclear", class_source: "human" }))).toBe("unclear (you marked it) · 10 % off");
    expect(lineTitle(line(0, 0, 1, 2, { timing: "estimated", cer: null, class: "unknown" }))).toBe("unknown (recogniser) · not measured · time estimated");
  });
  test("wordTitle names what was heard instead", () => {
    expect(wordTitle(word(0, 0, 1, "miss", { heard: "pane" }))).toBe("heard as pane");
    expect(wordTitle(word(0, 0, 1, "close", { heard: "pane" }))).toBe("close: heard pane");
    expect(wordTitle(word(0, null, null, "dropped"))).toBe("not heard");
    expect(wordTitle(word(0, 0, 1, "exact"))).toBe("");
    expect(wordTitle(word(0, 0, 1, "exact", { timing: "interpolated" }))).toBe("time interpolated");
  });
});

describe("pages and sections", () => {
  test("sectionsOf groups consecutive lines of a section in order", () => {
    const s = sectionsOf([line(0, 0, 0, 1), line(0, 1, 1, 2), line(1, 0, 3, 4), line(1, 1, 4, 5), line(2, 0, 6, 7)]);
    expect(s.map((x) => [x.section_idx, x.lines.length, x.first])).toEqual([
      [0, 2, 0],
      [1, 2, 2],
      [2, 1, 4],
    ]);
  });
  test("mergePages appends lines, takes the new cursor and keeps the first page's lexicon hits", () => {
    const a = page([line(0, 0, 0, 1)], { truncated: true, next_cursor: "c1", lexicon_hits: [{ word: "pane", typical_heard: [], attempts: 3, mangled: 2 }] });
    const b = page([line(0, 1, 1, 2)], { lexicon_hits: [] });
    const m = mergePages(a, b);
    expect(m.lines.map(lineKey)).toEqual(["0:0", "0:1"]);
    expect(m.next_cursor).toBeNull();
    expect(m.truncated).toBe(false);
    expect(m.lexicon_hits.map((h) => h.word)).toEqual(["pane"]);
  });
  test("mergePages replaces a line that comes twice (a reload after a mark)", () => {
    const a = page([line(0, 0, 0, 1, { class: "clear" })]);
    const b = page([line(0, 0, 0, 1, { class: "unclear", class_source: "human" })]);
    const m = mergePages(a, b);
    expect(m.lines).toHaveLength(1);
    expect(m.lines[0]!.class).toBe("unclear");
  });
});
