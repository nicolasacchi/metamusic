// matrix: @mobile @realtime because the duel page has a mobile toggle UI (desktop keyboard tests skip on phones at run time)
// M2b duel specs. Verdicts are immutable and next_duel returns the globally oldest open duel, so every
// spec makes its own duel (API helper or UI) and opens /duels/:id; none asserts a global rank or the
// queue head. Keyboard specs run on the desktop Chrome project, toggle specs on the mobile projects.
import { expect, test, type Page, type Response } from "@playwright/test";
import { crossfades, installAbSpy, readAb } from "./abSpy";
import { trackByTitle } from "./helpers";
import { createDuel, decide, getDuel, ladder, siblings } from "./m2bApi";

const isDesktop = (name: string) => name === "chrome";
const isMobile = (name: string) => name === "chrome-pixel7" || name === "chrome-iphone15";

/** The duel page with its clips decoded: the A/B player shows Play once both are ready. */
async function openDuel(page: Page, id: string): Promise<void> {
  await page.goto(`/duels/${id}`);
  await expect(page.getByTestId("ab-play")).toBeVisible({ timeout: 20_000 });
}

/** In-app navigation without a reload (the shared player must survive). */
async function spaGo(page: Page, to: string): Promise<void> {
  await page.evaluate((t) => {
    history.pushState(null, "", t);
    window.dispatchEvent(new PopStateEvent("popstate"));
  }, to);
}

test.describe("blind round and A/B with parent", () => {
  test("blind round from a lineage lands on a duel that shows only A and B", async ({ page, request }, info) => {
    test.skip(!isDesktop(info.project.name), "one Blind round per run is enough");
    const { root } = await siblings(request);
    await page.goto(`/tracks/${root.id}`);
    const btn = page.getByTestId("blind-round");
    await expect(btn).toBeEnabled();
    await btn.click();
    await expect(page).toHaveURL(/\/duels\/[0-9a-f-]{36}$/, { timeout: 30_000 });
    const card = page.getByTestId("duel-card");
    await expect(card.getByRole("heading", { name: "Blind duel" })).toBeVisible();
    await expect(page.getByTestId("ab-toggle")).toHaveText("A", { timeout: 20_000 });
    await expect(card).not.toContainText("Syn ");
    await expect(card).not.toContainText(/seed/i);
    await expect(card).not.toContainText(/\b(1001|2001)\b/);
  });

  test("non-blind A/B with parent shows titles before the verdict; slot A is the parent", async ({ page, request }, info) => {
    test.skip(!isDesktop(info.project.name), "desktop only");
    const { root, child } = await siblings(request);
    await page.goto(`/tracks/${child.id}`);
    await page.getByTestId("ab-parent").click();
    await expect(page).toHaveURL(/\/duels\/[0-9a-f-]{36}$/, { timeout: 30_000 });
    const id = page.url().split("/").pop()!;
    const d = await getDuel(request, id);
    expect(d.blind).toBe(false);
    expect(d.slots.a.track?.id).toBe(root.id);
    expect(d.slots.b.track?.id).toBe(child.id);
    const card = page.getByTestId("duel-card");
    await expect(card.getByRole("heading", { name: "Duel", exact: true })).toBeVisible();
    await expect(card).toContainText(root.title);
    await expect(card).toContainText(child.title);
    await expect(page.getByTestId("duel-reveal")).toHaveCount(0);
  });
});

test.describe("A/B engine", () => {
  test("synced start and 8 ms crossfade", async ({ page, request }, info) => {
    test.skip(!isDesktop(info.project.name), "keyboard spec: desktop only");
    const { root, child } = await siblings(request);
    const duel = await createDuel(request, root.id, child.id);
    await installAbSpy(page);
    await openDuel(page, duel.id);

    await page.getByTestId("ab-play").click();
    await expect.poll(async () => (await readAb(page)).starts.length).toBe(2);
    const { starts } = await readAb(page);
    expect(starts[0]!.node).not.toBe(starts[1]!.node);
    expect(starts[1]!.when).toBe(starts[0]!.when);
    expect(starts[1]!.offset).toBe(starts[0]!.offset);

    await page.keyboard.press("b");
    await expect.poll(async () => crossfades(await readAb(page)).length).toBe(2);
    let fades = crossfades(await readAb(page));
    for (const f of fades) expect(Math.abs(f.dt - 0.008)).toBeLessThan(1e-6);
    expect(fades.map((f) => f.target).sort()).toEqual([0, 1]);
    expect(fades.find((f) => f.target === 1)!.param).not.toBe(fades.find((f) => f.target === 0)!.param);
    const toB = fades.find((f) => f.target === 1)!.param;
    await expect(page.getByTestId("ab-toggle")).toHaveText("B");

    await page.keyboard.press(" ");
    await expect.poll(async () => crossfades(await readAb(page)).length).toBe(4);
    fades = crossfades(await readAb(page)).slice(2);
    for (const f of fades) expect(Math.abs(f.dt - 0.008)).toBeLessThan(1e-6);
    // back to A: the gain that went to 1 for B now goes to 0
    expect(fades.find((f) => f.param === toB)!.target).toBe(0);
    expect(fades.find((f) => f.param !== toB)!.target).toBe(1);
    await expect(page.getByTestId("ab-toggle")).toHaveText("A");
    expect((await readAb(page)).starts).toHaveLength(2);
  });

  test("keyboard flow: 2 then Enter reveals both titles with B as winner", async ({ page, request }, info) => {
    test.skip(!isDesktop(info.project.name), "keyboard spec: desktop only");
    const { root, child } = await siblings(request);
    const duel = await createDuel(request, root.id, child.id);
    await openDuel(page, duel.id);
    await page.keyboard.press("2");
    await expect(page.getByTestId("verdict-b")).toHaveAttribute("aria-pressed", "true");
    await page.keyboard.press("Enter");
    const reveal = page.getByTestId("duel-reveal");
    await expect(reveal).toBeVisible();
    await expect(reveal).toContainText(root.title);
    await expect(reveal).toContainText(child.title);
    await expect(reveal.getByRole("heading", { name: "B is better" })).toBeVisible();
    // the winner row is the one marked with a star and it is slot B
    const won = reveal.locator("tr.won");
    await expect(won).toHaveCount(1);
    await expect(won.locator("td").first()).toHaveText("B ★");
    const d = await getDuel(request, duel.id);
    expect(d.state).toBe("decided");
    expect(d.human_verdict?.verdict).toBe("b");
    expect(d.human_verdict?.winner_track_id).toBe(d.slots.b.track?.id);
  });

  test("mobile toggle: tap switches with ramps, Tie submits and reveals", async ({ page, request }, info) => {
    test.skip(!isMobile(info.project.name), "mobile only");
    const { root, child } = await siblings(request);
    const duel = await createDuel(request, root.id, child.id);
    await installAbSpy(page);
    await openDuel(page, duel.id);
    await page.getByTestId("ab-play").tap();
    await expect.poll(async () => (await readAb(page)).starts.length).toBe(2);
    await page.getByTestId("ab-toggle").tap();
    await expect.poll(async () => crossfades(await readAb(page)).length).toBe(2);
    await expect(page.getByTestId("ab-toggle")).toHaveText("B");
    await page.getByTestId("verdict-tie").tap();
    await page.getByTestId("verdict-submit").tap();
    const reveal = page.getByTestId("duel-reveal");
    await expect(reveal).toBeVisible();
    await expect(reveal.getByRole("heading", { name: "Tie" })).toBeVisible();
    expect((await getDuel(request, duel.id)).human_verdict?.verdict).toBe("tie");
  });
});

test.describe("ladder", () => {
  test("rows appear in the get_ladder order with a whisker each and the footer counts", async ({ page, request }, info) => {
    test.skip(!isDesktop(info.project.name), "desktop only");
    const { root, child } = await siblings(request);
    const duel = await createDuel(request, root.id, child.id);
    await decide(request, duel.id, "a");
    const table = page.getByTestId("ladder-table");
    // Other projects add verdicts to this lineage concurrently, and the page reads the ladder once when it
    // loads. Each attempt therefore reloads the page first and compares it with an API read taken after the
    // load; a verdict landing in between makes that attempt fail and the next reload picks it up.
    await expect(async () => {
      await page.goto(`/ladder?root=${root.id}`);
      await expect(table).toBeVisible();
      const api = await ladder(request, root.id);
      const rows = table.locator("tbody tr");
      await expect(rows).toHaveCount(api.items.length);
      const titles = await rows.locator("td:nth-child(2) a").allTextContents();
      expect(titles).toEqual(api.items.map((i) => i.title));
      await expect(table.locator("tbody svg")).toHaveCount(api.items.length);
      await expect(page.getByTestId("ladder-footer")).toContainText(`${api.counts.verdicts_human_counted} human verdicts counted`);
    }).toPass({ timeout: 20_000, intervals: [500, 1000] });
    expect((await ladder(request, root.id)).items.length).toBeGreaterThanOrEqual(2);
  });
});

test.describe("clips, player and nav", () => {
  test("clip requests are signed FLAC", async ({ page, request }, info) => {
    test.skip(!isDesktop(info.project.name), "desktop only");
    const { root, child } = await siblings(request);
    const duel = await createDuel(request, root.id, child.id);
    const clips: Response[] = [];
    page.on("response", (r) => {
      if (/\/media\/[0-9a-f]{64}\.flac\?exp=/.test(r.url())) clips.push(r);
    });
    await openDuel(page, duel.id);
    expect(clips.length).toBeGreaterThanOrEqual(2);
    for (const r of clips) {
      expect(r.status()).toBe(200);
      expect(r.headers()["content-type"]).toContain("audio/flac");
    }
  });

  test("the main player is paused while a duel plays", async ({ page, request }, info) => {
    test.skip(!isDesktop(info.project.name), "desktop only");
    const { root, child } = await siblings(request);
    const duel = await createDuel(request, root.id, child.id);
    const t = await trackByTitle(request, "Syn song 03");
    await page.goto(`/tracks/${t.id}`);
    await page.getByTestId("track-play").click();
    await expect(page.getByTestId("track-play")).toHaveText("Pause");
    await expect.poll(() => page.evaluate(() => document.querySelector("audio")?.paused)).toBe(false);
    await spaGo(page, `/duels/${duel.id}`);
    await expect(page.getByTestId("ab-play")).toBeVisible({ timeout: 20_000 });
    expect(await page.locator("audio").count()).toBe(1);
    await page.getByTestId("ab-play").click();
    await expect.poll(() => page.evaluate(() => document.querySelector("audio")?.paused)).toBe(true);
  });

  test("the nav badge uses list_duels and next_duel waits for the Duels page", async ({ page }, info) => {
    test.skip(!isDesktop(info.project.name), "desktop only");
    const urls: string[] = [];
    page.on("request", (r) => {
      const u = new URL(r.url());
      if (u.pathname.startsWith("/api/duels")) urls.push(u.pathname + u.search);
    });
    await page.goto("/");
    await expect.poll(() => urls.some((u) => u.startsWith("/api/duels?"))).toBe(true);
    const badge = urls.filter((u) => u.startsWith("/api/duels?"));
    expect(badge.some((u) => new URL(u, "http://x").searchParams.get("state") === "open" && new URL(u, "http://x").searchParams.get("limit") === "1")).toBe(true);
    expect(urls.some((u) => u.startsWith("/api/duels/next"))).toBe(false);
    await page.getByRole("link", { name: /^Duels/ }).click();
    await expect.poll(() => urls.some((u) => u.startsWith("/api/duels/next"))).toBe(true);
  });
});

// next_duel serves the globally oldest open duel, so this block is serial and on one project.
test.describe("queue, skip and history", () => {
  test.describe.configure({ mode: "serial" });

  test("skip reaches an own duel; deciding it lists it in the history and /duels/:id reveals it", async ({ page, request }, info) => {
    test.skip(info.project.name !== "chrome", "serial queue spec runs on chrome only");
    const { root, child } = await siblings(request);
    const mine = [await createDuel(request, root.id, child.id), await createDuel(request, child.id, root.id)];
    const ids = new Set(mine.map((d) => d.id));

    const seen: string[] = [];
    page.on("response", async (r) => {
      if (new URL(r.url()).pathname === "/api/duels/next" && r.ok()) {
        const j = (await r.json().catch(() => null)) as { duel?: { id: string } } | null;
        if (j?.duel) seen.push(j.duel.id);
      }
    });
    await page.goto("/duels");
    await expect(page.getByTestId("duel-card")).toBeVisible();
    await expect.poll(() => seen.length).toBeGreaterThanOrEqual(1);
    const skip = page.getByRole("button", { name: "Skip", exact: true });
    let reached = ids.has(seen.at(-1)!);
    for (let i = 0; i < 20 && !reached; i++) {
      const before = seen.length;
      await skip.click();
      await expect.poll(() => seen.length).toBeGreaterThan(before);
      expect(seen.at(-1)).not.toBe(seen.at(-2));
      reached = ids.has(seen.at(-1)!);
    }
    expect(reached, "skipping reached none of the spec's own duels").toBe(true);
    const target = seen.at(-1)!;
    if (seen.length > 1) expect(target).not.toBe(seen[0]);

    await expect(page.getByTestId("ab-play")).toBeVisible({ timeout: 20_000 });
    await page.getByTestId("verdict-tie").click();
    await page.getByTestId("verdict-submit").click();
    await expect(page.getByTestId("duel-reveal")).toBeVisible();

    await page.goto("/duels");
    await expect(page.locator(`a[href="/duels/${target}"]`)).toBeVisible();
    await page.goto(`/duels/${target}`);
    const reveal = page.getByTestId("duel-reveal");
    await expect(reveal).toBeVisible();
    await expect(reveal).toContainText(root.title);
    await expect(reveal.getByRole("heading", { name: "Tie" })).toBeVisible();
  });
});
