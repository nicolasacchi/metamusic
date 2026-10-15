import { expect, test } from "bun:test";
import { spawnSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { missingOperations, servedUnion, uiOperations } from "../../e2e/coverage";

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
  const fixture = resolve(import.meta.dirname, "../../e2e/testdata/openapi.json");
  expect(uiOperations(fixture)).toEqual(["postOp", "routeOp"]);
});

test("servedUnion merges the shards' lists", () => {
  const dir = mkdtempSync(join(tmpdir(), "served-"));
  try {
    writeFileSync(join(dir, "1.json"), JSON.stringify(["b", "a"]));
    writeFileSync(join(dir, "2.json"), JSON.stringify(["b", "c"]));
    expect(servedUnion([join(dir, "1.json"), join(dir, "2.json")])).toEqual(["a", "b", "c"]);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("served-check fails on a missing operation and on a missing shard list", () => {
  const dir = mkdtempSync(join(tmpdir(), "served-"));
  try {
    const script = resolve(import.meta.dirname, "../../e2e/served-check.ts");
    const f = join(dir, "1.json");
    writeFileSync(f, JSON.stringify(["whoami"]));
    const run = (...args: string[]) => spawnSync("bun", [script, ...args], { encoding: "utf8" });
    expect(run("1", f).status).toBe(1); // every other UI operation is missing
    expect(run("2", f).status).toBe(2); // one list expected too many
    writeFileSync(f, JSON.stringify(uiOperations()));
    expect(run("1", f).status).toBe(0);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});
