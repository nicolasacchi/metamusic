import { describe, expect, test } from "bun:test";
import { armedWord } from "./armed";
import { backendText } from "./engineMatrix";
import type { EngineView } from "./engines";

describe("armedWord: absent means unknown, never disarmed", () => {
  test("only an explicit false is disarmed", () => {
    expect(armedWord(true)).toBe("armed");
    expect(armedWord(false)).toBe("disarmed");
    expect(armedWord(undefined)).toBe("unknown");
    expect(armedWord(null)).toBe("unknown");
  });
  test("an engine backend without armed (an Editor's view) is not shown as Disarmed", () => {
    const b = { name: "b", enabled: true, tripped: false, trip_reason: null, disabled_reason: null, cold_start_s: 40, max_runtime_s: 600 };
    const e = (backend: unknown) => ({ name: "a", backend }) as EngineView;
    expect(backendText(e(b))).toBe("Cold start 40 s");
    expect(backendText(e({ ...b, armed: false }))).toBe("Disarmed, cold start 40 s");
    expect(backendText(e({ ...b, armed: true }))).toBe("Armed, cold start 40 s");
  });
});
