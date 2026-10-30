// matrix: @mobile because the Record studio (slot list, keyboard/touch reorder, 44 px targets) and the publish page are phone surfaces
import { expect as baseExpect, test, type APIRequestContext } from "./fixtures";
import { smallTargets, uniq } from "./helpers";
import { archiveDrafts, makeDraftSongs } from "./l1Api";
import { agentContext, changes, createRecord, editSlots, retireRecord as retire, getRecord, otherTake, rate, retireRecord, titledSong, waitChange, type RecordOut, type TitledSong } from "./l2Api";

// One pair of Songs of two takes each (fake backend, spends against the shared cap) per worker server; the tests of the
// file run in order on one Record they make, and the Record is hidden at the end.
const expect = baseExpect.configure({ timeout: 20_000 });
test.describe.configure({ mode: "serial", timeout: 240_000 });

let pair: Promise<[TitledSong, TitledSong]> | undefined;
const songs = (request: APIRequestContext) => (pair ??= (async () => [await titledSong(request, "l2ra", 2), await titledSong(request, "l2rb", 2)])());

let recordId = "";
let published: RecordOut | undefined;

/** Song titles of the slot rows in order. */
const order = async (request: APIRequestContext) => (await getRecord(request, recordId)).slots.map((s) => s.song.title);

test("create a Record from two Songs, reorder by keyboard, pin a slot and see the badge", async ({ page, request }) => {
  const [a, b] = await songs(request);
  const title = uniq("l2 record");
  await page.goto("/records");
  await page.getByTestId("record-new").fill(title);
  await page.getByRole("button", { name: "Create" }).click();
  await expect(page.getByTestId("record-title")).toHaveText(title);
  recordId = new URL(page.url()).pathname.split("/").pop() ?? "";
  expect(recordId).not.toBe("");

  for (const s of [a, b]) {
    await page.getByTestId("song-picker").locator("input").fill(s.title);
    await page.getByTestId("song-picker-add").first().click();
    await expect(page.getByTestId("slot-row")).toHaveCount(s === a ? 1 : 2);
  }
  expect(await order(request)).toEqual([a.title, b.title]);

  // edit the details (update_record) from the studio
  const renamed = `${title} (edited)`;
  await page.getByRole("button", { name: "Edit details" }).click();
  await page.getByLabel("Title", { exact: true }).fill(renamed);
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByTestId("record-title")).toHaveText(renamed);

  // keyboard: focus the first row's "down" control and press Enter, the order changes and focus stays usable
  const down = page.getByRole("button", { name: `Move ${a.title} down` });
  await down.focus();
  await page.keyboard.press("Enter");
  await expect.poll(() => order(request)).toEqual([b.title, a.title]);
  await expect(page.getByTestId("slot-row").first()).toContainText(b.title);
  // and back up
  await page.getByRole("button", { name: `Move ${a.title} up` }).focus();
  await page.keyboard.press("Enter");
  await expect.poll(() => order(request)).toEqual([a.title, b.title]);

  await expect(page.getByTestId("slot-row").first()).toContainText(a.title);
  // pin the second slot (Song B): the badge says pinned, the first stays floating
  const row = page.getByTestId("slot-row").nth(1);
  await expect(row.getByTestId("slot-mode")).toContainText(/floating|follows/i);
  await row.getByTestId("slot-pin").click();
  await expect(row.getByTestId("slot-mode")).toContainText(/pinned/i);
  await expect(page.getByTestId("slot-row").first().getByTestId("slot-mode")).not.toContainText(/pinned/i);
  const v = await getRecord(request, recordId);
  expect(v.slots.map((s) => s.mode)).toEqual(["floating", "pinned"]);
});

test("a Pick change upgrades the floating slot within 3 s and the pinned slot stays", async ({ page, request }) => {
  const [a, b] = await songs(request);
  const before = await getRecord(request, recordId);
  const floating = before.slots[0]!;
  const pinned = before.slots[1]!;

  // the other take of each Song gets the top rating: both Picks move, only the floating slot follows
  const aNew = await otherTake(request, a.songId);
  const bNew = await otherTake(request, b.songId);
  await rate(request, aNew, 5);
  await rate(request, bNew, 5);
  expect(await waitChange(request, recordId, "slot_upgraded", 3_000), "an upgrade line within 3 s of the rating").toBe(true);

  const after = await getRecord(request, recordId);
  expect(after.slots[0]!.take?.id).toBe(aNew);
  expect(after.slots[0]!.take?.id).not.toBe(floating.take?.id);
  expect(after.slots[1]!.take?.id).toBe(pinned.take?.id);
  expect(after.slots[1]!.mode).toBe("pinned");

  await page.goto(`/records/${recordId}`);
  await expect(page.getByTestId("slot-row").nth(1).getByTestId("slot-mode")).toContainText(/pinned/i);
  await page.getByTestId("record-tab-changelog").click();
  await expect(page.getByTestId("record-changelog-row").filter({ hasText: "upgraded" }).first()).toBeVisible();
  // the pinned slot differs from its Song's Pick now and says so
  await page.getByTestId("record-tab-slots").click();
  await expect(page.getByTestId("slot-pin-differs")).toHaveCount(1);
  expect((await changes(request, recordId)).filter((c) => c.kind === "slot_upgraded")).toHaveLength(1);
});

test("propose, then publish through the in-app page", async ({ page, request }) => {
  await page.goto(`/records/${recordId}`);
  await page.getByRole("button", { name: "Propose", exact: true }).click();
  await page.getByLabel("Why is it ready?").fill("e2e: ready to publish");
  await page.getByRole("button", { name: "Send proposal" }).click();
  await expect(page.getByTestId("record-status")).toContainText(/proposed/i);

  await page.getByTestId("record-publish").click();
  await expect(page).toHaveURL(new RegExp(`/human/records/${recordId}/publish$`));
  await expect(page.getByTestId("human-record-publish-page")).toBeVisible();
  await expect(page.getByTestId("publish-chapters").locator("li")).toHaveCount(2);
  await expect(page.getByTestId("publish-tags")).toBeVisible();
  // warnings (the pinned slot differs from its Pick) are reviewed by display before Publish enables
  if (await page.getByTestId("publish-reviewed").count()) await page.getByTestId("publish-reviewed").check();
  await page.getByTestId("publish-go").click();
  await expect.poll(async () => (await getRecord(request, recordId)).record.status).toBe("published");
  published = await getRecord(request, recordId);
});

test("an agent cannot edit a published Record; the studio shows it unchanged", async ({ page, request, playwright, baseURL, agentToken }) => {
  expect(published, "the previous test published the Record").toBeDefined();
  const agent = await agentContext(playwright, baseURL, agentToken);
  const rev = published!.record.revision;
  const slot = published!.slots[0]!;
  const edit = await agent.post(`/api/records/${recordId}/slots`, { data: { revision: rev, ops: [{ op: "remove", slot_id: slot.id }] } });
  expect([403, 409, 422], `agent slot edit of a published Record: ${edit.status()}`).toContain(edit.status());
  expect(edit.ok()).toBe(false);
  const rename = await agent.patch(`/api/records/${recordId}`, { data: { revision: rev, title: "agent rename" } });
  expect(rename.ok()).toBe(false);
  const publish = await agent.post(`/human/api/records/${recordId}/publish`, { data: { revision: rev, published: false } });
  expect(publish.status()).toBe(403);
  await agent.dispose();

  const now = await getRecord(request, recordId);
  expect(now.record.revision).toBe(rev);
  expect(now.slots.map((s) => s.id)).toEqual(published!.slots.map((s) => s.id));
  await page.goto(`/records/${recordId}`);
  await expect(page.getByTestId("record-status")).toContainText(/published/i);
  await expect(page.getByTestId("slot-row")).toHaveCount(2);
});

test("touch targets on /records, the studio and the publish page are at least 44 px", async ({ page }) => {
  test.skip((page.viewportSize()?.width ?? 1280) >= 768, "phone layout");
  for (const [where, path, ready] of [
    ["records list", "/records", "records-list"],
    ["studio", `/records/${recordId}`, "record-studio"],
    ["publish page", `/human/records/${recordId}/publish`, "human-record-publish-page"],
  ] as const) {
    await page.goto(path);
    await expect(page.getByTestId(ready)).toBeVisible();
    await expect(page.getByTestId("record-row").or(page.getByTestId("slot-row")).or(page.getByTestId("publish-go")).first()).toBeVisible();
    expect.soft(await smallTargets(page), `${where}: targets under 44x44`).toEqual([]);
  }
});

test("a long changelog pages: Older changes loads the next page (get_record_changes)", async ({ page, request }) => {
  const [d] = await makeDraftSongs(request, 1, uniq("l2rc"), false);
  let v = await createRecord(request, uniq("l2 long log"), [d!.songId]);
  try {
    const slot = v.slots[0]!.id;
    const ops = (n: number) => Array.from({ length: n }, (_, i) => ({ op: "set_side", slot_id: slot, side: i % 2 === 0 ? "A" : "B" }));
    v = await editSlots(request, v.record.id, v.record.revision, ops(40));
    v = await editSlots(request, v.record.id, v.record.revision, ops(40).map((o, i) => ({ ...o, side: i % 2 === 0 ? "C" : "D" })));
    expect((await changes(request, v.record.id)).length, "enough changes for a second page").toBeGreaterThan(50);
    await page.goto(`/records/${v.record.id}`);
    await page.getByTestId("record-tab-changelog").click();
    const rows = page.getByTestId("record-changelog-row");
    await expect(rows).toHaveCount(50);
    const calls: string[] = [];
    page.on("request", (r) => {
      if (/\/api\/records\/[^/]+\/changes/.test(r.url())) calls.push(r.url());
    });
    await page.getByTestId("record-changelog-more").click();
    await expect.poll(() => rows.count()).toBeGreaterThan(50);
    expect(calls).toHaveLength(1);
  } finally {
    await retire(request, v.record.id);
    await archiveDrafts(request, [d!.draftId]);
  }
});

test("reset: the Record is retired", async ({ request }) => {
  await retireRecord(request, recordId);
  expect((await getRecord(request, recordId)).record.hidden).toBe(true);
});
