<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { remintClips, submitVerdict } from "../lib/duelApi";
  import { duelPage, type DuelRow } from "../lib/duelView";
  import DuelCard from "../lib/components/DuelCard.svelte";
  import NewDuelForm from "../lib/components/NewDuelForm.svelte";

  interface Props {
    navigate: (to: string) => void;
  }
  let { navigate }: Props = $props();

  type Duel = components["schemas"]["Duel"];
  let duel = $state<Duel | null>(null);
  let pending = $state(0);
  let loading = $state(true);
  let error = $state("");
  let skipped: string[] = [];

  let history = $state<DuelRow[]>([]);
  let total = $state(0);
  let cursor = $state<string | null>(null);

  async function loadNext() {
    loading = true;
    error = "";
    try {
      const r = await api.GET("/api/duels/next", { params: { query: skipped.length > 0 ? { exclude: skipped } : {} } });
      if (r.data) {
        duel = r.data.duel;
        pending = r.data.open_total;
      } else error = r.error?.message ?? "Failed to load the next duel";
    } catch {
      error = "Failed to load the next duel";
    } finally {
      loading = false;
    }
  }
  async function loadHistory(more: boolean) {
    try {
      const r = await api.GET("/api/duels", { params: { query: { limit: 20, ...(more && cursor ? { cursor } : {}) } } });
      if (!r.data) return;
      const p = duelPage(r.data);
      history = more ? [...history, ...p.rows] : p.rows;
      total = p.total;
      cursor = p.nextCursor;
    } catch {
      /* history is optional */
    }
  }
  $effect(() => {
    void loadNext();
    void loadHistory(false);
  });

  function skip() {
    if (duel) skipped = [...skipped, duel.id];
    void loadNext();
  }
</script>

<h1>Duels</h1>
<NewDuelForm {navigate} />

<section aria-label="Next duel">
  {#if loading}
    <p class="muted">Loading…</p>
  {:else if error}
    <p role="alert">{error}</p>
  {:else if duel}
    <p class="muted">{pending} waiting</p>
    {#key duel.id}
      <DuelCard
        {duel}
        onsubmit={(s) => submitVerdict(api, duel!.id, s)}
        onskip={skip}
        onnext={() => {
          void loadNext();
          void loadHistory(false);
        }}
        remint={() => remintClips(api, duel!.id)}
      />
    {/key}
  {:else}
    <p class="muted" data-testid="duels-empty">No open duels. Create one above, or run a blind round from a track page.</p>
  {/if}
</section>

<section aria-label="History">
  <h2>History ({total})</h2>
  <ul>
    {#each history as r (r.id)}
      <li><a href={r.href}>{r.title}</a> <span class="muted">{r.created} · {r.outcome}{r.blind ? " · blind" : ""}</span></li>
    {/each}
  </ul>
  {#if cursor}<button type="button" onclick={() => void loadHistory(true)}>Load more</button>{/if}
</section>
