import { describe, expect, test } from "bun:test";
import { offlineAvailable, registerWorker } from "../api/swRegister";

describe("offline availability", () => {
  test("needs every capability", () => {
    const all = { isSecureContext: true, hasServiceWorker: true, hasCaches: true, hasSubtle: true };
    expect(offlineAvailable(all)).toBe(true);
    for (const k of Object.keys(all) as (keyof typeof all)[]) expect(offlineAvailable({ ...all, [k]: false })).toBe(false);
  });
  test("isSecureContext false: unavailable even with every API present", () => {
    expect(offlineAvailable({ isSecureContext: false, hasServiceWorker: true, hasCaches: true, hasSubtle: true })).toBe(false);
  });
});

describe("registerWorker", () => {
  test("insecure context never calls register", async () => {
    let calls = 0;
    const r = await registerWorker({
      isSecureContext: false,
      navigator: { serviceWorker: { register: async () => (calls++, {} as ServiceWorkerRegistration) } },
      warn: () => {},
    });
    expect(r).toBeNull();
    expect(calls).toBe(0);
  });
  test("no serviceWorker support is a no-op", async () => {
    expect(await registerWorker({ isSecureContext: true, navigator: {}, warn: () => {} })).toBeNull();
  });
  test("registers /sw.js with scope /", async () => {
    const seen: unknown[][] = [];
    await registerWorker({
      isSecureContext: true,
      navigator: { serviceWorker: { register: async (...a: unknown[]) => (seen.push(a), {} as ServiceWorkerRegistration) } },
      warn: () => {},
    });
    expect(seen).toEqual([["/sw.js", { scope: "/" }]]);
  });
  test("a refused registration is logged and ignored", async () => {
    const warned: string[] = [];
    const r = await registerWorker({
      isSecureContext: true,
      navigator: {
        serviceWorker: {
          register: async () => {
            throw new Error("blocked");
          },
        },
      },
      warn: (m) => void warned.push(m),
    });
    expect(r).toBeNull();
    expect(warned).toHaveLength(1);
  });
});
