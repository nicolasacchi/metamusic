// Init script that records the Web Audio scheduling calls of the A/B engine into window.__ab. The duel
// specs assert what was scheduled (same `when`/`offset` for both sources, 8 ms ramps), not audible output,
// so they do not depend on a headless AudioContext advancing its clock.
import type { Page } from "@playwright/test";

export interface AbStart {
  node: number;
  when: number;
  offset: number;
}
export interface AbParamCall {
  param: number;
  kind: "set" | "ramp" | "cancel";
  value: number | null;
  time: number;
}
export interface AbLog {
  starts: AbStart[];
  params: AbParamCall[];
}

function spy(): void {
  const log: AbLog = { starts: [], params: [] };
  (window as unknown as { __ab: AbLog }).__ab = log;
  const ids = new WeakMap<object, number>();
  let next = 0;
  const idOf = (o: object): number => {
    let n = ids.get(o);
    if (n === undefined) {
      n = next++;
      ids.set(o, n);
    }
    return n;
  };
  const src = AudioBufferSourceNode.prototype;
  const start = src.start;
  src.start = function (this: AudioBufferSourceNode, when?: number, offset?: number, duration?: number) {
    log.starts.push({ node: idOf(this), when: when ?? 0, offset: offset ?? 0 });
    return start.call(this, when, offset, duration);
  } as typeof src.start;
  const p = AudioParam.prototype;
  const set = p.setValueAtTime;
  p.setValueAtTime = function (this: AudioParam, v: number, t: number) {
    log.params.push({ param: idOf(this), kind: "set", value: v, time: t });
    return set.call(this, v, t);
  };
  const ramp = p.linearRampToValueAtTime;
  p.linearRampToValueAtTime = function (this: AudioParam, v: number, t: number) {
    log.params.push({ param: idOf(this), kind: "ramp", value: v, time: t });
    return ramp.call(this, v, t);
  };
  const cancel = p.cancelScheduledValues;
  p.cancelScheduledValues = function (this: AudioParam, t: number) {
    log.params.push({ param: idOf(this), kind: "cancel", value: null, time: t });
    return cancel.call(this, t);
  };
}

/** Install before navigation (page.addInitScript runs in every new document). */
export async function installAbSpy(page: Page): Promise<void> {
  await page.addInitScript(spy);
}

export const readAb = (page: Page): Promise<AbLog> => page.evaluate(() => (window as unknown as { __ab: AbLog }).__ab);

/** For each param that was ramped: the ramp's end time minus the preceding setValueAtTime time, and its target. */
export function crossfades(log: AbLog): { param: number; dt: number; target: number }[] {
  const out: { param: number; dt: number; target: number }[] = [];
  for (const r of log.params.filter((c) => c.kind === "ramp")) {
    const s = [...log.params].reverse().find((c) => c.kind === "set" && c.param === r.param && c.time <= r.time);
    if (s) out.push({ param: r.param, dt: r.time - s.time, target: r.value ?? NaN });
  }
  return out;
}
