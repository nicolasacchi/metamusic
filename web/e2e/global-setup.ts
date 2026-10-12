import { spawn, type ChildProcess } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { BASE_URL, EDGE_SECRET, HEADERS, PORT } from "./env";
import { missingOperations, uiOperations } from "./coverage";

async function waitHealthy(timeoutMs: number, dead: () => boolean): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (dead()) throw new Error("e2e server exited during startup (see stderr above)");
    try {
      const res = await fetch(`${BASE_URL}/healthz`);
      if (res.ok) return;
    } catch {
      // not listening yet
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`e2e server did not become healthy within ${timeoutMs}ms`);
}

async function stop(child: ChildProcess): Promise<void> {
  if (child.exitCode !== null || child.signalCode !== null) return;
  const exited = new Promise<void>((r) => child.once("exit", () => r()));
  child.kill("SIGTERM");
  const timer = setTimeout(() => child.kill("SIGKILL"), 5000);
  await exited;
  clearTimeout(timer);
}

export default async function globalSetup(): Promise<() => Promise<void>> {
  const bin = resolve(process.env.MM_E2E_BIN ?? resolve(import.meta.dirname, "../../bin/metamusic-e2e"));
  if (!existsSync(bin)) throw new Error(`e2e binary not found at ${bin} (set MM_E2E_BIN)`);

  const stale = await fetch(`${BASE_URL}/healthz`).then(
    () => true,
    () => false,
  );
  if (stale) throw new Error(`port ${PORT} already in use (stale e2e server?); stop it or set MM_E2E_PORT`);

  const root = mkdtempSync(join(tmpdir(), "mm-e2e-data-"));
  const dataDir = join(root, "data");
  const secretsDir = join(root, "secrets");
  mkdirSync(dataDir, { recursive: true });
  mkdirSync(secretsDir, { recursive: true, mode: 0o700 });
  writeFileSync(join(secretsDir, "secret_key"), "0123456789abcdef".repeat(4) + "\n", { mode: 0o600 });
  writeFileSync(join(secretsDir, "edge_secret"), EDGE_SECRET + "\n", { mode: 0o600 });

  const env: Record<string, string> = {
    ...(process.env as Record<string, string>),
    MM_AUTH: "proxy",
    MM_ADDR: `127.0.0.1:${PORT}`,
    MM_PUBLIC_URL: BASE_URL,
    MM_TRUSTED_PROXY: "127.0.0.1/32,::1/128",
    MM_TRUSTED_USERS: HEADERS["Remote-User"],
    MM_E2E: "1",
    MM_DATA_DIR: dataDir,
    MM_SECRETS_DIR: secretsDir,
  };
  const child = spawn(bin, ["serve"], { env, stdio: ["ignore", "ignore", "inherit"] });
  let dead = false;
  let spawnError: Error | undefined;
  child.once("exit", () => {
    dead = true;
  });
  child.once("error", (e) => {
    dead = true;
    spawnError = e;
  });

  const cleanup = async () => {
    await stop(child);
    rmSync(root, { recursive: true, force: true });
  };

  try {
    await waitHealthy(15_000, () => dead);
  } catch (e) {
    await cleanup();
    if (spawnError) throw new Error(`failed to start e2e server: ${spawnError.message}`, { cause: e });
    throw e;
  }

  // Run-scoped teardown: Playwright only calls this when setup succeeded, and it captures this
  // run's own child and data dir, so concurrent or failed runs can never touch another run's server.
  return async () => {
    let failure: Error | undefined;
    try {
      if (process.env.MM_E2E_COVERAGE !== "0") {
        const res = await fetch(`${BASE_URL}/__e2e/served`, { headers: HEADERS });
        if (!res.ok) throw new Error(`/__e2e/served returned ${res.status}`);
        const served = (await res.json()) as string[];
        const missing = missingOperations(uiOperations(), served);
        if (missing.length > 0) {
          failure = new Error(`operations with a UI never served to the SPA: ${missing.join(", ")} (filtered run? set MM_E2E_COVERAGE=0 to skip this check)`);
        }
      }
    } catch (e) {
      failure = e instanceof Error ? e : new Error(String(e));
    } finally {
      await cleanup();
    }
    if (failure) throw failure;
  };
}
