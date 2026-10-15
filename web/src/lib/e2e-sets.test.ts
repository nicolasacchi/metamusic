import { expect, test } from "bun:test";
import { E2E_SETS } from "../../e2e/sets";
import config from "../../playwright.config";

test("every Playwright project is in exactly one CI set", () => {
  const all = (config.projects ?? []).map((p) => p.name ?? "").sort();
  const inSets = Object.values(E2E_SETS).flat().sort();
  expect(all.length).toBeGreaterThan(10);
  expect(inSets).toEqual(all);
  expect(new Set(inSets).size).toBe(inSets.length);
});
