<script lang="ts">
  // Take-page engine actions (E2a, C161): Repaint section, Render with this as reference, and A/B with parent for a
  // repaint child. list_engines is fetched only when the user opens an action, so a plain yue2 take page makes no
  // engine call (it answers 501 until the engines operation lands).
  import { player } from "../player.svelte";
  import { api } from "../../api/client";
  import { newIdempotencyKey } from "../../api/idem";
  import type { components } from "../../api/schema";
  import { createDuel } from "../duelApi";
  import { abWindow } from "../abWindow";
  import { fetchEngines, type EngineList } from "../engines";
  import { engineIntent, referenceIntent, startGeneration } from "../genIntent";
  import { rangeProblem } from "../rangeSelect";
  import { actionLimits, actionReason, isRepaintChild, type TrackAction } from "../trackEngineActions";
  import RangeSelect from "./RangeSelect.svelte";

  type Ace = components["schemas"]["RecipeACE"];
  interface Props {
    trackId: string;
    duration: number;
    peaks: readonly number[] | null;
    bits: number;
    parentId: string | null;
    acestep?: Ace | null;
    /** Start playback at a second (the page's player). */
    playFrom: (s: number) => void;
    go: (to: string) => void;
  }
  let { trackId, duration, peaks, bits, parentId, acestep = null, playFrom, go }: Props = $props();

  let open = $state<TrackAction | null>(null);
  let list = $state<EngineList | null>(null);
  let loading = $state(false);
  let note = $state("");
  let from = $state<number | null>(null);
  let to = $state<number | null>(null);
  let windowEnd = $state<number | null>(null);
  let abBusy = $state(false);
  let error = $state("");

  const reason = $derived(open && list ? actionReason(list, open) : note);
  const limits = $derived(open ? actionLimits(list, open) : { min: 0, max: 0 });
  const problem = $derived(
    open === "repaint" ? (from === null || to === null ? "Select the range to repaint." : rangeProblem({ from, to }, limits)) : open === "reference" && from !== null && to !== null ? rangeProblem({ from, to }, limits) : "",
  );

  async function choose(a: TrackAction) {
    if (open === a) {
      open = null;
      return;
    }
    open = a;
    from = null;
    to = null;
    note = "";
    if (!list) {
      loading = true;
      const r = await fetchEngines();
      loading = false;
      if (r.kind === "ok") list = r.list;
      else note = r.kind === "unavailable" ? "YuE2 is the only engine available." : r.message;
    }
  }

  function set(a: number, b: number) {
    from = a;
    to = b;
  }
  function playWindow(a: number, b: number) {
    windowEnd = b;
    playFrom(a);
  }
  // Stop at the end of the window being auditioned.
  $effect(() => {
    if (windowEnd === null || player.state.trackId !== trackId) return;
    if (player.state.current >= windowEnd) {
      windowEnd = null;
      if (player.state.playing) player.toggle();
    }
  });

  function proceed() {
    if (!open || reason || problem) return;
    if (open === "repaint") {
      if (from === null || to === null) return;
      startGeneration(sessionStorage, { ...engineIntent("repaint", trackId), edit: { from_s: from, to_s: to } }, go);
    } else {
      startGeneration(sessionStorage, from !== null && to !== null ? referenceIntent(trackId, from, to) : referenceIntent(trackId), go);
    }
  }

  const abRange = $derived(
    isRepaintChild(null, acestep?.task) && parentId && acestep?.edit?.from_s != null && acestep.edit.to_s != null
      ? abWindow(acestep.edit.from_s, acestep.edit.to_s, duration)
      : null,
  );
  async function ab() {
    if (!abRange || !parentId || abBusy) return;
    abBusy = true;
    error = "";
    try {
      const d = await createDuel(api, parentId, trackId, newIdempotencyKey(), { blind: false, matchLoudness: true, fromS: abRange.from_s, toS: abRange.to_s });
      go(`/duels/${encodeURIComponent(d.id)}`);
    } catch (e) {
      error = e instanceof Error ? e.message : "Could not create the duel";
    } finally {
      abBusy = false;
    }
  }
</script>

<section class="card" data-testid="engine-actions">
  <h2>Engine actions</h2>
  <div class="row">
    <button type="button" aria-expanded={open === "repaint"} onclick={() => void choose("repaint")} data-testid="action-repaint">Repaint section…</button>
    <button type="button" aria-expanded={open === "reference"} onclick={() => void choose("reference")} data-testid="action-reference">Render with this as reference…</button>
  </div>
  {#if open}
    {#if loading}
      <p class="muted">Loading…</p>
    {:else}
      {#if reason}<p class="muted" role="status" data-testid="action-reason">{reason}</p>{/if}
      {#if !reason}
        <p class="muted">
          {open === "repaint" ? "Select the part of the take to repaint." : "Select the part to follow, or leave it empty for the default (the last 20 s)."}
        </p>
        <RangeSelect
          {duration}
          {peaks}
          {bits}
          {from}
          {to}
          onchange={set}
          min={limits.min}
          max={limits.max}
          limitLabel={open === "repaint" ? "Repaint" : "Reference"}
          onplay={playWindow}
          label={open === "repaint" ? "Repaint range" : "Reference window"}
        />
        <div class="row">
          <button type="button" onclick={proceed} disabled={problem !== ""} data-testid="action-continue">Continue to estimate</button>
          {#if open === "reference" && from !== null}<button type="button" onclick={() => ((from = null), (to = null))}>Use the default window</button>{/if}
        </div>
      {/if}
    {/if}
  {/if}
  {#if abRange && parentId}
    <div class="ab">
      <button type="button" onclick={() => void ab()} disabled={abBusy} data-testid="ab-repaint">A/B with parent at the repaint range</button>
      <p class="muted">
        Window {abRange.from_s}–{abRange.to_s} s{abRange.truncated ? " (the range is longer than a duel window, so only its first 45 s is compared)" : ""}.
      </p>
    </div>
  {/if}
  {#if error}<p role="alert">{error}</p>{/if}
</section>

<style>
  .card {
    border: 1px solid #3a322b;
    border-radius: 0.5rem;
    padding: 0.5rem 0.9rem;
    min-width: 0;
  }
  h2 {
    margin: 0 0 0.4rem;
    font-size: 1rem;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    margin: 0.3rem 0;
  }
  button {
    min-height: 44px;
    max-width: 100%;
  }
  .ab {
    margin-top: 0.4rem;
  }
</style>
