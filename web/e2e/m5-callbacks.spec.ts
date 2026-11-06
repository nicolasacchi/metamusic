// matrix: chrome-only because the callback flow is server state (verdicts move comment states) read through the Judge pages; the phone layout of the card is asserted at 360 px in m5-layout
// M5 INT-W: callbacks. The Judge count, the list, the fix and keep cards with their verdicts, and how each verdict moves the
// comment (not yet -> open, fixed -> resolved, worse -> regressed, a lost keep -> a fix note on the new take, a kept one nothing).
// One closed loop (m5Api.closeTheLoop: a composed base, two fix comments and a keep comment, one patch, two or more children)
// serves the tests in order; the callbacks are found by comment id, so no test depends on a total.
import { expect, test, type Page } from "./fixtures";
import { audioTime } from "./helpers";
import { callbacksOf, closeTheLoop, commentById, comments, pendingTotal, watchM5, type CallbackOut, type ClosedLoop } from "./m5Api";
import type { APIRequestContext } from "@playwright/test";

test.describe.configure({ mode: "serial", timeout: 300_000 });

let made: Promise<ClosedLoop> | undefined;
const loop = (request: APIRequestContext): Promise<ClosedLoop> => (made ??= closeTheLoop(request, "m5-cb"));

const waitingText = (n: number) => (n === 1 ? "1 callback waiting" : `${n} callbacks waiting`);

/** The two callbacks of a comment (one per child), the first and the second by creation. */
async function pair(request: APIRequestContext, commentId: string): Promise<[CallbackOut, CallbackOut]> {
  const cbs = await callbacksOf(request, `comment_id=${commentId}`);
  const [a, b] = [cbs[0], cbs[1]];
  if (!a || !b) throw new Error(`comment ${commentId} has ${cbs.length} callbacks, wanted two`);
  return [a, b];
}

/** Every callback of the loop: those of the two fix comments and of the keep comment, one per child each. */
async function allOf(request: APIRequestContext, l: ClosedLoop): Promise<CallbackOut[]> {
  return (await Promise.all([l.fixA.id, l.fixB.id, l.keep.id].map((id) => callbacksOf(request, `comment_id=${id}`)))).flat();
}

const answer = (page: Page, verdict: string) => page.getByTestId("callback-card").locator(`button[data-verdict="${verdict}"]`);

/** Opens the card by its URL and waits for it to show the callback. */
async function openCard(page: Page, cb: CallbackOut): Promise<void> {
  await page.goto(`/judge/callbacks/${cb.id}`);
  await expect(page.getByTestId("callback-comment")).toContainText(cb.comment.body);
}

test("with nothing rendered the Judge page shows no count and Callbacks says none yet", async ({ page }) => {
  const watch = watchM5(page);
  await page.goto("/judge");
  await expect(page.getByTestId("judge")).toBeVisible();
  await expect(page.getByTestId("judge-progress")).toBeVisible();
  await expect(page.getByTestId("callbacks-waiting")).toHaveCount(0);
  await page.goto("/judge/callbacks");
  await expect(page.getByRole("heading", { name: "Callbacks" })).toBeVisible();
  await expect(page.getByTestId("callbacks-empty")).toBeVisible();
  await expect(page.getByTestId("callbacks-list")).toHaveCount(0);
  expect(watch.problems()).toEqual([]);
});

test("a rendered Song PR puts its callbacks on the Judge page and in the list, pending first", async ({ page, request }) => {
  const watch = watchM5(page);
  const l = await loop(request);
  const all = await allOf(request, l);
  const pending = await pendingTotal(request);
  expect(pending).toBeGreaterThanOrEqual(2 * 3); // two children, each with a callback per fix comment and one for the keep comment
  expect(all.filter((c) => c.state === "pending")).toHaveLength(pending);

  await page.goto("/judge");
  const link = page.getByTestId("callbacks-waiting");
  await expect(link).toHaveText(waitingText(pending));
  await link.click();
  await expect(page).toHaveURL(/\/judge\/callbacks$/);
  await expect(page.getByTestId("callbacks-count")).toContainText(waitingText(pending));
  const items = page.getByTestId("callbacks-list").getByRole("link");
  await expect(items).toHaveCount(all.length);
  await expect(items.first()).toHaveAttribute("data-state", "pending");
  for (const child of l.children.slice(0, 2)) {
    const title = (await callbacksOf(request, `child_track_id=${child}`))[0]?.child.title ?? "";
    expect(title).not.toBe("");
    await expect(items.filter({ hasText: title }).first()).toBeVisible();
  }
  expect(watch.problems()).toEqual([]);
});

test("a fix card: the comment, the spot, hearing the new take, Not yet puts the comment back to open", async ({ page, request }) => {
  const watch = watchM5(page);
  const l = await loop(request);
  const [first] = await pair(request, l.fixA.id);
  expect((await commentById(request, l.base.id, l.fixA.id)).state).toBe("addressed"); // the first child moved it
  const before = await pendingTotal(request);

  await openCard(page, first);
  await expect(page.getByTestId("callback-card")).toContainText("Fix check");
  await expect(page.getByTestId("callback-spot")).toBeVisible();
  await expect(page.getByTestId("callback-hear")).toBeVisible();
  await expect(page.getByTestId("callback-hear-original")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Did the patch fix it?" })).toBeVisible();
  await expect(page.getByTestId("callback-card").locator("button[data-verdict]")).toHaveCount(3);
  for (const v of ["fixed", "not_yet", "worse"]) await expect(answer(page, v)).toBeVisible();
  await expect(answer(page, "kept")).toHaveCount(0);

  // hearing the new take starts it at the spot
  await page.getByTestId("callback-hear").click();
  await expect.poll(() => audioTime(page), { timeout: 10_000 }).toBeGreaterThanOrEqual(Math.max(0, first.spot.child_at_s - 0.5));

  await page.getByLabel(/Note \(optional\)/).fill("still drags a little");
  await answer(page, "not_yet").click();
  await expect(page.getByTestId("callback-result")).toHaveText("Answered: not yet");
  await expect(page.getByTestId("callback-answered")).toContainText("Not yet");
  await expect(answer(page, "fixed")).toHaveCount(0); // answered once; the card only reports now
  await expect(page.getByTestId("callback-next")).toBeVisible();

  await expect.poll(async () => (await commentById(request, l.base.id, l.fixA.id)).state).toBe("open");
  expect(await pendingTotal(request)).toBe(before - 1);
  expect((await callbacksOf(request, `comment_id=${l.fixA.id}`)).find((c) => c.id === first.id)?.state).toBe("answered");

  // Next callback goes to another waiting one
  await page.getByTestId("callback-next").click();
  await expect(page).toHaveURL(/\/judge\/callbacks\/[0-9a-f-]+$/);
  expect(page.url()).not.toContain(first.id);
  expect(watch.problems()).toEqual([]);
});

test("Fixed on the other child resolves the comment, and the Track page stops offering it", async ({ page, request }) => {
  const watch = watchM5(page);
  const l = await loop(request);
  const [, second] = await pair(request, l.fixA.id);

  await openCard(page, second);
  await answer(page, "fixed").click();
  await expect(page.getByTestId("callback-result")).toHaveText("Answered: fixed");
  await expect.poll(async () => (await commentById(request, l.base.id, l.fixA.id)).state).toBe("resolved");

  // the open view of the Track no longer lists it or offers it to a patch; the full view shows it with Reopen
  await page.goto(`/tracks/${l.base.id}`);
  const panelFix = page.locator(`[data-testid="song-pr-fix"][data-comment-id="${l.fixA.id}"]`);
  await expect(page.getByTestId("song-pr-panel")).toBeVisible();
  await expect(panelFix).toHaveCount(0);
  const row = page.getByTestId("comment").filter({ hasText: l.fixA.body });
  await expect(row).toHaveCount(0);
  await page.getByTestId("comments-panel").getByLabel("Show").selectOption("all");
  await expect(row).toBeVisible();
  await expect(row.getByRole("button", { name: "Reopen" })).toBeVisible();
  expect(watch.problems()).toEqual([]);
});

test("Worse regresses a comment and the sibling rule is order free: a Fixed on the other child resolves it", async ({ page, request }) => {
  const watch = watchM5(page);
  const l = await loop(request);
  const [first, second] = await pair(request, l.fixB.id);

  await openCard(page, first);
  await answer(page, "worse").click();
  await expect(page.getByTestId("callback-result")).toHaveText("Answered: worse");
  await expect.poll(async () => (await commentById(request, l.base.id, l.fixB.id)).state).toBe("regressed");

  // a regressed fix is offered to a new patch again (shown once the full comment view is loaded)
  await page.goto(`/tracks/${l.base.id}`);
  await page.getByTestId("comments-panel").getByLabel("Show").selectOption("all");
  const row = page.getByTestId("comment").filter({ hasText: l.fixB.body });
  await expect(row).toContainText("regressed");
  await expect(page.locator(`[data-testid="song-pr-fix"][data-comment-id="${l.fixB.id}"]`)).toContainText("regressed");

  await openCard(page, second);
  await answer(page, "fixed").click();
  await expect(page.getByTestId("callback-result")).toHaveText("Answered: fixed");
  await expect.poll(async () => (await commentById(request, l.base.id, l.fixB.id)).state).toBe("resolved");
  expect(watch.problems()).toEqual([]);
});

test("a keep card asks if it is still there: Lost writes a fix note on the new take, Kept changes nothing", async ({ page, request }) => {
  const watch = watchM5(page);
  const l = await loop(request);
  const [first, second] = await pair(request, l.keep.id);
  expect(first.kind).toBe("keep");

  await openCard(page, first);
  await expect(page.getByTestId("callback-card")).toContainText("Keep check");
  await expect(page.getByRole("heading", { name: "Is it still there?" })).toBeVisible();
  await expect(page.getByTestId("callback-card").locator("button[data-verdict]")).toHaveCount(2);
  await expect(answer(page, "kept")).toBeVisible();
  await expect(answer(page, "lost")).toBeVisible();
  await expect(answer(page, "fixed")).toHaveCount(0);
  await answer(page, "lost").click();
  await expect(page.getByTestId("callback-result")).toContainText("A fix note was added to the new take");

  // the note is a new open fix comment on the child, and the keep comment itself did not move
  const onChild = await comments(request, first.child.id);
  const note = onChild.find((c) => c.origin_callback_id === first.id);
  expect(note?.kind).toBe("fix");
  expect(note?.state).toBe("open");
  expect((await commentById(request, l.base.id, l.keep.id)).state).toBe("open");

  await openCard(page, second);
  await answer(page, "kept").click();
  await expect(page.getByTestId("callback-result")).toHaveText("Answered: kept");
  expect((await comments(request, second.child.id)).some((c) => c.origin_callback_id === second.id)).toBe(false);
  expect((await commentById(request, l.base.id, l.keep.id)).state).toBe("open");
  expect(watch.problems()).toEqual([]);
});

test("answered callbacks leave the count: the Judge link and the list agree with the server", async ({ page, request }) => {
  const watch = watchM5(page);
  const l = await loop(request);
  const all = await allOf(request, l);
  const pending = all.filter((c) => c.state === "pending").length;
  expect(await pendingTotal(request)).toBe(pending);

  await page.goto("/judge");
  if (pending > 0) await expect(page.getByTestId("callbacks-waiting")).toHaveText(waitingText(pending));
  else await expect(page.getByTestId("callbacks-waiting")).toHaveCount(0);

  await page.goto("/judge/callbacks");
  await expect(page.getByTestId("callbacks-count")).toContainText(waitingText(pending));
  const answered = page.getByTestId("callbacks-list").locator('a[data-state="answered"]');
  await expect(answered).toHaveCount(all.length - pending);
  await expect(answered.first()).toContainText("answered");

  // an answered card reports the answer and no longer offers buttons
  const done = all.find((c) => c.state === "answered");
  if (!done) throw new Error("no callback was answered");
  await page.goto(`/judge/callbacks/${done.id}`);
  await expect(page.getByTestId("callback-answered")).toBeVisible();
  await expect(page.getByTestId("callback-card").locator("button[data-verdict]")).toHaveCount(0);
  expect(watch.problems()).toEqual([]);
});
