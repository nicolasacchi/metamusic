import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { createSecretOnce, onceSubmit } from "./secretOnce";

describe("secretOnce", () => {
  test("holds secrets in memory and clear() forgets them", () => {
    const s = createSecretOnce();
    expect(s.secrets).toEqual({});
    s.show({ apiKey: "mms_abc", password: "pw" });
    expect(s.secrets).toEqual({ apiKey: "mms_abc", password: "pw" });
    s.clear();
    expect(s.secrets).toEqual({});
  });
  test("navigating away clears them (popstate and pagehide), detach stops listening", () => {
    for (const ev of ["popstate", "pagehide"]) {
      const t = new EventTarget();
      const s = createSecretOnce();
      const detach = s.attach(t);
      s.show({ apiKey: "mms_x", password: null });
      t.dispatchEvent(new Event(ev));
      expect(s.secrets).toEqual({});
      s.show({ apiKey: "mms_y", password: null });
      detach();
      t.dispatchEvent(new Event(ev));
      expect(s.secrets).toEqual({ apiKey: "mms_y" });
    }
  });
  test("null secrets (idempotent replay) are dropped", () => {
    const s = createSecretOnce();
    s.show({ apiKey: null, password: null });
    expect(s.secrets).toEqual({});
    expect(s.has()).toBe(false);
  });
  test("the idempotency key is reused on retry and fresh for a new action", async () => {
    const keys: string[] = [];
    const a = onceSubmit(async (k) => {
      keys.push(k);
      return 1;
    });
    await a();
    await a();
    const b = onceSubmit(async (k) => {
      keys.push(k);
      return 1;
    });
    await b();
    expect(keys[0]).toBe(keys[1]!);
    expect(keys[2]).not.toBe(keys[0]!);
  });
  test("the module and its component never use web storage", () => {
    for (const f of ["./secretOnce.ts", "./components/SecretOnce.svelte"]) {
      const src = readFileSync(new URL(f, import.meta.url), "utf8");
      expect(src).not.toMatch(/localStorage|sessionStorage|indexedDB|caches\./);
    }
  });
});
