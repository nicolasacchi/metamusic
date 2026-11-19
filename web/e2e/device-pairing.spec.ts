// matrix: chrome-only because pairing codes and device rows are per user and the flow is asserted once, in the listener project
import { expect, test, type Browser, type BrowserContext, type Page } from "./fixtures";
import { E2E_USER, LISTENER_USER, headersFor } from "./env";
import { uniq } from "./helpers";
import { asOwner } from "./u1Api";

// U1 D1, C314.7: a Listener pairs a phone and unpairs it from their own My devices page; the Owner's Devices page lists it with
// an owner column and can unpair it; the Listener never sees the Owner's devices and cannot revoke them.
test.describe.configure({ mode: "serial", timeout: 150_000 });

// A paired device arrives with none of the project's identity headers.
const plainContext = (browser: Browser, baseURL: string): Promise<BrowserContext> => browser.newContext({ extraHTTPHeaders: {}, baseURL });

async function mintCode(page: Page, name: string): Promise<string> {
  await page.goto("/human/devices");
  await page.getByTestId("pairing-name").fill(name);
  await page.getByTestId("pairing-create").click();
  await expect(page.getByTestId("pairing-qr")).toBeVisible();
  return ((await page.getByTestId("pairing-code").textContent()) ?? "").trim();
}

async function pairWith(ctx: BrowserContext, code: string): Promise<Page> {
  const p = await ctx.newPage();
  await p.goto(`/pair#${encodeURIComponent(code)}`);
  await expect(p.locator("#code")).toHaveValue(code);
  await p.getByRole("button", { name: "Pair" }).click();
  await p.waitForURL((u) => u.pathname === "/", { timeout: 15_000 });
  return p;
}

const rowOf = (page: Page, name: string) => page.getByTestId("device-list").locator("tbody tr", { hasText: name });

test("a Listener pairs and unpairs their own device; the Owner sees and revokes it; neither sees the other's", async ({ page, browser, baseURL, request, playwright }) => {
  const base = baseURL ?? "";
  const ownerName = uniq("e2e-owner-phone");
  const own1 = uniq("e2e-carol-phone");
  const own2 = uniq("e2e-carol-tablet");
  const ownerCtx = await browser.newContext({ extraHTTPHeaders: headersFor(E2E_USER), baseURL: base });
  const ownerApi = await asOwner(playwright, base);
  const phones: BrowserContext[] = [];
  try {
    // the Owner pairs a phone (Owner device: owner column "Owner")
    const ownerPage = await ownerCtx.newPage();
    const ownerPhone = await plainContext(browser, base);
    phones.push(ownerPhone);
    await pairWith(ownerPhone, await mintCode(ownerPage, ownerName));
    const ownerDevice = ((await (await ownerApi.get("/human/api/devices")).json()) as { items: { id: string; name: string }[] }).items.find((d) => d.name === ownerName);
    expect(ownerDevice, "the Owner's device is listed to the Owner").toBeDefined();

    // the Listener pairs two devices; they are rated as carol's, and the page is "My devices" with no owner column and no Tokens link
    await page.goto("/human/devices");
    await expect(page.getByRole("heading", { name: "My devices" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Tokens" })).toHaveCount(0);
    const phone1 = await plainContext(browser, base);
    const phone2 = await plainContext(browser, base);
    phones.push(phone1, phone2);
    await pairWith(phone1, await mintCode(page, own1));
    await pairWith(phone2, await mintCode(page, own2));
    const who = (await phone1.request.get("/api/me").then((r) => r.json())) as { kind: string; role?: string; user?: string };
    expect(who.kind).toBe("device");
    expect(who.user).toBe(LISTENER_USER);
    // a Listener's device never reaches the Owner's pages
    expect((await phone1.request.get("/human/api/tokens")).status()).toBeGreaterThanOrEqual(401);

    await page.goto("/human/devices");
    await expect(rowOf(page, own1)).toContainText("active");
    await expect(rowOf(page, own2)).toContainText("active");
    await expect(rowOf(page, ownerName), "the Listener never sees the Owner's devices").toHaveCount(0);
    await expect(page.getByTestId("device-owner")).toHaveCount(0);
    const mine = ((await (await request.get("/human/api/devices")).json()) as { items: { id: string }[] }).items;
    expect(mine.map((d) => d.id)).not.toContain(ownerDevice!.id);
    // nor can the Listener revoke it
    const refused = await request.post(`/human/api/devices/${ownerDevice!.id}/revoke`, { data: {}, headers: { "Idempotency-Key": uniq("rv") } });
    expect(refused.status()).toBe(404);

    // the Owner sees both Listener devices with the owner column, and their own as "Owner"
    const op = await ownerCtx.newPage();
    await op.goto("/human/devices");
    await expect(op.getByRole("heading", { name: "Devices", exact: true })).toBeVisible();
    await expect(rowOf(op, own1).getByTestId("device-owner")).toHaveText(LISTENER_USER);
    await expect(rowOf(op, own2).getByTestId("device-owner")).toHaveText(LISTENER_USER);
    await expect(rowOf(op, ownerName).getByTestId("device-owner")).toHaveText("Owner");

    // the Listener unpairs the first device from their own page
    page.once("dialog", (d) => void d.accept());
    await rowOf(page, own1).getByTestId("device-revoke").click();
    await expect(rowOf(page, own1)).toContainText("revoked");
    const p1 = await phone1.newPage();
    await p1.goto("/");
    await expect(p1).toHaveURL(/\/start\?expired=1/);

    // the Owner unpairs the second one
    await op.goto("/human/devices");
    op.once("dialog", (d) => void d.accept());
    await rowOf(op, own2).getByTestId("device-revoke").click();
    await expect(rowOf(op, own2)).toContainText("revoked");
    const p2 = await phone2.newPage();
    await p2.goto("/");
    await expect(p2).toHaveURL(/\/start\?expired=1/);
    // the Owner's own phone is untouched
    await expect(rowOf(op, ownerName)).toContainText("active");
  } finally {
    for (const c of phones) await c.close();
    await ownerApi.dispose();
    await ownerCtx.close();
  }
});
