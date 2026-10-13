import { describe, expect, test } from "bun:test";
import { createCountdown, createReveal, formatCountdown, mcpTemplates } from "./humanView";

describe("mcpTemplates", () => {
  test("reference ${MM_TOKEN} and never embed a token", () => {
    const t = mcpTemplates("https://mm.example.com");
    expect(t.claude).toContain('--header "Authorization: Bearer ${MM_TOKEN}"');
    expect(t.claude).toContain("https://mm.example.com/mcp");
    expect(t.stdio).toContain("${MM_TOKEN}");
    expect(t.stdio).toContain("mcp-stdio");
    expect(t.stdio).not.toMatch(/mmp_/);
    expect(t.claude).not.toMatch(/mmp_/);
  });
  test("trailing slash is trimmed", () => {
    expect(mcpTemplates("https://mm.example.com/").claude).toContain("https://mm.example.com/mcp");
  });
});

describe("reveal", () => {
  test("shows a token once and clear() forgets it", () => {
    const r = createReveal();
    expect(r.token).toBe("");
    r.show("mmp_secret");
    expect(r.token).toBe("mmp_secret");
    r.clear();
    expect(r.token).toBe("");
  });
  test("showing a second token replaces the first", () => {
    const r = createReveal();
    r.show("a");
    r.show("b");
    expect(r.token).toBe("b");
  });
});

describe("countdown", () => {
  test("formatCountdown", () => {
    expect(formatCountdown(300_000)).toBe("5:00");
    expect(formatCountdown(61_000)).toBe("1:01");
    expect(formatCountdown(999)).toBe("0:01");
    expect(formatCountdown(0)).toBe("0:00");
    expect(formatCountdown(-5)).toBe("0:00");
  });
  test("reaches 0:00 and expires", () => {
    let now = 0;
    const c = createCountdown(300_000, () => now);
    expect(c.label(now)).toBe("5:00");
    expect(c.expired(now)).toBe(false);
    now = 299_000;
    expect(c.label(now)).toBe("0:01");
    now = 300_000;
    expect(c.label(now)).toBe("0:00");
    expect(c.expired(now)).toBe(true);
  });
});
