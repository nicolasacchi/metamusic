<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { remintClips, submitVerdict } from "../lib/duelApi";
  import { duelViewModel } from "../lib/duelView";
  import DuelCard from "../lib/components/DuelCard.svelte";
  import DuelReveal from "../lib/components/DuelReveal.svelte";

  interface Props {
    id: string;
  }
  let { id }: Props = $props();
  let duel = $state<components["schemas"]["Duel"] | null>(null);
  let error = $state("");

  $effect(() => {
    const mine = id;
    duel = null;
    error = "";
    void (async () => {
      try {
        const r = await api.GET("/api/duels/{id}", { params: { path: { id: mine } } });
        if (mine !== id) return;
        if (r.data) duel = r.data;
        else error = r.response.status === 404 ? "Duel not found" : (r.error?.message ?? "Failed to load the duel");
      } catch {
        if (mine === id) error = "Failed to load the duel";
      }
    })();
  });
  const view = $derived(duel ? duelViewModel(duel) : null);
</script>

<h1>Duel</h1>
{#if error}
  <p role="alert">{error}</p>
{:else if !duel || !view}
  <p class="muted">Loading…</p>
{:else if view.kind === "expired"}
  <p role="status" data-testid="duel-expired">{view.notice}</p>
{:else if view.kind === "reveal"}
  <DuelReveal {duel} />
{:else}
  <DuelCard {duel} onsubmit={(s) => submitVerdict(api, duel!.id, s)} remint={() => remintClips(api, id)} />
{/if}
<p><a class="tap" href="/duels">All duels</a></p>
