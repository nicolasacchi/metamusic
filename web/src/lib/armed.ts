// The armed flag of a provider or engine backend as one word. An Editor's responses carry no `armed` (redactEstimate,
// budgetBrief in internal/api/editor_u1.go): absent means unknown, never "disarmed" (U1-INT-W). Only an explicit false
// reads as disarmed, the same rule as confirm.ts and ProviderBanner.svelte.
export function armedWord(armed: boolean | null | undefined): "armed" | "disarmed" | "unknown" {
  if (armed === true) return "armed";
  if (armed === false) return "disarmed";
  return "unknown";
}
