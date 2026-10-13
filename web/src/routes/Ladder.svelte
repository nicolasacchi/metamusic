<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import LadderTable from "../lib/components/LadderTable.svelte";

  interface Props {
    root?: string;
  }
  let { root = "" }: Props = $props();
  let page = $state<components["schemas"]["LadderPage"] | null>(null);
  let error = $state("");
  let sighted = $state(false);

  $effect(() => {
    const r = root;
    const inc = sighted;
    page = null;
    error = "";
    void (async () => {
      try {
        const res = await api.GET("/api/ladder", {
          params: { query: { ...(r ? { scope: "lineage" as const, root_track_id: r } : {}), include_sighted: inc, limit: 100 } },
        });
        if (r !== root || inc !== sighted) return;
        if (res.data) page = res.data;
        else error = res.error?.message ?? "Failed to load the ladder";
      } catch {
        error = "Failed to load the ladder";
      }
    })();
  });
</script>

<h1>Ladder{root ? " (lineage)" : ""}</h1>
<p class="muted">Ranking from your blind verdicts; the red line is the reject anchor (u = 0), not an acceptance probability.</p>
<label><input type="checkbox" bind:checked={sighted} /> Include sighted verdicts</label>
{#if error}
  <p role="alert">{error}</p>
{:else if !page}
  <p class="muted">Loading…</p>
{:else if page.items.length === 0}
  <p class="muted" data-testid="ladder-empty">No judged duels yet.</p>
{:else}
  <LadderTable {page} />
{/if}
