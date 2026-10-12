import { spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { BASE_URL, EDGE_SECRET, HEADERS, PORT, STATE_DIR, STATE_FILE } from "./env";

async function waitHealthy(timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
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

export default async function globalSetup(): Promise<void> {
  const bin = resolve(process.env.MM_E2E_BIN ?? resolve(import.meta.dirname, "../../bin/metamusic-e2e"));
  if (!existsSync(bin)) throw new Error(`e2e binary not found at ${bin} (set MM_E2E_BIN)`);

  const stale = await fetch(`${BASE_URL}/healthz`).then(
    () => true,
    () => false,
  );
  if (stale) throw new Error(`port ${PORT} already in use (stale e2e server?); stop it or set MM_E2E_PORT`);

  rmSync(STATE_DIR, { recursive: true, force: true });
  mkdirSync(STATE_DIR, { recursive: true, mode: 0o700 });
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
  if (!child.pid) throw new Error("failed to start e2e server");
  child.unref();
  writeFileSync(STATE_FILE, JSON.stringify({ pid: child.pid, root }));

  let exited = false;
  child.once("exit", () => {
    exited = true;
  });
  try {
    await waitHealthy(15_000);
    if (exited || child.exitCode !== null) throw new Error("e2e server exited during startup (port conflict?)");
  } catch (e) {
    if (exited) throw new Error("e2e server exited during startup (see stderr above)", { cause: e });
    throw e;
  }
}
