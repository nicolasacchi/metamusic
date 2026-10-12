import { expect, test } from "bun:test";
import { resolve } from "node:path";
import { missingOperations, uiOperations } from "./coverage";

test("missingOperations lists ops never served", () => {
  expect(missingOperations(["a", "b", "c"], ["b"])).toEqual(["a", "c"]);
  expect(missingOperations(["a"], ["a", "z"])).toEqual([]);
});

test("uiOperations reads committed openapi.json", () => {
  const ids = uiOperations();
  expect(ids).toContain("whoami");
  expect(ids).toContain("ping");
});

test("uiOperations excludes none:* ops and ops without x-mm", () => {
  const fixture = resolve(import.meta.dirname, "testdata/openapi.json");
  expect(uiOperations(fixture)).toEqual(["postOp", "routeOp"]);
});
