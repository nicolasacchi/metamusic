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

test("diag sw over time", async ({ page, browserName }, info) => {
  const logs: string[] = [];
  page.on("console", (m) => logs.push(m.type() + ":" + m.text().slice(0, 160)));
  page.on("pageerror", (e) => logs.push("pageerror:" + String(e).slice(0, 160)));
  await page.addInitScript(() => window.addEventListener("mm:sw-registered", () => ((window as unknown as { __swreg: number }).__swreg = Date.now())));
  await page.goto("/offline");
  const samples: string[] = [];
  for (let i = 0; i < 12; i++) {
    await page.waitForTimeout(1000);
    samples.push(
      await page.evaluate(async () => {
        const regs = await navigator.serviceWorker.getRegistrations();
        const r = regs[0];
        return [regs.length, navigator.serviceWorker.controller !== null, r?.installing?.state, r?.waiting?.state, r?.active?.state, (window as unknown as { __swreg?: number }).__swreg ? "evt" : "-"].join("/");
      }),
    );
  }
  console.log("DIAG-SW2", info.project.name, browserName, samples.join(" "), JSON.stringify(logs.slice(0, 8)));
});

test("diag popup channel after open", async ({ page, context, browserName }, info) => {
  await context.route("**/diag-stepup", (route) =>
    route.fulfill({ contentType: "text/html", body: `<!doctype html><title>c</title><script>const c=new BroadcastChannel("diag2");c.postMessage({type:"done"});setTimeout(()=>window.close(),50);</script>` }),
  );
  await page.goto("/");
  const popupP = context.waitForEvent("page");
  const got = await page.evaluate(async () => {
    const out: unknown[] = [];
    window.open("/diag-stepup", "_blank"); // listener created AFTER window.open, as a slow SPA would
    const c = new BroadcastChannel("diag2");
    c.onmessage = (e) => out.push(e.data);
    await new Promise((r) => setTimeout(r, 3000));
    return out;
  });
  const second = await popupP;
  await page.waitForTimeout(500);
  console.log("DIAG-POP", info.project.name, browserName, JSON.stringify(got), second.isClosed());
});
