// matrix: @mobile @firefox @android-firefox because TEMPORARY diagnostic of engine capabilities (to be deleted)
import { test } from "./fixtures";

test.use({ serviceWorkers: "allow" });

test("diag service worker", async ({ page, browserName }, info) => {
  const logs: string[] = [];
  page.on("console", (m) => logs.push(m.type() + ":" + m.text().slice(0, 200)));
  await page.goto("/offline");
  await page.waitForTimeout(5000);
  const r = await page.evaluate(async () => {
    const out: Record<string, unknown> = { secure: isSecureContext, has: "serviceWorker" in navigator };
    try {
      const regs = await navigator.serviceWorker.getRegistrations();
      out.regs = regs.length;
      out.controller = navigator.serviceWorker.controller !== null;
      const reg = await navigator.serviceWorker.register("/sw.js", { scope: "/" });
      out.registered = !!reg;
      out.state = (reg.installing ?? reg.waiting ?? reg.active)?.state;
    } catch (e) {
      out.err = String(e);
    }
    return out;
  });
  console.log("DIAG-SW", info.project.name, browserName, JSON.stringify(r), JSON.stringify(logs.slice(0, 6)));
});

test("diag broadcast channel", async ({ page, context, browserName }, info) => {
  await page.goto("/");
  await page.evaluate(() => {
    (window as unknown as { __got: unknown[] }).__got = [];
    const c = new BroadcastChannel("diag");
    c.onmessage = (e) => (window as unknown as { __got: unknown[] }).__got.push(e.data);
    (window as unknown as { __c: unknown }).__c = c;
  });
  const popupP = context.waitForEvent("page");
  await page.evaluate(() => window.open("/offline", "_blank", "noopener"));
  const second = await popupP;
  await second.waitForLoadState();
  await second.evaluate(() => {
    const c = new BroadcastChannel("diag");
    c.postMessage("hi");
  });
  await page.waitForTimeout(2000);
  const got = await page.evaluate(() => (window as unknown as { __got: unknown[] }).__got);
  const vis = await page.evaluate(() => document.visibilityState);
  console.log("DIAG-BC", info.project.name, browserName, JSON.stringify(got), vis);
});
