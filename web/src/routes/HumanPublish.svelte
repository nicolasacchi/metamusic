<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { freezeAll, getRecord, publishRecord } from "../lib/curationApi";
  import { pushToast } from "../lib/judge.svelte";
  import { warningText } from "../lib/recordView";

  type View = components["schemas"]["RecordView"];
  interface Props {
    id: string;
    navigate?: (to: string) => void;
    /** Pre-loaded view with the preview (unit tests; the page otherwise fetches it). */
    initial?: View;
  }
  let { id, navigate, initial }: Props = $props();

  // svelte-ignore state_referenced_locally
  let view = $state<View | null>(initial ?? null);
  let error = $state("");
  let busy = $state(false);
  let reviewed = $state(false);
  let stale = $state(false);

  async function load() {
    const r = await getRecord(api, id, ["preview"]);
    if (r.kind === "ok") {
      view = r.value;
      stale = false;
      reviewed = false;
      error = "";
    } else error = r.kind === "failed" && r.status === 404 ? "Record not found" : "Failed to load the record";
  }
  $effect(() => {
    if (initial) return;
    void load();
  });

  const rec = $derived(view?.record ?? null);
  const preview = $derived(view?.preview ?? null);
  const warnings = $derived(preview?.warnings ?? []);
  const position = (slotId: string) => view?.slots.find((s) => s.id === slotId)?.position;
  const published = $derived(rec?.status === "published");
  const hasUndecided = $derived(warnings.some((w) => w.code === "undecided_pick"));
  const mayAct = $derived(!!rec && (published || (view?.totals.playable ?? 0) > 0) && (warnings.length === 0 || reviewed || published));

  function go(to: string) {
    if (navigate) navigate(to);
    else {
      history.pushState(null, "", to);
      window.dispatchEvent(new PopStateEvent("popstate"));
    }
  }

  async function freeze() {
    if (!rec || busy) return;
    busy = true;
    const r = await freezeAll(api, id, rec.revision);
    busy = false;
    if (r.kind === "ok") await load();
    else if (r.kind === "stale") stale = true;
    else pushToast("Could not freeze the picks");
  }

  async function act() {
    if (!rec || busy || !mayAct) return;
    busy = true;
    const r = await publishRecord(api, id, rec.revision, !published);
    busy = false;
    if (r.kind === "ok") go(`/records/${id}`);
    else if (r.kind === "stale") stale = true;
    else if (r.kind === "failed") pushToast(r.message || r.code);
    else pushToast("Could not publish");
  }
</script>

<section data-testid="human-record-publish-page" data-id={id}>
  <h1>{published ? "Unpublish record" : "Publish record"}</h1>
  {#if error}
    <p role="alert">{error}</p>
  {:else if !view || !rec}
    <p class="muted">Loading…</p>
  {:else}
    {#if stale}<p role="alert" data-testid="record-stale">Changed elsewhere. <button type="button" onclick={load}>Reload</button></p>{/if}
    <h2 data-testid="publish-title">{rec.title}</h2>
    <p class="muted">
      <a href="/records/{id}">Back to the record</a> · {view.totals.playable}/{view.totals.slots} playable tracks
    </p>

    <h3>Chapters</h3>
    <ol data-testid="publish-chapters">
      {#each preview?.chapters ?? [] as c (c.position)}<li>{c.name}</li>{/each}
    </ol>

    <h3>Liner notes in the export</h3>
    <pre data-testid="publish-liner">{preview?.description ?? ""}</pre>

    <h3>Tags</h3>
    <dl class="tags" data-testid="publish-tags">
      {#each preview?.tags ?? [] as t, i (i)}<dt>{t.key}</dt><dd>{t.value}</dd>{/each}
    </dl>

    <h3>Warnings</h3>
    {#if warnings.length === 0}
      <p class="muted" data-testid="publish-no-warnings">None.</p>
    {:else}
      <ul data-testid="publish-warnings">
        {#each warnings as w, i (i)}<li>Slot {position(w.slot_id) ?? "?"}: {warningText(w.code)}</li>{/each}
      </ul>
      {#if hasUndecided && !published}
        <button type="button" disabled={busy} onclick={freeze} data-testid="publish-freeze">Freeze all picks</button>
        <p class="muted">Freezing pins every slot to its current take, so the published record stops following later Picks.</p>
      {/if}
      {#if !published}
        <label class="check"><input type="checkbox" bind:checked={reviewed} data-testid="publish-reviewed" /> I have reviewed these {warnings.length} {warnings.length === 1 ? "warning" : "warnings"}</label>
      {/if}
    {/if}

    <div class="actions">
      <button type="button" class="primary" disabled={busy || !mayAct} onclick={act} data-testid="publish-go">{published ? "Unpublish" : "Publish"}</button>
      <a class="tap" href="/records/{id}">Cancel</a>
    </div>
  {/if}
</section>

<style>
  pre {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font-family: inherit;
    background: var(--bg-sunken);
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    padding: var(--sp-3);
  }
  .tags {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: var(--sp-1) var(--sp-3);
    margin: 0;
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .check {
    display: flex;
    align-items: center;
    gap: var(--sp-2);
    min-height: var(--hit);
  }
  .check input {
    width: 24px;
    height: 24px;
  }
  .actions {
    display: flex;
    gap: var(--sp-3);
    align-items: center;
    margin-top: var(--sp-4);
  }
</style>
