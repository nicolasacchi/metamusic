<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { compareView } from "../lib/compareView";

  interface Props {
    a?: string;
    b?: string;
  }
  let { a = "", b = "" }: Props = $props();
  let cmp = $state<components["schemas"]["Comparison"] | null>(null);
  let error = $state("");

  $effect(() => {
    const [x, y] = [a, b];
    cmp = null;
    error = "";
    if (!x || !y) return;
    void (async () => {
      try {
        const r = await api.GET("/api/compare", { params: { query: { a: x, b: y } } });
        if (x !== a || y !== b) return;
        if (r.data) cmp = r.data;
        else error = r.error?.message ?? "Failed to compare";
      } catch {
        error = "Failed to compare";
      }
    })();
  });
  const v = $derived(cmp ? compareView(cmp) : null);
</script>

<h1>Compare</h1>
{#if !a || !b}
  <p class="muted">Open a comparison from a track page ("Compare with parent") or a duel result.</p>
{:else if error}
  <p role="alert">{error}</p>
{:else if !cmp || !v}
  <p class="muted">Loading…</p>
{:else}
  <p>
    <a href="/tracks/{cmp.a.track_id}">{cmp.a.title}</a> (A, seed {cmp.a.seed}) vs <a href="/tracks/{cmp.b.track_id}">{cmp.b.title}</a> (B, seed {cmp.b.seed})
    — <span data-testid="relation">{v.relation}</span>
  </p>
  <table data-testid="deltas">
    <thead><tr><th>Metric</th><th>B − A</th></tr></thead>
    <tbody>
      {#each v.deltas as d (d.key)}
        <tr><td>{d.label}</td><td class="sign-{d.sign} tone-{d.tone}">{d.text}</td></tr>
      {/each}
    </tbody>
  </table>
  <h2>Flags</h2>
  <ul>
    <li>Only A: {v.flags.onlyA.join(", ") || "none"}</li>
    <li>Only B: {v.flags.onlyB.join(", ") || "none"}</li>
    <li>Both: {v.flags.both.join(", ") || "none"}</li>
  </ul>
  <h2>Recipe</h2>
  <p>{v.recipe.sameHash ? "Same recipe hash." : `Differs in: ${v.recipe.changed.join(", ") || "nothing listed"}.`} Seed {v.recipe.seed}.</p>
  {#if v.recipe.added.length > 0 || v.recipe.removed.length > 0}
    <p>Style terms: +{v.recipe.added.join(", +") || "none"} / −{v.recipe.removed.join(", −") || "none"}</p>
  {/if}
  <h2>Head to head</h2>
  <p>Human {v.headToHead.human}; {v.headToHead.blindN} blind. Critic {v.headToHead.critic}.</p>
  <h2>Ladder</h2>
  <p>{v.ladderLine}</p>
  <h2>What this cannot hear</h2>
  <ul class="blind">{#each v.blindSpots as s (s)}<li>{s}</li>{/each}</ul>
{/if}

<style>
  table {
    border-collapse: collapse;
  }
  th,
  td {
    text-align: left;
    padding: 0.2rem 0.8rem;
  }
  td {
    font-variant-numeric: tabular-nums;
  }
  .sign-pos {
    color: #7bc47f;
  }
  .sign-neg {
    color: #e5a04d;
  }
  .sign-zero,
  .sign-none {
    opacity: 0.6;
  }
  .tone-good {
    color: #7bc47f;
    font-weight: 700;
  }
  .tone-bad {
    color: #e5484d;
    font-weight: 700;
  }
  .blind {
    font-size: 0.85rem;
    opacity: 0.8;
  }
</style>
