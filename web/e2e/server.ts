// Launching one metamusic e2e server: shared by global-setup (the template that is seeded once) and the
// per-worker fixture (a fresh copy of that template per Playwright worker and spec file, see fixtures.ts).
import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { createServer } from "node:net";
import { HEADERS } from "./env";

export interface RunningServer {
  url: string;
  child: ChildProcess;
  /** Resolves when the process has exited (it flushes its coverage data on SIGTERM). */
  exited: Promise<void>;
  dead: () => boolean;
}

/** A free loopback port: bound to 0 and released. Concurrent runs and workers never share a fixed range. */
export function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const s = createServer();
    s.once("error", reject);
    s.listen(0, "127.0.0.1", () => {
      const addr = s.address();
      s.close(() => (addr && typeof addr === "object" ? resolve(addr.port) : reject(new Error("no port"))));
    });
  });
}

export function serverEnv(dataDir: string, secretsDir: string, url: string): Record<string, string> {
  const u = new URL(url);
  return {
    ...(process.env as Record<string, string>),
    MM_AUTH: "proxy",
    MM_ADDR: `127.0.0.1:${u.port}`,
    MM_PUBLIC_URL: url,
    MM_TRUSTED_PROXY: "127.0.0.1/32,::1/128",
    MM_TRUSTED_USERS: HEADERS["Remote-User"],
    MM_E2E: "1",
    MM_DATA_DIR: dataDir,
    MM_SECRETS_DIR: secretsDir,
    // the schedule is off so a backup only ever runs when a spec asks for it; the rate buckets are scaled up because
    // one e2e human principal makes far more requests than a person would (D-M2a-10)
    MM_RATE_SCALE: "100",
    MM_BACKUP_AT: "off",
    // M3 (C86.5): the fake backend finishes a take in about a second; specs steer slower ones with "fake:slow=<s>" in the style.
    MM_BACKEND: "fake",
    MM_FAKE_BACKEND: "fast",
  };
}

export async function startServer(bin: string, dataDir: string, secretsDir: string, timeoutMs = 15_000): Promise<RunningServer> {
  const port = await freePort();
  const url = `http://127.0.0.1:${port}`;
  const child = spawn(bin, ["serve"], { env: serverEnv(dataDir, secretsDir, url), stdio: ["ignore", "ignore", "inherit"] });
  let dead = false;
  let spawnError: Error | undefined;
  const exited = new Promise<void>((r) => {
    child.once("exit", () => {
      dead = true;
      r();
    });
  });
  child.once("error", (e) => {
    dead = true;
    spawnError = e;
  });
  const srv: RunningServer = { url, child, exited, dead: () => dead };
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (dead) throw new Error(`e2e server exited during startup${spawnError ? `: ${spawnError.message}` : " (see stderr above)"}`);
    try {
      if ((await fetch(`${url}/healthz`)).ok) return srv;
    } catch {
      // not listening yet
    }
    await new Promise((r) => setTimeout(r, 25));
  }
  await stopServer(srv);
  throw new Error(`e2e server did not become healthy within ${timeoutMs}ms`);
}

export async function stopServer(srv: RunningServer): Promise<void> {
  if (srv.dead()) return;
  srv.child.kill("SIGTERM");
  const timer = setTimeout(() => srv.child.kill("SIGKILL"), 5000);
  await srv.exited;
  clearTimeout(timer);
}

/** Copies a quiesced data/secrets tree (reflink where the filesystem has it, preserving modes and times). */
export function copyTree(src: string, dst: string): void {
  let r = spawnSync("cp", ["-a", "--reflink=auto", src, dst], { stdio: "ignore" });
  if (r.status !== 0) r = spawnSync("cp", ["-a", src, dst], { stdio: "inherit" });
  if (r.status !== 0) throw new Error(`copying ${src} failed`);
}

