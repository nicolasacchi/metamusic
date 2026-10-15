<script lang="ts">
  import { api } from "../../api/client";
  import { offline } from "../offline.svelte";
  import { addToOfflineSet, removeFromOfflineSet } from "../offlineButton";

  interface Props {
    trackId: string;
  }
  let { trackId }: Props = $props();

  let busy = $state(false);
  let error = $state("");

  // get_offline_set for the button state (once per page visit; the store keeps the last value).
  $effect(() => {
    if (!offline.available) return;
    void offline.refresh();
    void offline.refreshSet();
  });

  const status = $derived(offline.status(trackId));
  const inSet = $derived(offline.set?.items.some((i) => i.track_id === trackId) ?? false);
  const progress = $derived(offline.progress[trackId]);
  const pct = $derived(progress && progress.total > 0 ? Math.round((progress.done / progress.total) * 100) : null);

  const actions = {
    download: (id: string) => offline.download(id),
    remove: (id: string) => offline.remove(id),
  };

  async function run(add: boolean) {
    if (busy) return;
    busy = true;
    error = "";
    try {
      const r = await (add ? addToOfflineSet : removeFromOfflineSet)(api, trackId, actions);
      if (!r.ok) error = r.message;
    } catch (e) {
      error = e instanceof Error && e.message ? e.message : "Offline download failed";
    } finally {
      busy = false;
      await offline.refreshSet();
    }
  }
</script>

{#if offline.available}
  <div class="off">
    {#if status === "complete"}
      <button type="button" data-testid="offline-remove" disabled={busy} onclick={() => run(false)}>Remove from offline</button>
      <span class="muted" data-testid="offline-state">Downloaded</span>
    {:else if busy || status === "partial"}
      <button type="button" data-testid="offline-add" disabled>Downloading{pct !== null ? ` ${pct}%` : "…"}</button>
    {:else}
      <button type="button" data-testid="offline-add" onclick={() => run(true)}>Download for offline</button>
      {#if inSet}<span class="muted" data-testid="offline-state">In the offline set, not downloaded</span>{/if}
    {/if}
    {#if error}<span role="alert">{error}</span>{/if}
  </div>
{/if}

<style>
  .off {
    display: flex;
    gap: 0.6rem;
    align-items: center;
    flex-wrap: wrap;
  }
</style>
