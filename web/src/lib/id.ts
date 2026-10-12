// crypto.randomUUID exists only in secure contexts; plain-HTTP origins (LAN
// addresses) must still mount, so use getRandomValues, which is always available.
export function randomId(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(8)), (b) => b.toString(16).padStart(2, "0")).join("");
}
