<script lang="ts">
  // The "Morning crate" card on New (M6N-W2, C384.6): shown when today's crate is ready, with the number of takes still
  // unrated. Owner only; says nothing about what is inside (the crate is blind).
  import { untrack } from "svelte";
  import { api } from "../../api/client";
  import { can } from "../me.svelte";
  import { crateName, isToday, unratedCount } from "../morningCrate";

  let card = $state<{ id: string; name: string; unrated: number; revealed: boolean } | null>(null);

  async function load() {
    if (!can("list_morning_crates") || !can("get_morning_crate")) return;
    try {
      const l = await api.GET("/api/morning-crates", { params: { query: { state: "ready", limit: 1 } } });
      const c = l.data?.items[0];
      if (!c || !isToday(c.created_at, new Date())) return void (card = null);
      const g = await api.GET("/api/morning-crates/{id}", { params: { path: { id: c.id } } });
      if (g.data) card = { id: c.id, name: crateName(c), unrated: unratedCount(g.data), revealed: g.data.revealed };
    } catch {
      /* New works without the card */
    }
  }
  $effect(() => {
    untrack(() => void load());
  });
</script>

{#if card}
  <a class="card" href="/morning/{card.id}" data-testid="morning-card">
    <span class="t">Morning crate</span>
    <span class="muted">{card.name} is ready.</span>
    {#if !card.revealed}
      <span class="badge" data-testid="morning-card-badge" aria-label="{card.unrated} unrated">{card.unrated === 0 ? "all rated" : `${card.unrated} to rate`}</span>
    {:else}
      <span class="muted">Revealed.</span>
    {/if}
  </a>
{/if}

<style>
  .card {
    display: flex;
    align-items: center;
    gap: var(--sp-3);
    flex-wrap: wrap;
    margin: var(--sp-3) 0;
    padding: var(--sp-3) var(--sp-4);
    min-height: var(--hit);
    background: var(--surface);
    border: 1px solid var(--accent);
    border-radius: var(--r-md);
    color: inherit;
    text-decoration: none;
  }
  .t {
    font-weight: var(--fw-medium);
  }
  .badge {
    margin-left: auto;
    padding: 0 var(--sp-2);
    border-radius: var(--r-pill);
    background: var(--accent);
    color: var(--bg, #000);
    font-size: var(--fs-xs);
  }
</style>
