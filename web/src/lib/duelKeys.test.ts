import { describe, expect, test } from "bun:test";
import { keyAction, type KeyLike } from "./duelKeys";

function ev(key: string, extra: Partial<KeyLike> = {}): KeyLike & { prevented: boolean } {
  const e = {
    key,
    repeat: false,
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    target: null as unknown,
    prevented: false,
    preventDefault() {
      e.prevented = true;
    },
    ...extra,
  };
  return e as KeyLike & { prevented: boolean };
}

describe("keyAction", () => {
  test("mappings", () => {
    const cases: [string, unknown][] = [
      ["a", { type: "select", slot: "a" }],
      ["b", { type: "select", slot: "b" }],
      [" ", { type: "toggle" }],
      ["p", { type: "playpause" }],
      ["r", { type: "restart" }],
      ["1", { type: "choose", verdict: "a" }],
      ["2", { type: "choose", verdict: "b" }],
      ["3", { type: "choose", verdict: "tie" }],
      ["4", { type: "choose", verdict: "both_bad" }],
    ];
    for (const [k, want] of cases) {
      const e = ev(k);
      expect(keyAction(e, { chosen: null })).toEqual(want as never);
      expect(e.prevented).toBe(true);
    }
  });
  test("uppercase letters map too", () => {
    expect(keyAction(ev("A"), { chosen: null })).toEqual({ type: "select", slot: "a" });
  });
  test("Enter submits only with a chosen verdict", () => {
    const none = ev("Enter");
    expect(keyAction(none, { chosen: null })).toBeNull();
    expect(none.prevented).toBe(false);
    expect(keyAction(ev("Enter"), { chosen: "tie" })).toEqual({ type: "submit" });
  });
  test("repeat and modifiers are ignored and not prevented", () => {
    for (const extra of [{ repeat: true }, { ctrlKey: true }, { metaKey: true }, { altKey: true }]) {
      const e = ev("a", extra);
      expect(keyAction(e, { chosen: null })).toBeNull();
      expect(e.prevented).toBe(false);
    }
  });
  test("form targets are ignored", () => {
    for (const tagName of ["INPUT", "TEXTAREA", "SELECT"]) {
      const e = ev("a", { target: { tagName } });
      expect(keyAction(e, { chosen: null })).toBeNull();
      expect(e.prevented).toBe(false);
    }
    const ce = ev("a", { target: { tagName: "DIV", isContentEditable: true } });
    expect(keyAction(ce, { chosen: null })).toBeNull();
  });
  test("unknown keys are ignored", () => {
    const e = ev("x");
    expect(keyAction(e, { chosen: null })).toBeNull();
    expect(e.prevented).toBe(false);
  });
});
