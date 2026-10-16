// Hermetic e2e: every Playwright worker serves its own metamusic (own data dir, own fake GPU workers, own budgets and
// counters), started from a copy of the template that global-setup seeded once. A server serves one (project, spec file)
// pair and is replaced when the worker moves on, so a count-based spec never sees what an earlier spec of the worker
// created. Specs import `test` and `expect` from here, never from @playwright/test; baseURL points at the worker's server.
import { test as base } from "@playwright/test";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { HEADERS } from "./env";
import { copyTree, startServer, stopServer, type RunningServer } from "./server";

export * from "@playwright/test";

export interface WorkerServer {
  url: string;
  /** The unpacked seed archive of this server (byte-identity export check). */
  seedDir: string;
}

class Pool {
  private cur?: { key: string; srv: RunningServer; dir: string; server: WorkerServer };
  private n = 0;
  constructor(
    private readonly workerIndex: number,
    private readonly bin: string,
    private readonly template: string,
    private readonly runDir: string,
  ) {}

  async acquire(key: string): Promise<WorkerServer> {
    if (this.cur?.key === key) return this.cur.server;
    await this.retire();
    const dir = mkdtempSync(join(tmpdir(), `mm-e2e-w${this.workerIndex}-`));
    copyTree(join(this.template, "data"), join(dir, "data"));
    copyTree(join(this.template, "secrets"), join(dir, "secrets"));
    const srv = await startServer(this.bin, join(dir, "data"), join(dir, "secrets"));
    this.cur = { key, srv, dir, server: { url: srv.url, seedDir: join(dir, "data", "inbox-synthetic") } };
    return this.cur.server;
  }

  /** Records what this server served and any leak the scanner found, then stops it (flushing its coverage data). */
  async retire(): Promise<void> {
    const c = this.cur;
    if (!c) return;
    this.cur = undefined;
    const id = `${this.workerIndex}-${this.n++}`;
    try {
      if (!c.srv.dead()) {
        const served = await fetch(`${c.srv.url}/__e2e/served`, { headers: HEADERS });
        if (served.ok) writeFileSync(join(this.runDir, `served-${id}.json`), await served.text());
        const lk = await fetch(`${c.srv.url}/__e2e/leaks`, { headers: HEADERS });
        if (lk.ok) writeFileSync(join(this.runDir, `leaks-${id}.json`), await lk.text());
      }
    } finally {
      await stopServer(c.srv);
      rmSync(c.dir, { recursive: true, force: true });
    }
  }
}

function need(name: string): string {
  const v = process.env[name];
  if (!v) throw new Error(`${name} is set by global-setup`);
  return v;
}

export const test = base.extend<{ server: WorkerServer; seedDir: string; agentToken: string }, { pool: Pool }>({
  pool: [
    // eslint-disable-next-line no-empty-pattern -- Playwright reads the fixture dependencies from the first parameter
    async ({}, use, workerInfo) => {
      const bin = need("MM_E2E_BIN_RESOLVED");
      const pool = new Pool(workerInfo.workerIndex, bin, need("MM_E2E_TEMPLATE"), need("MM_E2E_RUN_DIR"));
      mkdirSync(need("MM_E2E_RUN_DIR"), { recursive: true });
      await use(pool);
      await pool.retire();
    },
    { scope: "worker" },
  ],
  server: async ({ pool }, use, testInfo) => {
    await use(await pool.acquire(`${testInfo.project.name}|${testInfo.file}`));
  },
  baseURL: async ({ server }, use) => {
    await use(server.url);
  },
  seedDir: async ({ server }, use) => {
    await use(server.seedDir);
  },
  // eslint-disable-next-line no-empty-pattern -- see above
  agentToken: async ({}, use) => {
    await use(need("MM_E2E_AGENT_TOKEN"));
  },
});
