import { describe, expect, test } from "bun:test";
import { createCovers } from "./coverLinks";

const links = (id: string) => ({ cover: { url_512: `/c/${id}`, url_1024: `/c/${id}-big`, expires_at: "2099-01-01T00:00:00Z" } });

describe("cover link requests", () => {
  test("never more than the cap in flight, and every tile still gets its cover", async () => {
    let active = 0;
    let peak = 0;
    const covers = createCovers({
      links: async (id) => {
        active++;
        peak = Math.max(peak, active);
        await new Promise((r) => setTimeout(r, 5));
        active--;
        return links(id);
      },
      local: async () => null,
      online: () => true,
      now: () => Date.now(),
      maxConcurrent: 3,
    });
    const ids = Array.from({ length: 18 }, (_, i) => `t${i}`);
    const got = await Promise.all(ids.map((id) => covers.get(id)));
    expect(peak).toBe(3);
    expect(got.map((c) => c?.url512)).toEqual(ids.map((id) => `/c/${id}`));
  });

  test("a failing request frees its slot", async () => {
    let calls = 0;
    const covers = createCovers({
      links: async (id) => {
        calls++;
        if (id === "bad") throw new Error("boom");
        return links(id);
      },
      local: async () => null,
      online: () => true,
      now: () => Date.now(),
      maxConcurrent: 1,
    });
    const [a, b] = await Promise.all([covers.get("bad"), covers.get("ok")]);
    expect(a).toBeNull();
    expect(b?.url512).toBe("/c/ok");
    expect(calls).toBe(2);
  });
});
