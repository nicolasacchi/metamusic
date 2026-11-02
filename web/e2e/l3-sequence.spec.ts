// matrix: @mobile because the Sequence tab (strategy form, preview steps, apply and propose buttons, pinned list) is used on the phone
import { expect as baseExpect, test } from "./fixtures";
import { apiJSON, uniq } from "./helpers";
import { agentContext, changes, createRecord, editSlots, getRecord, retireRecord, type RecordOut } from "./l2Api";
import { songsAt, type TitledAt } from "./l3Api";

// Four rendered Songs at distinct declared tempos, put into a Record in a deliberately jumpy order. The tests run in order on
// Records they make; each Record is hidden when the spec is done with it.
const expect = baseExpect.configure({ timeout: 20_000 });
test.describe.configure({ mode: "serial", timeout: 240_000 });

const tag = uniq("l3s");
let made: Promise<TitledAt[]> | undefined;
const songs = (request: Parameters<typeof songsAt>[0]) => (made ??= songsAt(request, tag, [140, 90, 120, 100]));
const titles = (v: RecordOut) => v.slots.map((s) => s.song.title);

let recordId = "";
const retire: string[] = [];

test.afterAll(async ({ request }) => {
  for (const id of retire) await retireRecord(request, id).catch(() => undefined);
});

test("preview shows steps, a coverage chip and the cost; Apply reorders and the changelog says sequenced", async ({ page, request }) => {
  const all = await songs(request);
  const v = await createRecord(request, uniq("l3 seq record"), all.map((s) => s.songId));
  recordId = v.record.id;
  retire.push(recordId);
  const before = titles(v);
  expect(before).toHaveLength(4);

  await page.goto(`/records/${recordId}`);
  await page.getByTestId("record-tab-sequence").click();
  await expect(page.getByTestId("sequence-panel")).toBeVisible();
  await expect(page.getByTestId("sequence-too-small")).toHaveCount(0);
  await page.getByTestId("sequence-strategy").selectOption("steady");
  await page.getByTestId("sequence-run").click();
  await expect(page.getByTestId("sequence-preview")).toBeVisible();
  await expect(page.getByTestId("sequence-step")).toHaveCount(4);
  await expect(page.getByTestId("sequence-coverage")).toBeVisible();
  await expect(page.getByTestId("sequence-curve")).toBeVisible();
  // the preview wrote nothing
  expect(titles(await getRecord(request, recordId))).toEqual(before);

  const previewed = await page.getByTestId("sequence-step").allInnerTexts();
  await page.getByTestId("sequence-apply").click();
  await expect.poll(async () => titles(await getRecord(request, recordId))).not.toEqual(before);
  // the order applied is the order previewed
  const after = titles(await getRecord(request, recordId));
  for (const [i, t] of after.entries()) expect(previewed[i]).toContain(t);

  await expect(page.getByTestId("sequence-run-info")).toBeVisible();
  await expect(page.getByTestId("sequence-stale")).toHaveCount(0);
  await page.getByTestId("record-tab-changelog").click();
  await expect(page.getByTestId("record-changelog-row").filter({ hasText: "(sequenced: steady)" }).first()).toBeVisible();
  expect((await changes(request, recordId)).some((c) => c.kind === "slot_moved")).toBe(true);
});

test("a manual reorder makes the latest run stale; the badge offers a refresh", async ({ page, request }) => {
  const v = await getRecord(request, recordId);
  const ids = v.slots.map((s) => s.id);
  await editSlots(request, recordId, v.record.revision, [{ op: "reorder", slot_ids: [ids[1], ids[0], ...ids.slice(2)] }]);
  await page.goto(`/records/${recordId}`);
  await page.getByTestId("record-tab-sequence").click();
  await expect(page.getByTestId("sequence-stale")).toBeVisible();
  await expect(page.getByTestId("sequence-run-info")).toBeVisible();
  // a new preview from the stale state gives a fresh answer and Apply works against the current revision
  await page.getByTestId("sequence-run").click();
  await expect(page.getByTestId("sequence-preview")).toBeVisible();
  await expect(page.getByTestId("sequence-outdated")).toHaveCount(0);
});

test("propose with this order from the tab: the Record becomes proposed and the changelog shows a sequenced proposal", async ({ page, request }) => {
  const all = await songs(request);
  const v = await createRecord(request, uniq("l3 seq propose"), all.map((s) => s.songId));
  retire.push(v.record.id);
  await page.goto(`/records/${v.record.id}?sequence=arc`);
  await expect(page.getByTestId("sequence-preview")).toBeVisible(); // ?sequence= runs the preview once
  await expect(page.getByTestId("sequence-strategy")).toHaveValue("arc");
  await page.getByTestId("sequence-propose").click();
  await page.getByTestId("sequence-rationale").fill("e2e: ordered for the arc");
  await page.getByTestId("sequence-send").click();
  await expect(page.getByTestId("record-status")).toContainText(/proposed/i);
  await page.getByTestId("record-tab-changelog").click();
  await expect(page.getByTestId("record-changelog-row").filter({ hasText: "Proposed for publishing (sequenced" }).first()).toBeVisible();
});

test("an agent proposes a sequenced Record through the API; the human sees it proposed with its run", async ({ page, request, playwright, baseURL, agentToken }) => {
  const all = await songs(request);
  const agent = await agentContext(playwright, baseURL, agentToken);
  const rec = await createRecord(agent, uniq("l3 agent record"), all.map((s) => s.songId));
  retire.push(rec.record.id);
  const proposed = await apiJSON<RecordOut>(agent, "POST", `/api/records/${rec.record.id}/proposal`, { revision: rec.record.revision, rationale: "e2e: agent proposal", sequence: { strategy: "arc" } });
  await agent.dispose();
  expect(proposed.record.status).toBe("proposed");

  await page.goto(`/records/${rec.record.id}`);
  await expect(page.getByTestId("record-status")).toContainText(/proposed/i);
  await page.getByTestId("record-tab-sequence").click();
  await expect(page.getByTestId("sequence-run-info")).toBeVisible();
  await expect(page.getByTestId("sequence-stale")).toHaveCount(0);
  await expect(page.getByTestId("record-publish")).toBeVisible(); // the human can take it from here
});

test("pinned slots are listed with their Take and keep their pin through a sequence", async ({ page, request }) => {
  const all = await songs(request);
  const v = await createRecord(request, uniq("l3 seq pinned"), all.map((s) => s.songId));
  retire.push(v.record.id);
  const pinSlot = v.slots[0]!;
  await editSlots(request, v.record.id, v.record.revision, [{ op: "pin", slot_id: pinSlot.id, take_id: pinSlot.take!.id }]);
  await page.goto(`/records/${v.record.id}`);
  await page.getByTestId("record-tab-sequence").click();
  const pinned = page.getByTestId("sequence-pinned");
  await expect(pinned).toBeVisible();
  await expect(pinned).toContainText(pinSlot.song.title);
  await page.getByTestId("sequence-run").click();
  await expect(page.getByTestId("sequence-preview")).toBeVisible();
  await page.getByTestId("sequence-apply").click();
  await expect.poll(async () => (await getRecord(request, v.record.id)).slots.find((s) => s.id === pinSlot.id)?.mode).toBe("pinned");
});

test("a Record of one Song says a sequence needs two", async ({ page, request }) => {
  const [a] = await songs(request);
  const v = await createRecord(request, uniq("l3 seq one"), [a!.songId]);
  retire.push(v.record.id);
  await page.goto(`/records/${v.record.id}`);
  await page.getByTestId("record-tab-sequence").click();
  await expect(page.getByTestId("sequence-too-small")).toBeVisible();
  await expect(page.getByTestId("sequence-run")).toBeDisabled();
});
