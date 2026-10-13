// crypto.randomUUID exists only in secure contexts; plain-HTTP origins (LAN
// addresses) must still mount, so use getRandomValues, which is always available.
export function randomId(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(8)), (b) => b.toString(16).padStart(2, "0")).join("");
}

let lastMs = 0;
let counter = 0;

/**
 * RFC 9562 UUIDv7: 48-bit Unix ms, version, 12-bit counter in rand_a (monotonic within
 * a millisecond and across a backwards clock step), variant, 62 random bits.
 * Uses getRandomValues only, so it works in insecure contexts.
 */
export function uuidv7(): string {
  const rnd = crypto.getRandomValues(new Uint8Array(10));
  const now = Date.now();
  if (now > lastMs) {
    lastMs = now;
    // Start below the midpoint so a burst inside one millisecond has room before the overflow bump.
    counter = (((rnd[0]! << 8) | rnd[1]!) & 0x7ff);
  } else {
    counter++;
    if (counter > 0xfff) {
      lastMs++;
      counter = 0;
    }
  }
  const ms = lastMs.toString(16).padStart(12, "0");
  const hex = (b: number) => b.toString(16).padStart(2, "0");
  const variant = hex((rnd[2]! & 0x3f) | 0x80);
  const tail = Array.from(rnd.subarray(3, 10), hex).join("");
  const c = counter.toString(16).padStart(3, "0");
  return `${ms.slice(0, 8)}-${ms.slice(8)}-7${c}-${variant}${tail.slice(0, 2)}-${tail.slice(2)}`;
}
