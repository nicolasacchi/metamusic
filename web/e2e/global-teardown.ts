import { readFileSync, rmSync } from "node:fs";
import { BASE_URL, HEADERS, STATE_DIR, STATE_FILE } from "./env";
import { missingOperations, uiOperations } from "./coverage";

async function stop(pid: number): Promise<void> {
  try {
    process.kill(pid, "SIGTERM");
  } catch {
    return;
  }
  for (let i = 0; i < 50; i++) {
    await new Promise((r) => setTimeout(r, 100));
    try {
      process.kill(pid, 0);
    } catch {
      return;
    }
  }
  process.kill(pid, "SIGKILL");
}

export default async function globalTeardown(): Promise<void> {
  const state = JSON.parse(readFileSync(STATE_FILE, "utf8")) as { pid: number; root: string };
  let failure: Error | undefined;
  try {
    if (process.env.MM_E2E_COVERAGE === "0") return;
    const res = await fetch(`${BASE_URL}/__e2e/served`, { headers: HEADERS });
    if (!res.ok) throw new Error(`/__e2e/served returned ${res.status}`);
    const served = (await res.json()) as string[];
    const missing = missingOperations(uiOperations(), served);
    if (missing.length > 0) {
      failure = new Error(`operations with a UI never served to the SPA: ${missing.join(", ")} (filtered run? set MM_E2E_COVERAGE=0 to skip this check)`);
    }
  } catch (e) {
    failure = e instanceof Error ? e : new Error(String(e));
  } finally {
    await stop(state.pid);
    rmSync(state.root, { recursive: true, force: true });
    rmSync(STATE_DIR, { recursive: true, force: true });
  }
  if (failure) throw failure;
}
