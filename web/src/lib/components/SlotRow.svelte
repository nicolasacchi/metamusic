<script lang="ts">
  import type { components } from "../../api/schema";
  import { fmt } from "../time";
  import type { SlotModel } from "../recordView";
  import DownIcon from "./icons/DownIcon.svelte";
  import GripIcon from "./icons/GripIcon.svelte";
  import PlayIcon from "./icons/PlayIcon.svelte";
  import UpIcon from "./icons/UpIcon.svelte";

  type TakeCard = components["schemas"]["TakeCard"];
  type Side = "A" | "B" | "C" | "D";
  interface Props {
    model: SlotModel;
    editable: boolean;
    busy?: boolean;
    selected?: boolean;
    dragging?: boolean;
    onup: () => void;
    ondown: () => void;
    /** pointerdown on the drag handle (desktop). */
    onhandle?: (e: PointerEvent) => void;
    onpin: (takeId: string) => void;
    onunpin: () => void;
    onside: (side: Side | null) => void;
    onremove: () => void;
    onselect?: () => void;
    onplay?: (takeId: string) => void;
    loadTakes?: (songId: string) => Promise<TakeCard[]>;
  }
  let { model, editable, busy = false, selected = false, dragging = false, onup, ondown, onhandle, onpin, onunpin, onside, onremove, onselect, onplay, loadTakes }: Props = $props();

  const s = $derived(model.slot);
  let choosing = $state(false);
  let takes = $state<TakeCard[]>([]);
  async function openChoose() {
    choosing = !choosing;
    if (choosing && loadTakes) takes = await loadTakes(s.song.id);
  }
</script>

<li class="slot" class:selected class:dragging data-testid="slot-row" data-slot-id={s.id} data-position={s.position}>
  <div class="line1">
    {#if editable}
      <button type="button" class="grip" aria-label="Drag to reorder {s.song.title}" tabindex="-1" onpointerdown={onhandle}><GripIcon size={20} /></button>
    {/if}
    <span class="pos num">{s.position}</span>
    <div class="main">
      <a class="song tap" href="/songs/{s.song.id}">{s.song.title}</a>
      {#if s.song.retired && s.song.retired_into}<a class="chip warn" href="/songs/{s.song.retired_into}">merged</a>{/if}
      {#if s.take}
        <span class="take muted">
          {s.take.title} · <span class="num">{fmt(s.take.duration_s)}</span>
          {#if s.take.lossy}<span class="chip warn">lossy</span>{/if}
          {#if s.take.stars !== null}<span class="num" aria-label="{s.take.stars} stars">{s.take.stars}★</span>{/if}
        </span>
      {:else}
        <span class="take muted">No playable take</span>
      {/if}
    </div>
    {#if s.take && onplay}
      <button type="button" class="icon" aria-label="Play {s.take.title}" onclick={() => onplay?.(s.take!.id)}><PlayIcon size={20} /></button>
    {/if}
    {#if editable}
      <span class="move">
        <button type="button" class="icon" aria-label="Move {s.song.title} up" disabled={busy || !model.canUp} onclick={onup} data-testid="slot-up"><UpIcon size={20} /></button>
        <button type="button" class="icon" aria-label="Move {s.song.title} down" disabled={busy || !model.canDown} onclick={ondown} data-testid="slot-down"><DownIcon size={20} /></button>
      </span>
    {/if}
  </div>

  <div class="line2">
    <span class="chip" class:pick={s.mode === "pinned"} data-testid="slot-mode">{model.pin.label}</span>
    {#if model.pinDiffers}<span class="chip warn" data-testid="slot-pin-differs">pin differs from the Pick</span>{/if}
    {#if s.side}<span class="chip">Side {s.side}</span>{/if}
    {#if s.why}<span class="why muted" class:undecided={model.undecided} data-testid="slot-why">{s.why}</span>{/if}
    {#if onselect}<button type="button" class="link" aria-pressed={selected} onclick={onselect}>History</button>{/if}
  </div>

  {#each model.warnings as w (w)}
    <p class="warning" role="note" data-testid="slot-warning">{w}</p>
  {/each}

  {#if editable}
    <div class="line3">
      {#if model.pin.pin && s.take}
        <button type="button" disabled={busy} onclick={() => onpin(s.take!.id)} data-testid="slot-pin">{s.mode === "pinned" ? "Pin current take" : "Pin"}</button>
        <button type="button" disabled={busy} aria-expanded={choosing} onclick={openChoose}>Other take…</button>
      {/if}
      {#if model.pin.unpin}
        <button type="button" disabled={busy} onclick={onunpin} data-testid="slot-unpin">Unpin</button>
      {/if}
      <label class="side">
        <span class="muted">Side</span>
        <select disabled={busy} value={s.side ?? ""} onchange={(e) => onside(e.currentTarget.value === "" ? null : (e.currentTarget.value as Side))} aria-label="Side for {s.song.title}">
          <option value="">none</option>
          {#each ["A", "B", "C", "D"] as o (o)}<option value={o}>{o}</option>{/each}
        </select>
      </label>
      <button type="button" class="danger" disabled={busy} onclick={onremove} aria-label="Remove {s.song.title}" data-testid="slot-remove">Remove</button>
    </div>
    {#if choosing}
      <ul class="menu" data-testid="slot-take-menu">
        {#each takes as t (t.id)}
          <li><button type="button" disabled={busy} onclick={() => { choosing = false; onpin(t.id); }}>{t.title} · {fmt(t.duration_s)}{t.lossy ? " · lossy" : ""}</button></li>
        {:else}
          <li class="muted">No takes to pin.</li>
        {/each}
      </ul>
    {/if}
  {/if}
</li>

<style>
  .slot {
    list-style: none;
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    padding: var(--sp-2) var(--sp-3);
    display: grid;
    gap: var(--sp-1);
  }
  .slot.selected {
    border-color: var(--accent);
  }
  .slot.dragging {
    opacity: 0.6;
    border-color: var(--accent);
  }
  .line1,
  .line2,
  .line3 {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--sp-2);
  }
  .line1 {
    flex-wrap: nowrap;
    min-height: var(--hit-lg);
  }
  .pos {
    flex: none;
    min-width: 1.5em;
    color: var(--text-2);
  }
  .main {
    flex: 1;
    min-width: 0;
    display: grid;
  }
  .song {
    font-weight: var(--fw-medium);
    overflow-wrap: anywhere;
  }
  .take {
    font-size: var(--fs-sm);
  }
  .icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: var(--hit);
    min-height: var(--hit);
    padding: 0;
  }
  .move {
    display: flex;
  }
  .grip {
    display: none;
    border: 0;
    cursor: grab;
    touch-action: none;
    min-width: var(--hit);
    min-height: var(--hit);
    align-items: center;
    justify-content: center;
    padding: 0;
  }
  @media (pointer: fine) {
    .grip {
      display: inline-flex;
    }
  }
  .why {
    font-size: var(--fs-sm);
  }
  .why.undecided {
    color: var(--warn);
  }
  .warning {
    margin: 0;
    font-size: var(--fs-sm);
    color: var(--warn);
  }
  .link {
    border: 0;
    color: var(--accent);
    text-decoration: underline;
  }
  button,
  select {
    min-height: var(--hit);
  }
  .side {
    display: inline-flex;
    align-items: center;
    gap: var(--sp-1);
  }
  .menu {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--sp-1);
  }
  .menu button {
    width: 100%;
    text-align: left;
    border-radius: var(--r-sm);
  }
</style>
