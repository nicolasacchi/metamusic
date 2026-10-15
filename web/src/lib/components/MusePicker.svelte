<script lang="ts">
  import { api } from "../../api/client";
  import type { components } from "../../api/schema";
  import { createMuse, searchMuses, type Muse } from "../museApi";

  type Summary = components["schemas"]["MuseSummary"];
  interface Props {
    /** Called with the chosen (or created) Muse. */
    onpick: (muse: { id: string; title: string }) => void;
    oncancel?: () => void;
    /** Test seam: skip the network typeahead. */
    initialResults?: Summary[];
  }
  let { onpick, oncancel, initialResults = [] }: Props = $props();

  // svelte-ignore state_referenced_locally
  let results = $state<Summary[]>(initialResults);
  let q = $state("");
  let creating = $state(false);
  let kind = $state<"reference" | "brief">("reference");
  let title = $state("");
  let artist = $state("");
  let brief = $state("");
  let message = $state("");
  let busy = $state(false);
  let seq = 0;
  // One idempotent action per click of "Create": a retry after a network error reuses the key.
  let pending: (() => ReturnType<ReturnType<typeof createMuse>>) | null = null;

  $effect(() => {
    const text = q.trim();
    const mine = ++seq;
    void searchMuses(api, text).then((r) => {
      if (mine === seq) results = r;
    });
  });

  async function submit(e: Event) {
    e.preventDefault();
    if (busy || !title.trim()) return;
    busy = true;
    message = "";
    pending ??= createMuse(api, {
      kind,
      title: title.trim(),
      ...(kind === "reference" && artist.trim() ? { artist: artist.trim() } : {}),
      ...(kind === "brief" && brief.trim() ? { brief: brief.trim() } : {}),
    });
    const r = await pending();
    busy = false;
    if (r.kind === "created" || r.kind === "existing") {
      pending = null;
      if (r.kind === "existing") message = "That Muse already exists: using it.";
      onpick({ id: r.muse.id, title: (r.muse as Muse).title });
    } else if (r.kind === "network") {
      message = "Network error: press Create again to retry.";
    } else {
      pending = null;
      message = r.message || "Could not create the Muse.";
    }
  }
</script>

<div class="picker" data-testid="muse-picker">
  {#if !creating}
    <input type="search" bind:value={q} placeholder="Find a Muse" aria-label="Find a Muse" data-testid="muse-search" />
    <ul>
      {#each results as m (m.id)}
        <li><button type="button" onclick={() => onpick({ id: m.id, title: m.title })}>{m.title}{#if m.artist} · {m.artist}{/if} <span class="muted">({m.draft + m.rendered + m.judged + m.picked})</span></button></li>
      {/each}
    </ul>
    <button type="button" onclick={() => (creating = true)} data-testid="muse-create-open">Create Muse…</button>
    {#if oncancel}<button type="button" onclick={oncancel}>Cancel</button>{/if}
  {:else}
    <form onsubmit={submit}>
      <label>Kind
        <select bind:value={kind} onchange={() => (pending = null)}>
          <option value="reference">Reference (title and artist)</option>
          <option value="brief">Brief (title and text)</option>
        </select>
      </label>
      <label>Title <input bind:value={title} oninput={() => (pending = null)} required maxlength="200" data-testid="muse-title" /></label>
      {#if kind === "reference"}
        <label>Artist <input bind:value={artist} oninput={() => (pending = null)} maxlength="200" /></label>
      {:else}
        <label>Brief <textarea bind:value={brief} oninput={() => (pending = null)} rows="3"></textarea></label>
      {/if}
      <button type="submit" disabled={busy || !title.trim()} data-testid="muse-create">Create</button>
      <button type="button" onclick={() => (creating = false)}>Back</button>
    </form>
  {/if}
  {#if message}<p role="status">{message}</p>{/if}
</div>

<style>
  .picker {
    display: grid;
    gap: 0.4rem;
    max-width: 24rem;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  form,
  label {
    display: grid;
    gap: 0.25rem;
  }
</style>
