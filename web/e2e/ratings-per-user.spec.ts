// matrix: chrome-only because the rating rules are data and API rules; one Owner page checks the Owner's view
import { expect, test } from "./fixtures";
import { ownTracks } from "./helpers";
import { EDITOR_USER, LISTENER_USER } from "./env";
import { asUser, ratingOf } from "./u1Api";

// U1 C314.7, D-U1-16: every user has their own rating of a take. The Owner sees each user's and the average, an Editor the
// average, a Listener neither, and nobody's rating moves another's.
test("three users rate one take independently and each sees only what their role allows", async ({ page, request, playwright, baseURL }, info) => {
  const [t] = await ownTracks(request, info);
  const bob = await asUser(playwright, baseURL, EDITOR_USER);
  const carol = await asUser(playwright, baseURL, LISTENER_USER);
  const rate = async (ctx: typeof bob, stars: number | null) => {
    const r = await ctx.put(`/api/tracks/${t.id}/rating`, { data: { stars } });
    expect(r.ok(), await r.text()).toBe(true);
  };
  try {
    await rate(request, 5);
    await rate(bob, 4);
    await rate(carol, 2);

    const o = await ratingOf(request, t.id);
    expect(o.mine?.stars).toBe(5);
    expect(o.others?.map((x) => [x.user, x.stars]).sort()).toEqual([[EDITOR_USER, 4], [LISTENER_USER, 2]]);
    expect(o.average).toBeTruthy();
    // both raters are configured users: none is flagged former (a removed user gets "former user (name)" in the same list)
    expect(o.others?.every((x) => !x.former)).toBe(true);

    const e = await ratingOf(bob, t.id);
    expect(e.mine?.stars).toBe(4);
    expect(e.others ?? null).toBeNull();
    expect(e.average).toBeTruthy();

    const l = await ratingOf(carol, t.id);
    expect(l.mine?.stars).toBe(2);
    expect(l.others ?? null).toBeNull();
    expect(l.average ?? null).toBeNull();

    // the Owner's page shows the other raters and the average
    await page.goto(`/tracks/${t.id}`);
    await expect(page.getByTestId("rating-panel")).toBeVisible();
    await expect(page.getByTestId("rating-average")).toBeVisible();
    await expect(page.getByTestId("rating-other")).toHaveCount(2);
    await expect(page.getByTestId("rating-others")).toContainText(EDITOR_USER);
    await expect(page.getByTestId("rating-others")).toContainText(LISTENER_USER);
    await expect(page.getByTestId("rating-others")).not.toContainText("former user");

    // the Owner re-rates in the UI; the others stay where they were. A Listener clearing theirs leaves the Owner's alone.
    await page.getByTestId("track-judge").getByRole("button", { name: "3 stars", exact: true }).click();
    await expect.poll(async () => (await ratingOf(request, t.id)).mine?.stars).toBe(3);
    await rate(carol, null);
    expect((await ratingOf(request, t.id)).mine?.stars).toBe(3);
    expect((await ratingOf(bob, t.id)).mine?.stars).toBe(4);
    expect((await ratingOf(carol, t.id)).mine?.stars ?? null).toBeNull();
    await expect.poll(async () => (await ratingOf(request, t.id)).others?.map((x) => x.user).sort()).toEqual([EDITOR_USER]);
  } finally {
    await bob.dispose();
    await carol.dispose();
  }
});
