<script lang="ts">
  // Select [from, to] seconds on a take: drag on the waveform, resize with the handles, or use the numeric fields and
  // arrow keys (0.1 s, shift 1 s). Shows the engine's limits and plays the window through `onplay`.
  import Waveform from "./Waveform.svelte";
  import { dragRange, keyDelta, nudge, parseField, rangeProblem, snap, xToSeconds } from "../rangeSelect";
  import { fmt } from "../time";

  interface Props {
    duration: number;
    peaks?: readonly number[] | null;
    bits?: number;
    from: number | null;
    to: number | null;
    onchange: (from: number, to: number) => void;
    /** Limits of the window in seconds; 0 = none. */
    min?: number;
    max?: number;
    limitLabel?: string;
    onplay?: (from: number, to: number) => void;
    playing?: boolean;
    label?: string;
  }
  let { duration, peaks = null, bits = 8, from, to, onchange, min = 0, max = 0, limitLabel = "Window", onplay, playing = false, label = "Range" }: Props = $props();

  let strip: HTMLDivElement;
  let width = $state(0);
  let anchor: number | null = null;
  let fromText = $state("");
  let toText = $state("");
  $effect(() => {
    fromText = from === null ? "" : String(from);
    toText = to === null ? "" : String(to);
  });

  const have = $derived(from !== null && to !== null);
  const problem = $derived(have ? rangeProblem({ from: from as number, to: to as number }, { min, max }) : "");
  const left = $derived(have && duration > 0 ? ((from as number) / duration) * 100 : 0);
  const span = $derived(have && duration > 0 ? (((to as number) - (from as number)) / duration) * 100 : 0);
  const limits = $derived(
    [min > 0 ? `at least ${min} s` : "", max > 0 ? `at most ${max} s` : ""].filter(Boolean).join(", "),
  );

  function secondsAt(ev: PointerEvent): number {
    const r = strip.getBoundingClientRect();
    return xToSeconds(ev.clientX - r.left, r.width, duration);
  }
  function down(ev: PointerEvent) {
    if (duration <= 0) return;
    strip.setPointerCapture?.(ev.pointerId);
    anchor = secondsAt(ev);
  }
  function move(ev: PointerEvent) {
    if (anchor === null) return;
    const r = dragRange(anchor, secondsAt(ev));
    if (r.to - r.from >= 0.1) onchange(r.from, r.to);
  }
  function up() {
    anchor = null;
  }
  function key(ev: KeyboardEvent, end: "from" | "to") {
    const d = keyDelta(ev.key, ev.shiftKey);
    if (d === null || !have) return;
    ev.preventDefault();
    const r = nudge({ from: from as number, to: to as number }, end, d, duration);
    onchange(r.from, r.to);
  }
  let handleDrag: "from" | "to" | null = null;
  function hdown(ev: PointerEvent, end: "from" | "to") {
    ev.stopPropagation();
    (ev.currentTarget as HTMLElement).setPointerCapture?.(ev.pointerId);
    handleDrag = end;
  }
  function hmove(ev: PointerEvent) {
    if (!handleDrag || !have) return;
    const s = secondsAt(ev);
    const cur = { from: from as number, to: to as number };
    const r = handleDrag === "from" ? nudge(cur, "from", s - cur.from, duration) : nudge(cur, "to", s - cur.to, duration);
    onchange(r.from, r.to);
  }
  function hup() {
    handleDrag = null;
  }
  function commit() {
    const a = parseField(fromText);
    const b = parseField(toText);
    if (a === null || b === null) return;
    onchange(snap(a), snap(b));
  }
</script>

<div class="range" data-testid="range-select" role="group" aria-label={label}>
  <div class="strip" bind:this={strip} bind:clientWidth={width} onpointerdown={down} onpointermove={move} onpointerup={up} onpointercancel={up} role="presentation">
    <div class="wave"><Waveform {peaks} {duration} current={0} onseek={() => {}} {bits} height={72} label="{label} waveform" /></div>
    {#if have}
      <div class="sel" style="left:{left}%;width:{span}%" data-testid="range-selection"></div>
      <button
        type="button"
        class="handle"
        style="left:{left}%"
        role="slider"
        aria-label="{label} start"
        aria-valuemin={0}
        aria-valuemax={duration}
        aria-valuenow={from}
        data-testid="range-handle-from"
        onpointerdown={(e) => hdown(e, "from")}
        onpointermove={hmove}
        onpointerup={hup}
        onkeydown={(e) => key(e, "from")}
      ></button>
      <button
        type="button"
        class="handle"
        style="left:{left + span}%"
        role="slider"
        aria-label="{label} end"
        aria-valuemin={0}
        aria-valuemax={duration}
        aria-valuenow={to}
        data-testid="range-handle-to"
        onpointerdown={(e) => hdown(e, "to")}
        onpointermove={hmove}
        onpointerup={hup}
        onkeydown={(e) => key(e, "to")}
      ></button>
    {/if}
  </div>
  <div class="fields">
    <label>From (s)<input type="text" inputmode="decimal" bind:value={fromText} onchange={commit} data-testid="range-from" /></label>
    <label>To (s)<input type="text" inputmode="decimal" bind:value={toText} onchange={commit} data-testid="range-to" /></label>
    {#if onplay}
      <button type="button" disabled={!have} onclick={() => have && onplay(from as number, to as number)} data-testid="range-play">{playing ? "Playing…" : "Play window"}</button>
    {/if}
  </div>
  <p class="muted info" data-testid="range-info">
    {#if have}{fmt(from as number)} – {fmt(to as number)} ({snap((to as number) - (from as number))} s){:else}Drag on the waveform to select.{/if}
    {#if limits}· {limitLabel}: {limits}{/if}
  </p>
  {#if problem}<p role="alert" class="bad" data-testid="range-problem">{problem}</p>{/if}
</div>

<style>
  .range {
    min-width: 0;
    max-width: 100%;
  }
  .strip {
    position: relative;
    height: 72px;
    touch-action: none;
    cursor: crosshair;
    overflow: hidden;
  }
  .wave {
    pointer-events: none;
  }
  .sel {
    position: absolute;
    top: 0;
    bottom: 0;
    background: rgba(224, 160, 48, 0.25);
    border-left: 2px solid #e0a030;
    border-right: 2px solid #e0a030;
    pointer-events: none;
    box-sizing: border-box;
  }
  .handle {
    position: absolute;
    top: 0;
    bottom: 0;
    width: 44px;
    min-height: 44px;
    margin-left: -22px;
    padding: 0;
    background: transparent;
    border: 0;
    cursor: ew-resize;
    touch-action: none;
  }
  .handle:focus-visible {
    outline: 2px solid #e0a030;
  }
  .fields {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem;
    align-items: end;
    margin-top: 0.4rem;
  }
  .fields label {
    display: grid;
    gap: 0.15rem;
    font-size: 0.85rem;
  }
  .fields input {
    width: 6rem;
    min-height: 44px;
    box-sizing: border-box;
  }
  .fields button {
    min-height: 44px;
  }
  .info {
    margin: 0.3rem 0 0;
    overflow-wrap: anywhere;
  }
  .bad {
    color: #e07060;
    margin: 0.2rem 0 0;
  }
</style>
