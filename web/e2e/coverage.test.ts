import { expect, test } from "bun:test";
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
