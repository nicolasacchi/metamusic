import { tmpdir } from "node:os";
import { join } from "node:path";

// Throwaway test-only values; the server binds to loopback and uses a temp data dir.
export const PORT = Number(process.env.MM_E2E_PORT ?? 18080);
export const BASE_URL = `http://127.0.0.1:${PORT}`;
export const EDGE_SECRET = "e2e0edge0secret0e2e0edge0secret00";
export const E2E_USER = "e2e";
export const STATE_DIR = join(tmpdir(), `mm-e2e-${PORT}`);
export const STATE_FILE = join(STATE_DIR, "state.json");
export const HEADERS = { "Remote-User": E2E_USER, "X-MM-Edge": EDGE_SECRET };
