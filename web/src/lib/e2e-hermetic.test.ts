import { expect, test } from "bun:test";
import config from "../../playwright.config";

// Every worker serves its own seeded server (e2e/fixtures.ts), so projects are independent: a project dependency would
// re-introduce the phases that CI sharding (--shard=i/N over the whole test list) cannot split.
test("no Playwright project depends on another", () => {
  const projects = config.projects ?? [];
  expect(projects.length).toBeGreaterThan(10);
  expect(projects.filter((p) => (p.dependencies ?? []).length > 0).map((p) => p.name)).toEqual([]);
});

test("project names are unique", () => {
  const names = (config.projects ?? []).map((p) => p.name);
  expect(new Set(names).size).toBe(names.length);
});
