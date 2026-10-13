import { describe, expect, test } from "bun:test";
import cases from "./money.cases.json";
import { formatMicroUSD, formatMicroUSDCeil, parseUSDInput } from "./money";

describe("money", () => {
  test("spec examples", () => {
    expect(formatMicroUSD(326217)).toBe("$0.33");
    expect(formatMicroUSD(5800)).toBe("$0.0058");
    expect(formatMicroUSD(0)).toBe("$0.00");
    expect(formatMicroUSDCeil(324999)).toBe("$0.33");
    expect(formatMicroUSDCeil(691731)).toBe("$0.70");
    expect(formatMicroUSDCeil(5801)).toBe("$0.0059");
  });
  test("shared cases agree with Go", () => {
    for (const c of cases.format) {
      expect(formatMicroUSD(c.micro)).toBe(c.display);
      expect(formatMicroUSDCeil(c.micro)).toBe(c.display_ceil);
    }
    for (const c of cases.parse) expect(parseUSDInput(c.in)).toBe(c.micro);
  });
  test("negative values mirror positive", () => {
    expect(formatMicroUSD(-326217)).toBe("-$0.33");
    expect(formatMicroUSDCeil(-691731)).toBe("-$0.70");
  });
  test("parse: integers only, no float artefacts", () => {
    expect(parseUSDInput("0.50")).toBe(500000);
    expect(parseUSDInput("0.30")).toBe(300000);
    expect(parseUSDInput("0.1234567")).toBeNull();
    expect(parseUSDInput(" 1.5 ")).toBe(1500000);
    expect(parseUSDInput("$1")).toBeNull();
    expect(parseUSDInput("1e3")).toBeNull();
    expect(parseUSDInput(".5")).toBeNull();
    expect(parseUSDInput("99999999999999")).toBeNull();
    expect(formatMicroUSD(300000)).not.toContain("0.30000000000000004");
  });
  test("non-integer or unsafe input is rendered safely", () => {
    expect(formatMicroUSD(Number.NaN)).toBe("$0.00");
  });
});
