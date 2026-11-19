// matrix: chrome-only because it drives one desktop Chrome browser context through two users in turn
import { expect, test } from "./fixtures";
import { E2E_USER, LISTENER_USER, headersFor } from "./env";
import { ownTracks } from "./helpers";
import { asUser, setStatus } from "./u1Api";

// U1 C313.7 / risk 7: browser state is per origin, not per user. One browser context is the Owner (the project identity), then
// the same context becomes the Listener (the headers a second sign-in at the edge would send). Everything the Owner left in
// localStorage goes, the Listener gets a device id of their own, put_device_profile succeeds for them, and a take the Owner
// discarded in between is nowhere in the restored queue.
const PLAYER_KEY = "mm:player:v1";
const SEEN_KEY = "mm:inbox-seen-at";
const DEVICE_KEY = "mm:device-id";
const USER_KEY = "mm:me:user";

test("after switching user, the first user's queue, inbox markers and device id are gone and the Listener's device profile saves", async ({ page, context, request, playwright, baseURL }, info) => {
  const [t1, t2] = await ownTracks(request, info);
  const ls = (key: string) => page.evaluate((k) => localStorage.getItem(k), key);

  // the Owner builds state: a queue on t1, an inbox marker, a device id
  await page.goto(`/tracks/${t1.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(() => ls(PLAYER_KEY)).toContain(t1.id);
  // a marker value no fresh boot would write (the inbox writes "now" on a first visit, so the Listener gets a marker of their own)
  const OWNER_SEEN = "2001-02-03T04:05:06.000Z";
  await page.evaluate(([k, v]) => localStorage.setItem(k, v), [SEEN_KEY, OWNER_SEEN] as const);
  await expect.poll(() => ls(DEVICE_KEY)).toBeTruthy();
  const ownerDevice = await ls(DEVICE_KEY);
  expect(await ls(USER_KEY)).toBe(E2E_USER);

  // the Owner discards the take that is in the stored queue
  await setStatus(request, t1.id, "discarded");

  // the same browser, now signed in as the Listener
  await context.setExtraHTTPHeaders(headersFor(LISTENER_USER));
  await page.goto("/more");
  await expect(page.getByTestId("identity")).toHaveText(LISTENER_USER);

  expect(await ls(USER_KEY)).toBe(LISTENER_USER);
  expect(await ls(PLAYER_KEY), "the Owner's queue is gone").toBeNull();
  expect(await ls(SEEN_KEY), "the Owner's inbox marker is gone").not.toBe(OWNER_SEEN);
  const listenerDevice = await ls(DEVICE_KEY);
  expect(listenerDevice === null || listenerDevice !== ownerDevice, "the Listener has no copy of the Owner's device id").toBe(true);

  // the Listener cannot read the discarded take; a play registers their device (put_device_profile) and queues only what they see
  const carol = await asUser(playwright, baseURL, LISTENER_USER);
  try {
    expect((await carol.get(`/api/tracks/${t1.id}`)).status()).toBe(404);
  } finally {
    await carol.dispose();
  }
  // C313.7 404 path: the first put for the Listener's id is answered 404 once; the client mints a new device id and retries.
  const isPut = (m: string, u: string) => m === "PUT" && new URL(u).pathname.startsWith("/api/device-profiles/");
  let faked = false;
  let idBefore: string | null = null;
  await page.route(
    (u) => u.pathname.startsWith("/api/device-profiles/"),
    async (route) => {
      if (route.request().method() === "PUT" && !faked) {
        faked = true;
        idBefore = await ls(DEVICE_KEY);
        await route.fulfill({ status: 404, contentType: "application/problem+json", body: JSON.stringify({ code: "NOT_FOUND", message: "unknown device", details: null }) });
        return;
      }
      await route.continue();
    },
  );
  const retried = page.waitForResponse((r) => isPut(r.request().method(), r.url()) && r.status() === 200, { timeout: 30_000 });
  await page.goto(`/tracks/${t2.id}`);
  await page.getByTestId("track-play").click();
  await retried;
  expect(faked, "the 404 was injected").toBe(true);
  const newId = await ls(DEVICE_KEY);
  expect(newId, "a new device id was minted after the 404").toBeTruthy();
  expect(newId).not.toBe(ownerDevice);
  if (idBefore) expect(newId).not.toBe(idBefore);
  const queue = (await ls(PLAYER_KEY)) ?? "";
  expect(queue, "the stored queue is not empty").toContain(t2.id);
  expect(queue).not.toContain(t1.id);
});
