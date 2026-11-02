<script lang="ts">
  import { api } from "../../api/client";
  import { createRecord } from "../crateApi";
  import { recordHandOff, sequenceHref } from "../atlasApi";
  import type { Viewer } from "../genIntent";
  import AddToCrate from "./AddToCrate.svelte";
  import AddToRecord from "./AddToRecord.svelte";

  interface Props {
    /** Selected Song ids, in selection order. */
    ids: readonly string[];
    boardHref: string;
    viewer?: Viewer | null;
    /** Phone: a bottom sheet above the MiniPlayer and the tab bar. */
    sheet?: boolean;
    navigate: (to: string, replace?: boolean) => void;
    onclear: () => void;
  }
  let { ids, boardHref, viewer = null, sheet = false, navigate, onclear }: Props = $props();

  let panel = $state<"" | "crate" | "record">("");
  let title = $state("Atlas selection");
  let busy = $state(false);
  let error = $state("");
  const hand = $derived(recordHandOff(ids));
  const one = $derived(ids.length === 1 ? ids[0]! : "");

  async function newRecord() {
    const t = title.trim();
    if (!t || busy || hand.songIds.length === 0) return;
    busy = true;
    error = "";
    const r = await createRecord(api, t, hand.songIds)();
    busy = false;
    if (r.ok) navigate(sequenceHref(r.value.record.id));
    else error = r.message;
  }
</script>

{#if ids.length > 0}
  <section class="bar" class:sheet aria-label="Selection" data-testid="atlas-select-bar">
    <div class="row">
      <span data-testid="atlas-selection-count">{ids.length} selected</span>
      <button type="button" aria-expanded={panel === "crate"} onclick={() => (panel = panel === "crate" ? "" : "crate")} data-testid="atlas-crate-open">Add to Crate…</button>
      <button type="button" aria-expanded={panel === "record"} onclick={() => (panel = panel === "record" ? "" : "record")} data-testid="atlas-record-open">Add to Record…</button>
      {#if one}<a href="/songs/{encodeURIComponent(one)}" data-testid="atlas-open-song">Open</a>{/if}
      <a href={boardHref} data-testid="atlas-board-link">Show on the Board</a>
      <button type="button" onclick={onclear} data-testid="atlas-clear">Clear</button>
    </div>
    <form
      class="row"
      onsubmit={(e) => {
        e.preventDefault();
        void newRecord();
      }}
    >
      <input bind:value={title} aria-label="New Record title" maxlength="200" data-testid="atlas-record-title" />
      <button type="submit" disabled={busy || !title.trim()} data-testid="atlas-new-record">New Record from selection…</button>
    </form>
    {#if hand.note}<p class="muted" data-testid="atlas-record-cap">{hand.note}</p>{/if}
    {#if error}<p role="alert">{error}</p>{/if}
    {#if panel === "crate"}
      <div class="panel" data-testid="atlas-crate-panel"><AddToCrate subject="song" ids={[...ids]} heading="Add {ids.length} selected to a Crate" /></div>
    {/if}
    {#if panel === "record"}
      <div class="panel" data-testid="atlas-record-panel"><AddToRecord songIds={hand.songIds} {viewer} heading="Add {hand.songIds.length} selected to a Record" /></div>
    {/if}
  </section>
{/if}

<style>
  .bar {
    display: grid;
    gap: var(--sp-2);
    margin: var(--sp-2) 0;
    padding: var(--sp-3);
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    background: var(--surface);
  }
  .bar.sheet {
    position: fixed;
    left: 0;
    right: 0;
    bottom: calc(var(--footer-h, 0px) + var(--nav-b, 0px));
    z-index: 4;
    margin: 0;
    max-height: 60vh;
    overflow-y: auto;
    border-radius: var(--r-md) var(--r-md) 0 0;
    background: var(--bg-raised);
    border-top: 1px solid var(--line-strong);
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    align-items: center;
  }
  .bar.sheet .row {
    flex-wrap: nowrap;
    overflow-x: auto;
  }
  .bar.sheet .row > * {
    flex: none;
    white-space: nowrap;
  }
  button,
  input,
  a {
    min-height: var(--hit);
  }
  a {
    display: inline-flex;
    align-items: center;
  }
  .panel {
    padding: var(--sp-3);
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    background: var(--surface);
  }
</style>
