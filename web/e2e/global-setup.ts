import { randomBytes } from "node:crypto";
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { EDGE_SECRET, HEADERS } from "./env";
import { missingOperations, uiOperations } from "./coverage";
import { serverEnv, startServer, stopServer, freePort } from "./server";

/**
 * The agent of the approval spec (C86.5): a read+write+generate PAT made through the human create_token op, and a
 * per-token auto-approve limit of one take so that every agent batch of two or more takes waits for the operator.
 * The token lives in the seeded database, so every per-worker copy of it knows the token; workers get its value
 * through process.env (never written to disk or logged).
 */
async function createAgentToken(base: string): Promise<void> {
  const post = async (method: "POST" | "PUT", path: string, body: unknown, key?: string) => {
    const res = await fetch(`${base}${path}`, {
      method,
      headers: { ...HEADERS, "Content-Type": "application/json", ...(key ? { "Idempotency-Key": key } : {}) },
      body: JSON.stringify(body),
    });
    if (!res.ok) throw new Error(`${method} ${path} -> ${res.status} ${await res.text()}`);
    return (await res.json()) as Record<string, unknown>;
  };
  const tok = await post("POST", "/human/api/tokens", { name: "e2e-agent", scopes: ["read", "write", "generate"] }, `e2e-setup-${randomBytes(6).toString("hex")}`);
  if (typeof tok.token !== "string" || typeof tok.id !== "string") throw new Error("create_token returned no token");
  process.env.MM_E2E_AGENT_TOKEN = tok.token;
  await post("PUT", "/human/api/spend/caps", { tokens: [{ token_id: tok.id, auto_approve_takes: 1, auto_approve_micro_usd: null, daily_budget_micro_usd: null }] });
}

/**
 * Seeds ONE template (data + secrets dir) and quiesces it: the server that made the agent token is stopped, which
 * checkpoints the SQLite WAL, so the template is a plain consistent file tree. Every Playwright worker then serves its
 * own copy of it (fixtures.ts), so no spec shares budgets, counters, fake GPU workers or play counts with another.
 */
export default async function globalSetup(): Promise<() => Promise<void>> {
  const bin = resolve(process.env.MM_E2E_BIN ?? resolve(import.meta.dirname, "../../bin/metamusic-e2e"));
  if (!existsSync(bin)) throw new Error(`e2e binary not found at ${bin} (set MM_E2E_BIN)`);

  const root = mkdtempSync(join(tmpdir(), "mm-e2e-"));
  const template = join(root, "template");
  const dataDir = join(template, "data");
  const secretsDir = join(template, "secrets");
  const runDir = join(root, "run");
  mkdirSync(dataDir, { recursive: true });
  mkdirSync(runDir);
  mkdirSync(secretsDir, { recursive: true, mode: 0o700 });
  writeFileSync(join(secretsDir, "secret_key"), "0123456789abcdef".repeat(4) + "\n", { mode: 0o600 });
  writeFileSync(join(secretsDir, "edge_secret"), EDGE_SECRET + "\n", { mode: 0o600 });
  // Canary for the leak scanner (C39): its value must never appear in any response body or header.
  writeFileSync(join(secretsDir, "ntfy_token"), `canary-${randomBytes(16).toString("hex")}\n`, { mode: 0o600 });
  process.env.MM_E2E_BIN_RESOLVED = bin;
  process.env.MM_E2E_TEMPLATE = template;
  process.env.MM_E2E_RUN_DIR = runDir;

  // Seed the synthetic library (C22) plus the M2c reference and lossy takes (C66): synchronous; a failed seed fails the whole setup.
  const url = `http://127.0.0.1:${await freePort()}`;
  const seed = spawnSync(bin, ["seed-synthetic", "--profile", "e2e", "--references", "--short-audio"], { env: serverEnv(dataDir, secretsDir, url), stdio: ["ignore", "ignore", "inherit"] });
  if (seed.status !== 0) {
    rmSync(root, { recursive: true, force: true });
    throw new Error(`seed-synthetic failed (exit ${seed.status ?? seed.signal ?? "unknown"}${seed.error ? `: ${seed.error.message}` : ""})`);
  }
  try {
    const srv = await startServer(bin, dataDir, secretsDir);
    try {
      await createAgentToken(srv.url);
    } finally {
      await stopServer(srv);
    }
  } catch (e) {
    rmSync(root, { recursive: true, force: true });
    throw e;
  }

  // Run-scoped teardown: every retired worker server left its served-operation list and leak findings in runDir
  // (fixtures.ts); this checks the union. Playwright only calls it when setup succeeded.
  return async () => {
    let failure: Error | undefined;
    try {
      // A sharded run (CI) serves only part of the operations: it writes what it served to MM_E2E_SERVED_OUT and
      // `bun e2e/served-check.ts` (the coverage job) checks the union of every shard against the registry.
      const servedOut = process.env.MM_E2E_SERVED_OUT;
      if (servedOut || process.env.MM_E2E_COVERAGE !== "0") {
        const served = new Set<string>();
        for (const f of readdirSync(runDir).filter((n) => n.startsWith("served-"))) for (const id of JSON.parse(readFileSync(join(runDir, f), "utf8")) as string[]) served.add(id);
        if (servedOut) {
          writeFileSync(servedOut, JSON.stringify([...served].sort()));
        } else {
          const missing = missingOperations(uiOperations(), [...served]);
          if (missing.length > 0) {
            failure = new Error(`operations with a UI never served to the SPA: ${missing.join(", ")} (filtered run? set MM_E2E_COVERAGE=0 to skip this check)`);
          }
        }
      }
      // The leak scanner of each server saw every request and response of its life (C39); it must have found no secret value.
      const leaks: unknown[] = [];
      for (const f of readdirSync(runDir).filter((n) => n.startsWith("leaks-"))) leaks.push(...(JSON.parse(readFileSync(join(runDir, f), "utf8")) as unknown[]));
      if (leaks.length > 0) {
        // Entries name the kind, place and path of a leak, never its value.
        failure = new Error(`secret leak scanner reported ${leaks.length} finding(s): ${JSON.stringify(leaks)}`, { cause: failure });
      }
    } catch (e) {
      failure = e instanceof Error ? e : new Error(String(e));
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
    if (failure) throw failure;
  };
}
