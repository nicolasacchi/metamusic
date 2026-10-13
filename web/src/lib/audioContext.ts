// One shared AudioContext (D-M2b-13): iOS limits the number of contexts and resumes only one inside
// a user gesture. Created lazily, never closed.
let shared: AudioContext | null = null;

export function getAudioContext(): AudioContext {
  if (shared && shared.state !== "closed") return shared;
  const Ctor: typeof AudioContext | undefined =
    globalThis.AudioContext ?? (globalThis as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
  if (!Ctor) throw new Error("Web Audio is not available in this browser");
  try {
    shared = new Ctor({ latencyHint: "interactive", sampleRate: 48000 });
  } catch {
    shared = new Ctor({ latencyHint: "interactive" });
  }
  return shared;
}
