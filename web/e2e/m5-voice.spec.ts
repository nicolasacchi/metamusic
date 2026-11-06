// matrix: @firefox because MediaRecorder and getUserMedia are engine APIs the recorder feature-checks; its states and uploads run on Chrome and Firefox (the real recording is Chrome's, m5-voice-save)
// M5 INT-W: the voice recorder under the comment composer. MediaRecorder and the microphone are stubbed in the page: a fake
// recorder hands over a synthetic blob (2 KiB of one byte value, no audio) when stopped, and a fake stream counts the stops of its
// tracks. What is asserted is the recorder's own contract: nothing is requested until Save, the microphone is released on stop,
// Save sends the recording once with its pin and kind, and the 501/503 answers read as "not available".
import { expect, test, type Page } from "./fixtures";
import { audioTime, trackByTitle } from "./helpers";
import { comments, watchM5 } from "./m5Api";

test.describe.configure({ mode: "serial", timeout: 120_000 });

declare global {
  interface Window {
    __mic: { opened: number; stopped: number };
  }
}

const BLOB_BYTES = 2048;

/** A stub recorder and microphone: getUserMedia answers with a fake stream, MediaRecorder hands over BLOB_BYTES of 7 on stop. */
async function fakeRecorder(page: Page): Promise<void> {
  await page.addInitScript((size) => {
    window.__mic = { opened: 0, stopped: 0 };
    const track = {
      stop: () => {
        window.__mic.stopped++;
      },
    };
    const stream = { getTracks: () => [track] };
    navigator.mediaDevices.getUserMedia = async () => {
      window.__mic.opened++;
      return stream as unknown as MediaStream;
    };
    class FakeRecorder {
      static isTypeSupported(): boolean {
        return true;
      }
      state = "inactive";
      mimeType: string;
      ondataavailable: ((e: { data: Blob }) => void) | null = null;
      onstop: (() => void) | null = null;
      constructor(_stream: unknown, opts?: { mimeType?: string }) {
        this.mimeType = opts?.mimeType ?? "audio/webm;codecs=opus";
      }
      start(): void {
        this.state = "recording";
      }
      stop(): void {
        this.state = "inactive";
        this.ondataavailable?.({ data: new Blob([new Uint8Array(size).fill(7)], { type: this.mimeType }) });
        this.onstop?.();
      }
    }
    Object.defineProperty(window, "MediaRecorder", { value: FakeRecorder, configurable: true, writable: true });
  }, BLOB_BYTES);
}

const recorder = (page: Page) => page.getByTestId("voice-recorder");

/** Records about a second and stops; leaves the recorder in its recorded state. */
async function recordSecond(page: Page): Promise<void> {
  await page.getByTestId("voice-record").click();
  await expect(recorder(page)).toHaveAttribute("data-state", "recording");
  await expect(page.getByTestId("voice-timer")).toContainText("of 2:00");
  await page.waitForTimeout(1200);
  await page.getByTestId("voice-stop").click();
  await expect(recorder(page)).toHaveAttribute("data-state", "recorded");
}

interface Upload {
  audio_base64: string;
  at_second?: number | null;
  kind?: string;
  key: string;
}

/** Answers the upload with a created comment and keeps what the page sent. */
async function acceptUploads(page: Page, trackId: string): Promise<Upload[]> {
  const sent: Upload[] = [];
  await page.route("**/voice-notes", async (route) => {
    const body = route.request().postDataJSON() as Omit<Upload, "key">;
    sent.push({ ...body, key: route.request().headers()["idempotency-key"] ?? "" });
    const now = new Date().toISOString();
    await route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify({
        id: `00000000-0000-7000-8000-${String(sent.length).padStart(12, "0")}`,
        track_id: trackId,
        body: "[voice note 0:01]",
        at_second: body.at_second ?? null,
        link: "",
        url: "",
        section: null,
        kind: body.kind ?? "note",
        state: "open",
        source: "web",
        author: { kind: "human", id: "human:e2e", name: "e2e" },
        created_at: now,
        updated_at: now,
        edited_at: null,
        state_changed_at: null,
        hidden: false,
      }),
    });
  });
  return sent;
}

test("the recorder mounts idle under the composer and sends nothing by itself", async ({ page, request }) => {
  const watch = watchM5(page);
  await fakeRecorder(page);
  const sent = await acceptUploads(page, "none");
  const t = await trackByTitle(request, "Syn song 03");
  await page.goto(`/tracks/${t.id}`);
  const rec = recorder(page);
  await expect(rec).toBeVisible();
  await expect(rec).toHaveAttribute("data-track", t.id);
  await expect(rec).toHaveAttribute("data-state", "idle");
  await expect(rec).toHaveAttribute("data-at", ""); // this take is not the one playing: no position to pin
  await expect(page.getByTestId("voice-record")).toHaveText("Record a voice note");
  // it sits below the composer in the comments card
  const composerBox = await page.getByTestId("comment-composer").boundingBox();
  const recBox = await rec.boundingBox();
  expect(recBox?.y ?? 0).toBeGreaterThan((composerBox?.y ?? 0) + (composerBox?.height ?? 0) - 1);
  expect(await page.evaluate(() => window.__mic.opened)).toBe(0); // no microphone until the user presses Record
  expect(sent).toHaveLength(0);

  // reference takes take comments too, so they get the recorder
  const ref = await trackByTitle(request, "Syn ref a");
  await page.goto(`/tracks/${ref.id}`);
  await expect(recorder(page)).toHaveAttribute("data-state", "idle");
  expect(sent).toHaveLength(0);
  expect(watch.problems()).toEqual([]);
});

test("record, stop (the microphone is released), discard: nothing is uploaded", async ({ page, request }) => {
  const watch = watchM5(page);
  await fakeRecorder(page);
  const t = await trackByTitle(request, "Syn song 03");
  const sent = await acceptUploads(page, t.id);
  const before = (await comments(request, t.id)).length;
  await page.goto(`/tracks/${t.id}`);
  await recordSecond(page);
  expect(await page.evaluate(() => window.__mic.opened)).toBe(1);
  expect(await page.evaluate(() => window.__mic.stopped)).toBe(1); // released on stop, not on leaving the page
  await expect(page.getByTestId("voice-recorded")).toContainText(/Recorded 0:0[0-9]/);
  await expect(page.getByTestId("voice-save")).toBeVisible();
  await expect(page.getByTestId("voice-kind")).toHaveValue("note");
  await page.getByTestId("voice-discard").click();
  await expect(recorder(page)).toHaveAttribute("data-state", "idle");
  await page.reload(); // leaving the page with a recording unsaved sends nothing either
  await expect(recorder(page)).toHaveAttribute("data-state", "idle");
  expect(sent).toHaveLength(0);
  expect((await comments(request, t.id)).length).toBe(before);
  expect(watch.problems()).toEqual([]);
});

test("Save sends the recording once, with its kind and a key, and reloads the comments", async ({ page, request }) => {
  const watch = watchM5(page);
  await fakeRecorder(page);
  const t = await trackByTitle(request, "Syn song 03");
  const sent = await acceptUploads(page, t.id);
  await page.goto(`/tracks/${t.id}`);
  await recordSecond(page);
  expect(sent).toHaveLength(0); // stopping is not saving
  const listed = page.waitForResponse((r) => new URL(r.url()).pathname === `/api/tracks/${t.id}/comments` && r.request().method() === "GET");
  await page.getByTestId("voice-kind").selectOption("keep");
  await page.getByTestId("voice-save").click();
  await expect(page.getByTestId("voice-saved")).toBeVisible();
  await expect(recorder(page)).toHaveAttribute("data-state", "saved");
  await expect(page.getByTestId("voice-record")).toHaveText("Record another");
  await listed; // the comment list reloads by itself
  expect(sent).toHaveLength(1);
  const up = sent[0];
  expect(up?.kind).toBe("keep");
  expect(up?.key.length ?? 0).toBeGreaterThan(8);
  expect(up?.at_second ?? null).toBeNull();
  expect(Buffer.from(up?.audio_base64 ?? "", "base64")).toHaveLength(BLOB_BYTES);
  expect(watch.problems()).toEqual([]);
});

test("a note recorded while the take plays is pinned to the playhead", async ({ page, request }) => {
  const watch = watchM5(page);
  await fakeRecorder(page);
  const t = await trackByTitle(request, "Syn song 03");
  const sent = await acceptUploads(page, t.id);
  await page.goto(`/tracks/${t.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(() => audioTime(page), { timeout: 10_000 }).toBeGreaterThan(1.2);
  await page.getByTestId("track-play").click();
  await expect(page.getByTestId("track-play")).toHaveText("Play");
  const at = Number(await recorder(page).getAttribute("data-at"));
  expect(at).toBeGreaterThan(1);
  await recordSecond(page);
  await expect(page.getByTestId("voice-recorded")).toContainText(" at 0:0");
  await page.getByTestId("voice-save").click();
  await expect(page.getByTestId("voice-saved")).toBeVisible();
  expect(sent).toHaveLength(1);
  expect(Math.abs((sent[0]?.at_second ?? -9) - at)).toBeLessThanOrEqual(0.2);
  expect(watch.problems()).toEqual([]);
});

for (const status of [501, 503]) {
  test(`when the voice operation answers ${status} the recorder says so and keeps nothing`, async ({ page, request }) => {
    // the browser logs every non-2xx fetch as a console error: this one is provoked on purpose
    const watch = watchM5(page, [/Failed to load resource/, new RegExp(`^${status} `)]);
    await fakeRecorder(page);
    await page.route("**/voice-notes", (route) =>
      route.fulfill({
        status,
        contentType: "application/problem+json",
        body: JSON.stringify({ code: status === 501 ? "NOT_IMPLEMENTED" : "UNAVAILABLE", message: "voice notes are not available", details: { component: "voice" } }),
      }),
    );
    const t = await trackByTitle(request, "Syn song 03");
    const before = (await comments(request, t.id)).length;
    await page.goto(`/tracks/${t.id}`);
    await recordSecond(page);
    await page.getByTestId("voice-save").click();
    await expect(page.getByTestId("voice-unavailable")).toHaveText("Voice notes are not available on this server.");
    await expect(recorder(page)).toHaveAttribute("data-state", "unavailable");
    await expect(page.getByTestId("voice-record")).toHaveCount(0);
    expect((await comments(request, t.id)).length).toBe(before);
    expect(watch.problems()).toEqual([]);
  });
}

test("a browser that cannot record shows why and offers no button", async ({ page, request }) => {
  const watch = watchM5(page);
  await page.addInitScript(() => {
    Object.defineProperty(window, "MediaRecorder", { value: undefined, configurable: true });
  });
  const t = await trackByTitle(request, "Syn song 03");
  await page.goto(`/tracks/${t.id}`);
  await expect(recorder(page)).toHaveAttribute("data-state", "unsupported");
  await expect(page.getByTestId("voice-unsupported")).toContainText("cannot record audio");
  await expect(page.getByTestId("voice-record")).toHaveCount(0);
  expect(watch.problems()).toEqual([]);
});
