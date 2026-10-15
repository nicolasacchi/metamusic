<script lang="ts">
  import { onMount } from "svelte";
  import { offline } from "../lib/offline.svelte";
  import { player } from "../lib/player.svelte";
  import { formatBytes, mergeRows, syncMessage } from "../lib/offlineView";
  import { fmt } from "../lib/time";

  let online = $state(typeof navigator === "undefined" || navigator.onLine !== false);
  let message = $state("");
  let busyId = $state("");

  // Offline (navigator.onLine false, or the set could not be fetched): the downloaded items from IndexedDB only.
  const useSet = $derived(online && offline.error !== "offline");
  const rows = $derived(mergeRows(useSet ? offline.set : null, offline.items, (id) => offline.status(id)));
  const playable = $derived(rows.filter((r) => r.status === "complete").map((r) => r.track_id));

  onMount(() => {
    const up = () => {
      online = true;
      void offline.refreshSet();
    };
    const down = () => (online = false);
    window.addEventListener("online", up);
    window.addEventListener("offline", down);
    void offline.refresh();
    if (online) void offline.refreshSet();
    return () => {
      window.removeEventListener("online", up);
      window.removeEventListener("offline", down);
    };
  });

  async function sync() {
    message = "";
    try {
      message = syncMessage(await offline.sync());
    } catch {
      message = "Could not sync the offline set.";
    }
  }
  async function get(id: string) {
    busyId = id;
    message = "";
    try {
      await offline.download(id);
    } catch (e) {
      message = e instanceof Error ? e.message : "Download failed";
    } finally {
      busyId = "";
    }
  }
  async function drop(id: string) {
    busyId = id;
    try {
      await offline.remove(id);
    } finally {
      busyId = "";
    }
  }
  function play(id: string) {
    const r = rows.find((x) => x.track_id === id);
    player.play(id, playable, { title: r?.title ?? "", album: r?.album ?? "" });
  }
  function pct(id: string): number {
    const p = offline.progress[id];
    return p && p.total > 0 ? Math.round((100 * p.done) / p.total) : 0;
  }
</script>

<h1>Offline</h1>

{#if !offline.available}
  <p data-testid="offline-unavailable">Offline listening needs HTTPS.</p>
{:else}
  <section aria-label="Storage">
    {#if offline.quota}
      <p data-testid="offline-quota">
        Storage: {formatBytes(offline.quota.usage)} used of {formatBytes(offline.quota.quota)}.
      </p>
    {/if}
    <p data-testid="offline-persist">
      {#if offline.persisted === true}Storage is protected from eviction.
      {:else if offline.persisted === false}The browser may evict downloads when space runs low.
      {:else}Storage protection is requested after the first download.{/if}
    </p>
  </section>

  {#if useSet}
    <p>
      <button type="button" onclick={sync} disabled={offline.syncing} data-testid="offline-sync">
        {offline.syncing ? "Syncing…" : "Sync offline set"}
      </button>
      {#if offline.set}<span class="muted">{offline.set.items.length} of {offline.set.limit_items} tracks in the set.</span>{/if}
    </p>
  {:else}
    <p class="muted" data-testid="offline-banner">You are offline. Showing downloaded tracks.</p>
  {/if}
  {#if message}<p role="status" data-testid="offline-message">{message}</p>{/if}

  {#if rows.length === 0}
    <p class="muted" data-testid="offline-empty">Nothing downloaded yet. Add tracks to the offline set, then sync.</p>
  {:else}
    <ul class="rows" data-testid="offline-list">
      {#each rows as r (r.track_id)}
        <li data-testid="offline-row" data-status={r.status}>
          <span class="title">{r.title}</span>
          <span class="muted">{r.album} · {fmt(r.duration_s)}</span>
          <span class="state">
            {#if offline.progress[r.track_id]}
              <progress max="100" value={pct(r.track_id)} aria-label="Download progress"></progress>
            {:else if r.status === "complete"}Downloaded
            {:else if r.status === "partial"}Partial
            {:else}Not downloaded{/if}
          </span>
          {#if r.status === "complete"}
            <button type="button" onclick={() => play(r.track_id)}>Play</button>
          {/if}
          {#if useSet && r.status !== "complete"}
            <button type="button" onclick={() => get(r.track_id)} disabled={busyId === r.track_id}>Download</button>
          {/if}
          {#if r.status !== "none"}
            <button type="button" onclick={() => drop(r.track_id)} disabled={busyId === r.track_id}>Remove</button>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
{/if}

<style>
  .rows {
    list-style: none;
    padding: 0;
    display: grid;
    gap: 0.4rem;
  }
  li {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem 1rem;
    align-items: center;
    padding: 0.4rem 0;
    border-bottom: 1px solid #3a322b;
  }
  .title {
    font-weight: 600;
  }
</style>
