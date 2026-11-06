// matrix: chrome-only because Firefox CI has no audio device to run an AudioContext on, so the real recording is Chrome's; the recorder's states run on Firefox too in m5-voice
// M5 INT-W: the voice recorder saving a real recording. The microphone is a synthetic tone: the page's getUserMedia is replaced
// by an oscillator stream (no hardware, no audio file in the repo), Chrome's own MediaRecorder records it, and the bytes are a
// real WebM/Opus recording that the real server decodes, stores and serves back as a comment with a voice note.
import { expect, test, type Page } from "./fixtures";
import { audioTime, trackByTitle } from "./helpers";
import { comments, watchM5 } from "./m5Api";

test.describe.configure({ mode: "serial", timeout: 120_000 });

declare global {
  interface Window {
    __mic: { opened: number; stopped: number };
  }
}

/** A tone instead of a microphone: getUserMedia answers with the stream of an oscillator, and counts opens and stops. */
async function fakeMic(page: Page): Promise<void> {
  await page.addInitScript(() => {
    window.__mic = { opened: 0, stopped: 0 };
    const md = navigator.mediaDevices;
    md.getUserMedia = async () => {
      const ctx = new AudioContext();
      await ctx.resume();
      const osc = ctx.createOscillator();
      osc.frequency.value = 440;
      const dest = ctx.createMediaStreamDestination();
      osc.connect(dest);
      osc.start();
      window.__mic.opened++;
      const stream = dest.stream;
      for (const t of stream.getTracks()) {
        const stop = t.stop.bind(t);
        t.stop = () => {
          window.__mic.stopped++;
          osc.stop();
          void ctx.close();
          stop();
        };
      }
      return stream;
    };
  });
}

const recorder = (page: Page) => page.getByTestId("voice-recorder");

/** Records about a second of the tone and stops; leaves the recorder in its recorded state. */
async function recordSecond(page: Page): Promise<void> {
  await page.getByTestId("voice-record").click();
  // the first AudioContext of a cold Firefox can take several seconds
  await expect(recorder(page)).toHaveAttribute("data-state", "recording", { timeout: 30_000 });
  await expect(page.getByTestId("voice-timer")).toContainText("of 2:00");
  await page.waitForTimeout(1200);
  await page.getByTestId("voice-stop").click();
  await expect(recorder(page)).toHaveAttribute("data-state", "recorded");
}

/** Counts the uploads the page sends. */
function countUploads(page: Page): { n: () => number } {
  let n = 0;
  page.on("request", (r) => {
    if (r.method() === "POST" && new URL(r.url()).pathname.endsWith("/voice-notes")) n++;
  });
  return { n: () => n };
}

test("Save uploads the recording once, and the voice note appears in the comments", async ({ page, request }) => {
  const watch = watchM5(page);
  await fakeMic(page);
  const uploads = countUploads(page);
  const t = await trackByTitle(request, "Syn song 03");
  const before = (await comments(request, t.id)).filter((c) => c.voice).length;
  await page.goto(`/tracks/${t.id}`);
  await recordSecond(page);
  expect(uploads.n()).toBe(0); // stopping is not saving
  const saved = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname.endsWith("/voice-notes"));
  await page.getByTestId("voice-save").click();
  const resp = await saved;
  expect(resp.ok(), `create_voice_note answered ${resp.status()}: ${await resp.text()}`).toBe(true);
  await expect(page.getByTestId("voice-saved")).toBeVisible();
  await expect(recorder(page)).toHaveAttribute("data-state", "saved");
  await expect(page.getByTestId("voice-record")).toHaveText("Record another");
  expect(uploads.n()).toBe(1);

  const voiced = (await comments(request, t.id)).filter((c) => c.voice);
  expect(voiced).toHaveLength(before + 1);
  const note = voiced[voiced.length - 1] ?? voiced[0];
  expect(note?.kind).toBe("note");
  expect(note?.voice?.duration_s ?? 0).toBeGreaterThan(0.3);
  // the comment list reloaded by itself: the note is there without a page reload
  await expect(page.getByTestId("comment").filter({ hasText: /voice note/i })).not.toHaveCount(0);
  expect(watch.problems()).toEqual([]);
});

test("a note recorded while the take plays is pinned to the playhead", async ({ page, request }) => {
  const watch = watchM5(page);
  await fakeMic(page);
  const t = await trackByTitle(request, "Syn song 03");
  await page.goto(`/tracks/${t.id}`);
  await page.getByTestId("track-play").click();
  await expect.poll(() => audioTime(page), { timeout: 10_000 }).toBeGreaterThan(1.2);
  await page.getByTestId("track-play").click();
  await expect(page.getByTestId("track-play")).toHaveText("Play");
  const at = Number(await recorder(page).getAttribute("data-at"));
  expect(at).toBeGreaterThan(1);
  await recordSecond(page);
  await expect(page.getByTestId("voice-recorded")).toContainText(" at 0:0");
  const saved = page.waitForResponse((r) => r.request().method() === "POST" && new URL(r.url()).pathname.endsWith("/voice-notes"));
  await page.getByTestId("voice-kind").selectOption("fix");
  await page.getByTestId("voice-save").click();
  const resp = await saved;
  expect(resp.ok(), `create_voice_note answered ${resp.status()}: ${await resp.text()}`).toBe(true);
  await expect(page.getByTestId("voice-saved")).toBeVisible();
  const pinned = (await comments(request, t.id)).find((c) => c.voice && c.kind === "fix");
  expect(pinned).toBeDefined();
  expect(Math.abs((pinned?.at_second ?? -9) - at)).toBeLessThanOrEqual(0.2);
  expect(watch.problems()).toEqual([]);
});

