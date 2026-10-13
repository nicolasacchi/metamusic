import { randomBytes } from "node:crypto";

// Throwaway test-only values; the server binds to loopback and uses a temp data dir.
export const PORT = Number(process.env.MM_E2E_PORT ?? 18080);
export const BASE_URL = `http://127.0.0.1:${PORT}`;
// Random hex secret per run, exported via process.env so Playwright workers (children of the
// process that loaded the config) share it; a stale server on the port rejects it.
process.env.MM_E2E_EDGE_SECRET ??= randomBytes(16).toString("hex");
export const EDGE_SECRET = process.env.MM_E2E_EDGE_SECRET;
export const E2E_USER = "e2e";
export const HEADERS = { "Remote-User": E2E_USER, "X-MM-Edge": EDGE_SECRET };
// Per-run Playwright artifact directory (exported so workers share it): concurrent or overlapping runs
// never write into the same .playwright-artifacts-N tree.
process.env.MM_E2E_OUTPUT_DIR ??= `test-results/run-${process.pid}-${Date.now()}`;
export const OUTPUT_DIR = process.env.MM_E2E_OUTPUT_DIR;

// Seeded library facts (`seed-synthetic --profile e2e --references`, profile C66). Specs derive every count
// from these constants; none carries a bare literal. X1 verifies them against the real seeded server.
/** Tracks with a recipe: the M1/M2a/M2b synthetic library. */
export const SEED_RECIPE_TRACKS = 9;
/** Unmatched FLACs imported as lossless reference tracks (syn_ref_a, syn_ref_b). */
export const SEED_REFERENCE_FLACS = 2;
/** Standalone MP3s imported as lossy reference tracks (trio p1-p3, lone, pair p2/p5). */
export const SEED_LOSSY_TRACKS = 6;
/** Every recipe-less track (the library filter "Recipe: unknown"). */
export const SEED_UNKNOWN_TRACKS = SEED_REFERENCE_FLACS + SEED_LOSSY_TRACKS;
export const SEED_TRACKS = SEED_RECIPE_TRACKS + SEED_UNKNOWN_TRACKS;
/** Audio lines of the seed import run: one per matched FLAC plus one per lossy reference MP3. */
export const SEED_AUDIO_FILES = SEED_RECIPE_TRACKS + SEED_REFERENCE_FLACS + SEED_LOSSY_TRACKS;
