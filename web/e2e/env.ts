import { randomBytes } from "node:crypto";
import { tmpdir } from "node:os";
import { join } from "node:path";

// Throwaway test-only values; the server binds to loopback and uses a temp data dir.
export const PORT = Number(process.env.MM_E2E_PORT ?? 18080);
export const BASE_URL = `http://127.0.0.1:${PORT}`;
// Random hex secret per run, exported via process.env so Playwright workers (children of the
// process that loaded the config) share it; a stale server on the port rejects it.
process.env.MM_E2E_EDGE_SECRET ??= randomBytes(16).toString("hex");
export const EDGE_SECRET = process.env.MM_E2E_EDGE_SECRET;
export const E2E_USER = "e2e";
export const STATE_DIR = join(tmpdir(), `mm-e2e-${PORT}`);
export const STATE_FILE = join(STATE_DIR, "state.json");
export const HEADERS = { "Remote-User": E2E_USER, "X-MM-Edge": EDGE_SECRET };
