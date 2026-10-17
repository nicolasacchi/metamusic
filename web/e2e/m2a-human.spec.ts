// matrix: @mobile because it covers the human pages (tokens, pairing and devices) the operator uses from the phone (pairing tests run on chrome only at run time)
import { expect, test, type Browser, type BrowserContext } from "./fixtures";
import { openNav, trackByTitle, uniq } from "./helpers";

// Contexts inherit the project's `use` options, proxy identity headers included, unless overridden.
// A paired device must arrive with none of them.
async function plainContext(browser: Browser, baseURL: string): Promise<BrowserContext> {
  return browser.newContext({ extraHTTPHeaders: {}, baseURL });
}

const me = (ctx: BrowserContext) => ctx.request.get("/api/me").then((r) => r.json() as Promise<{ kind: string; name: string }>);

async function mintCode(page: import("./fixtures").Page, name: string): Promise<string> {
  await page.goto("/human/devices");
  await page.getByTestId("pairing-name").fill(name);
  await page.getByTestId("pairing-create").click();
  await expect(page.getByTestId("pairing-qr")).toBeVisible();
  const text = (await page.getByTestId("pairing-code").textContent()) ?? "";
  return text.trim();
}

async function pairWith(ctx: BrowserContext, code: string) {
  const p = await ctx.newPage();
  await p.goto(`/pair#${encodeURIComponent(code)}`);
  await expect(p.locator("#code")).toHaveValue(code);
  await p.getByRole("button", { name: "Pair" }).click();
  await p.waitForURL((u) => u.pathname === "/", { timeout: 15_000 });
  return p;
}

test("the Tokens link is a full-page navigation; a token authenticates as an agent until revoked", async ({ page, playwright, baseURL }) => {
  await page.goto("/");
  const doc = page.waitForRequest((r) => r.resourceType() === "document" && new URL(r.url()).pathname === "/human/tokens");
  await openNav(page, "Tokens");
  await doc;
  await expect(page.getByRole("heading", { name: "Tokens" })).toBeVisible();

  const name = uniq("e2e-token");
  await page.getByTestId("token-name").fill(name);
  await page.getByTestId("token-create").click();
  const token = (await page.getByTestId("token-value").textContent())?.trim() ?? "";
  expect(token).toMatch(/^mmp_/);
  // the ready-made commands never contain the token
  await expect(page.getByTestId("tpl-claude")).not.toContainText(token);
  await expect(page.getByTestId("tpl-stdio")).not.toContainText(token);

  const agent = await playwright.request.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${token}` } });
  try {
    const who = await agent.get("/api/me");
    expect(who.status()).toBe(200);
    expect(((await who.json()) as { kind: string }).kind).toBe("agent");

    await page.getByTestId("token-dismiss").click();
    await expect(page.getByTestId("token-reveal")).toHaveCount(0);
    const row = page.getByTestId("token-list").locator("tbody tr", { hasText: name });
    await expect(row).toContainText("active");
    page.once("dialog", (d) => void d.accept()); // confirm("Revoke token ...")
    await row.getByTestId("token-revoke").evaluate((b) => (b as HTMLElement).click()); // the row re-renders at once; a retrying click would chase it
    await expect(row).toContainText("revoked");
    expect((await agent.get("/api/me")).status()).toBe(401);
  } finally {
    await agent.dispose();
  }
});

// Minting a pairing code revokes every outstanding code (one active code), so the pairing tests run one
// after the other and only on one project; the mobile projects cover the rest of this spec.
test.describe("pairing", () => {
  test.describe.configure({ mode: "serial" });
  test.beforeEach(({ browserName }, info) => {
    void browserName;
    test.skip(info.project.name !== "chrome", "pairing codes are global: one project only");
  });

  test("pairing: a code makes a device that can rate but never reach /human; unpair returns to /start", async ({ page, browser, baseURL }) => {
    const track = await trackByTitle(page.request, "Syn song 03");
    const code = await mintCode(page, uniq("e2e-phone"));
    expect(code).toMatch(/^[0-9A-Z-]{8,9}$/);

    const ctx = await plainContext(browser, baseURL ?? "");
    try {
      const dev = await pairWith(ctx, code);
      await dev.goto("/");
      await expect(dev.getByTestId("track-table")).toBeVisible();
      // first thing: the principal really is a device, not the human
      expect((await me(ctx)).kind).toBe("device");

      // ratings count as human judging
      await dev.goto(`/tracks/${track.id}`);
      await dev.getByTestId("track-judge").getByRole("button", { name: "3 stars", exact: true }).click();
      await expect.poll(async () => (await ctx.request.get(`/api/tracks/${track.id}`).then((r) => r.json()) as { rating: { human: { stars: number } | null } }).rating.human?.stars).toBe(3);

      // credentials can never be minted from a device
      const tokens = await ctx.request.get("/human/tokens", { maxRedirects: 0 });
      expect([401, 403, 302, 303]).toContain(tokens.status());
      expect((await ctx.request.get("/human/api/tokens")).status()).toBeGreaterThanOrEqual(401);
      await dev.goto("/human/tokens");
      await expect(dev.getByTestId("token-form")).toHaveCount(0);

      // "Unpair this browser" calls revoke_own_device and goes to /start
      await dev.goto("/settings");
      await dev.getByTestId("unpair").click();
      await expect(dev).toHaveURL(/\/start/);
      await expect(dev.getByRole("link", { name: "Pair this device" })).toBeVisible();
      await expect(dev.getByRole("button", { name: "Continue with sign-in" })).toBeVisible();
    } finally {
      await ctx.close();
    }
  });

  test("revoking a device from /human/devices sends its next navigation to /start?expired=1", async ({ page, browser, baseURL }) => {
    const name = uniq("e2e-lost");
    const code = await mintCode(page, name);
    const ctx = await plainContext(browser, baseURL ?? "");
    try {
      const dev = await pairWith(ctx, code);
      await dev.goto("/");
      expect((await me(ctx)).kind).toBe("device");

      await page.goto("/human/devices");
      const row = page.getByTestId("device-list").locator("tbody tr", { hasText: name });
      await expect(row).toContainText("active");
      page.once("dialog", (d) => void d.accept()); // confirm("Unpair ...")
      await row.getByTestId("device-revoke").click();
      await expect(row).toContainText("revoked");

      await dev.goto("/");
      await expect(dev).toHaveURL(/\/start\?expired=1/);
      await expect(dev.getByRole("status")).toContainText("session ended");
    } finally {
      await ctx.close();
    }
  });
});

test("a wrong pairing code is refused without a hint", async ({ browser, baseURL }) => {
  const ctx = await plainContext(browser, baseURL ?? "");
  try {
    const p = await ctx.newPage();
    await p.goto("/pair");
    await p.locator("#code").fill("ZZZZ-ZZZZ");
    await p.getByRole("button", { name: "Pair" }).click();
    await expect(p.getByRole("alert")).toContainText("Code not valid or expired");
    expect((await ctx.request.get("/api/me")).status()).not.toBe(200);
  } finally {
    await ctx.close();
  }
});
