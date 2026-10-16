import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { headerOf, KNOWN_TAGS, MAX_QUARANTINED, matrixIgnore, parseHeader, quarantineFilter, specFiles } from "../../e2e/matrix";

const e2e = join(import.meta.dir, "../../e2e");

test("parseHeader reads tags and the reason, and refuses a header without one", () => {
  expect(parseHeader("// matrix: @mobile @firefox because it plays audio\nimport x")).toEqual({ tags: ["mobile", "firefox"], reason: "it plays audio" });
  expect(parseHeader("// matrix: chrome-only because data")).toEqual({ tags: [], reason: "data" });
  expect(parseHeader("// matrix: @mobile\n")).toBeUndefined();
  expect(parseHeader("// matrix: @mobile because \n")).toBeUndefined();
});

test("every spec file declares its device matrix with a reason and only known tags", () => {
  const files = specFiles();
  expect(files.length).toBeGreaterThan(25);
  for (const f of files) {
    const h = headerOf(f);
    expect(h, `${f}: missing "// matrix: <tags|chrome-only> because <why>" header`).toBeDefined();
    for (const t of h!.tags) expect(KNOWN_TAGS.has(t), `${f}: unknown tag @${t}`).toBe(true);
    const line = readFileSync(join(e2e, f), "utf8").split("\n").find((l) => l.startsWith("// matrix:"))!;
    expect(line.includes("chrome-only") === (h!.tags.length === 0), `${f}: "chrome-only" iff no tags`).toBe(true);
  }
});

test("the default matrix prunes specs per device and the full matrix prunes nothing", () => {
  const saved = process.env.MM_E2E_MATRIX;
  try {
    delete process.env.MM_E2E_MATRIX;
    expect(matrixIgnore("mobile").length).toBeGreaterThan(0);
    expect(matrixIgnore("mobile")).toContain("**/m1-import.spec.ts");
    expect(matrixIgnore("mobile")).not.toContain("**/m1-player.spec.ts");
    expect(matrixIgnore("android-firefox")).not.toContain("**/m3-autoadvance.spec.ts");
    process.env.MM_E2E_MATRIX = "full";
    for (const k of ["mobile", "firefox", "android-firefox"] as const) expect(matrixIgnore(k)).toEqual([]);
  } finally {
    if (saved === undefined) delete process.env.MM_E2E_MATRIX;
    else process.env.MM_E2E_MATRIX = saved;
  }
});

test("quarantine: skipped by default, runs with =1, alone with =only", () => {
  const saved = process.env.MM_E2E_QUARANTINE;
  try {
    delete process.env.MM_E2E_QUARANTINE;
    expect(quarantineFilter().grepInvert?.source).toBe("@quarantine");
    process.env.MM_E2E_QUARANTINE = "1";
    expect(quarantineFilter()).toEqual({});
    process.env.MM_E2E_QUARANTINE = "only";
    expect(quarantineFilter().grep?.source).toBe("@quarantine");
  } finally {
    if (saved === undefined) delete process.env.MM_E2E_QUARANTINE;
    else process.env.MM_E2E_QUARANTINE = saved;
  }
});

// A quarantined test carries @quarantine in its title and its file a "// quarantine: <reason, with a link>" comment.
// More than MAX_QUARANTINED quarantined tests fail the build: fix them instead of parking more.
test("quarantined specs state a linked reason and there are at most five tests", () => {
  let total = 0;
  for (const f of specFiles()) {
    const lines = readFileSync(join(e2e, f), "utf8").split("\n");
    const n = lines.filter((l) => !l.trimStart().startsWith("//") && l.includes("@quarantine")).length;
    if (n === 0) continue;
    total += n;
    const reason = lines.find((l) => l.startsWith("// quarantine:"));
    expect(reason, `${f}: @quarantine needs a "// quarantine: <reason> <link>" comment`).toBeDefined();
    expect(/\S{3,}.*(https?:\/\/\S+|#\d+)/.test(reason!.slice("// quarantine:".length)), `${f}: the quarantine reason must link an issue or run`).toBe(true);
  }
  expect(total, `${total} quarantined tests; fix some (limit ${MAX_QUARANTINED})`).toBeLessThanOrEqual(MAX_QUARANTINED);
});
