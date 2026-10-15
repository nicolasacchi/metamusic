// Usage: bun e2e/served-check.ts <expected-shards> <served.json>...
// The coverage job runs it over the served-operation lists of every e2e shard: the union must cover every operation
// with a UI route (what an unsharded run checks in global-setup), and exactly <expected-shards> lists must exist.
import { missingOperations, servedUnion, uiOperations } from "./coverage";

const [expected, ...files] = process.argv.slice(2);
if (!expected || files.length !== Number(expected)) {
  console.error(`served-check: expected ${expected} served lists, got ${files.length}`);
  process.exit(2);
}
const missing = missingOperations(uiOperations(), servedUnion(files));
if (missing.length > 0) {
  console.error(`operations with a UI never served to the SPA by any shard: ${missing.join(", ")}`);
  process.exit(1);
}
console.log(`served-check: ${files.length} shards cover every UI operation`);
