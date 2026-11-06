// matrix: chrome-only because the Smart round is a request/response flow on the Duels page; the duel player itself is covered by m2b-duels on every device
// M5 INT-W: the Smart round and the section lock. suggest_duel_pairs -> create_duel with a section selector, blind until the
// verdict: nothing in the page or in the duel the server shows may name a track, a slot or a window before the reveal.
import { expect, test, type APIRequestContext } from "./fixtures";
import { allTracks, apiJSON } from "./helpers";
import { rate } from "./l2Api";
import { getDuel, siblings, type DuelDTO } from "./m2bApi";
import { trackDetail, watchM5, type TrackDetail } from "./m5Api";

test.describe.configure({ mode: "serial", timeout: 120_000 });

/** The seeded takes with a timed chorus (a plan or score gave the sections their times), sorted by title. */
async function timedTakes(request: APIRequestContext): Promise<TrackDetail[]> {
  const out: TrackDetail[] = [];
  const cands = (await allTracks(request)).filter((t) => t.title.startsWith("Syn song") || t.title.startsWith("Syn ears loop"));
  for (const t of cands.sort((a, b) => (a.title < b.title ? -1 : 1))) {
    const d = await trackDetail(request, t.id);
    if (d.sections.some((s) => s.kind === "chorus" && s.start_s !== null && s.end_s !== null)) out.push(d);
  }
  return out;
}

interface WindowedDuel extends DuelDTO {
  revealed: boolean;
  window: { from_s: number | null; to_s: number | null; duration_s: number };
  slots: { a: DuelDTO["slots"]["a"] & { window?: { from_s: number; to_s: number } | null }; b: DuelDTO["slots"]["b"] & { window?: { from_s: number; to_s: number } | null } };
}

test("a Smart round locked to the chorus is blind: no title, id, slot or window in the page before the verdict", async ({ page, request }) => {
  const watch = watchM5(page);
  const timed = await timedTakes(request);
  expect(timed.length, "the e2e profile has at least two takes with a timed chorus").toBeGreaterThanOrEqual(2);
  const [x, y] = timed;
  if (!x || !y) throw new Error("two timed takes");
  // suggest_duel_pairs (global scope) draws from the takes the human rated
  await rate(request, x.id, 4);
  await rate(request, y.id, 2);
  const names = timed.flatMap((t) => [t.title, t.id]);

  await page.goto("/duels");
  const round = page.getByTestId("smart-round");
  await expect(round).toBeVisible();
  await expect(page.getByTestId("smart-blind")).toBeChecked();
  await page.getByTestId("smart-section").selectOption("chorus");
  await expect(page.getByTestId("smart-ordinal")).toHaveValue("1");
  await page.getByTestId("smart-suggest").click();

  const pairs = page.getByTestId("smart-pairs");
  await expect(pairs).toBeVisible();
  await expect(pairs).toContainText("Pair 1");
  await expect(pairs).toContainText(/score \d\.\d{3}/);
  for (const n of names) expect(await pairs.innerText()).not.toContain(n);
  await page.getByTestId("smart-start").first().click();

  await expect(page).toHaveURL(/\/duels\/[0-9a-f-]{36}$/);
  const duelId = new URL(page.url()).pathname.split("/").pop() ?? "";
  const card = page.getByTestId("duel-card");
  await expect(card.getByRole("heading", { name: "Blind duel" })).toBeVisible();
  await expect(page.getByTestId("ab-toggle")).toHaveText("A", { timeout: 20_000 });

  // nothing in the page names the takes, the slots or the section windows
  const html = await page.content();
  for (const n of names) expect(html, `the blind duel page must not contain ${n}`).not.toContain(n);
  expect(html).not.toMatch(/from_s|to_s|b_from_ms|b_to_ms|a_from_ms|a_to_ms/);
  expect(await card.innerText()).not.toMatch(/\d+:\d{2}\s*[-–—]\s*\d+:\d{2}/);
  await expect(page.getByTestId("duel-titles")).toHaveCount(0);

  // and the server's own view agrees: no track in a slot, no window anywhere
  const hidden = await apiJSON<WindowedDuel>(request, "GET", `/api/duels/${duelId}`);
  expect(hidden.blind).toBe(true);
  expect(hidden.revealed).toBe(false);
  expect(hidden.slots.a.track).toBeNull();
  expect(hidden.slots.b.track).toBeNull();
  expect(hidden.slots.a.window ?? null).toBeNull();
  expect(hidden.slots.b.window ?? null).toBeNull();
  expect(hidden.window.from_s).toBeNull();
  expect(hidden.window.to_s).toBeNull();
  const wire = JSON.stringify(hidden);
  for (const n of names) expect(wire).not.toContain(n);

  // the verdict reveals: titles, then the windows, each inside the chorus of its own take
  await page.getByTestId("verdict-a").click();
  await page.getByTestId("verdict-submit").click();
  const reveal = page.getByTestId("duel-reveal");
  await expect(reveal).toBeVisible();
  const shown = await getDuel(request, duelId);
  const sa = shown.slots.a.track;
  const sb = shown.slots.b.track;
  if (!sa || !sb) throw new Error("the decided duel names both takes");
  await expect(reveal).toContainText(sa.title);
  await expect(reveal).toContainText(sb.title);

  const open = await apiJSON<WindowedDuel>(request, "GET", `/api/duels/${duelId}`);
  expect(open.revealed).toBe(true);
  // each window starts up to 2 s before its own chorus (a pre-roll) and holds the chorus start; both are equal in length
  const lengths: number[] = [];
  for (const [slot, take] of [[open.slots.a, sa], [open.slots.b, sb]] as const) {
    const detail = timed.find((t) => t.id === take.id);
    const chorus = detail?.sections.find((s) => s.kind === "chorus");
    const w = slot.window;
    if (!detail || !chorus || !w) throw new Error(`revealed slot of ${take.title} has no window`);
    const start = chorus.start_s ?? 0;
    expect(w.from_s).toBeLessThanOrEqual(start + 0.05);
    expect(w.from_s).toBeGreaterThanOrEqual(start - 2 - 0.05);
    expect(w.to_s).toBeGreaterThan(start);
    lengths.push(w.to_s - w.from_s);
  }
  expect(Math.abs((lengths[0] ?? 0) - (lengths[1] ?? 1))).toBeLessThanOrEqual(0.05);
  expect(watch.problems()).toEqual([]);
});

test("a section nobody has, or a take without times, is refused with its own code", async ({ page, request }) => {
  const watch = watchM5(page);
  const [x, y] = await timedTakes(request);
  if (!x || !y) throw new Error("two timed takes");
  const untimed = (await allTracks(request)).find((t) => t.title === "Syn song 04");
  if (!untimed) throw new Error("seed take Syn song 04 missing");

  const post = (b: string, section: Record<string, unknown>) =>
    request.fetch("/api/duels", { method: "POST", headers: { "Idempotency-Key": `m5-${Math.random().toString(16).slice(2)}` }, data: { a_track_id: x.id, b_track_id: b, section } });
  const noSection = await post(y.id, { kind: "bridge", ordinal: 1 });
  expect(noSection.status()).toBe(422);
  expect(((await noSection.json()) as { code: string }).code).toBe("SECTION_NOT_FOUND");
  const noTimes = await post(untimed.id, { kind: "chorus", ordinal: 1 });
  expect(noTimes.status()).toBe(422);
  const body = (await noTimes.json()) as { code: string; details: { track_id: string } };
  expect(body.code).toBe("SECTION_UNTIMED");
  expect(body.details.track_id).toBe(untimed.id);

  // the picker never offers a pair it cannot lock: a section no timed take has gives an empty list, not an error
  await page.goto("/duels");
  await page.getByTestId("smart-section").selectOption("bridge");
  await page.getByTestId("smart-suggest").click();
  await expect(page.getByTestId("smart-none")).toBeVisible();
  await expect(page.getByTestId("smart-pairs")).toHaveCount(0);
  expect(watch.problems()).toEqual([]);
});

test("a lineage round needs its root; sighted, the pairs carry their titles and open a duel with titles", async ({ page, request }) => {
  const watch = watchM5(page);
  const { root, child } = await siblings(request);
  await page.goto("/duels");
  await page.getByTestId("smart-scope").selectOption("lineage");
  await expect(page.getByTestId("smart-suggest")).toBeDisabled();
  await page.getByTestId("smart-root").fill(root.id);
  await expect(page.getByTestId("smart-suggest")).toBeEnabled();
  await page.getByTestId("smart-blind").uncheck();
  await page.getByTestId("smart-suggest").click();
  const pairs = page.getByTestId("smart-pairs");
  await expect(pairs).toContainText(root.title);
  await expect(pairs).toContainText(child.title);
  await expect(pairs).not.toContainText("Pair 1");
  await page.getByTestId("smart-start").first().click();
  await expect(page).toHaveURL(/\/duels\/[0-9a-f-]{36}$/);
  await expect(page.getByTestId("duel-card").getByRole("heading", { name: "Duel", exact: true })).toBeVisible();
  await expect(page.getByTestId("duel-titles")).toContainText(root.title);
  await expect(page.getByTestId("duel-titles")).toContainText(child.title);
  expect(watch.problems()).toEqual([]);
});
