<script lang="ts">
  import { api } from "../../api/client";
  import { newIdempotencyKey } from "../../api/idem";
  import type { components } from "../../api/schema";
  import { createDuel } from "../duelApi";

  type Summary = components["schemas"]["TrackSummary"];
  interface Props {
    navigate: (to: string) => void;
  }
  let { navigate }: Props = $props();

  let qa = $state("");
  let qb = $state("");
  let ra = $state<Summary[]>([]);
  let rb = $state<Summary[]>([]);
  let a = $state("");
  let b = $state("");
  let blind = $state(true);
  let match = $state(true);
  let from = $state("");
  let to = $state("");
  let busy = $state(false);
  let error = $state("");
  // One key per submit action; kept while the user retries the same form values.
  let key = "";
  let keyFor = "";

  async function search(q: string): Promise<Summary[]> {
    try {
      const r = await api.GET("/api/tracks", { params: { query: { q, limit: 20 } } });
      return r.data?.items ?? [];
    } catch {
      return [];
    }
  }
  const num = (s: string): number | undefined => {
    const n = Number(s);
    return s.trim() !== "" && Number.isFinite(n) ? n : undefined;
  };

  async function submit() {
    if (!a || !b || a === b || busy) return;
    busy = true;
    error = "";
    const sig = JSON.stringify([a, b, blind, match, from, to]);
    if (sig !== keyFor) {
      key = newIdempotencyKey();
      keyFor = sig;
    }
    try {
      const f = num(from);
      const t = num(to);
      const d = await createDuel(api, a, b, key, { blind, matchLoudness: match, ...(f !== undefined ? { fromS: f } : {}), ...(t !== undefined ? { toS: t } : {}) });
      navigate(`/duels/${encodeURIComponent(d.id)}`);
    } catch (e) {
      error = e instanceof Error ? e.message : "Could not create the duel";
    } finally {
      busy = false;
    }
  }
</script>

<form
  class="new"
  data-testid="new-duel"
  onsubmit={(e) => {
    e.preventDefault();
    void submit();
  }}
>
  <h2>New duel</h2>
  <div class="pick">
    <label>
      Track A
      <input type="search" bind:value={qa} placeholder="Search tracks" oninput={async () => (ra = await search(qa))} />
    </label>
    <select bind:value={a} aria-label="Track A" data-testid="duel-a">
      <option value="">Choose…</option>
      {#each ra as t (t.id)}<option value={t.id}>{t.title} · seed {t.seed}</option>{/each}
    </select>
  </div>
  <div class="pick">
    <label>
      Track B
      <input type="search" bind:value={qb} placeholder="Search tracks" oninput={async () => (rb = await search(qb))} />
    </label>
    <select bind:value={b} aria-label="Track B" data-testid="duel-b">
      <option value="">Choose…</option>
      {#each rb as t (t.id)}<option value={t.id}>{t.title} · seed {t.seed}</option>{/each}
    </select>
  </div>
  <label><input type="checkbox" bind:checked={blind} /> Blind</label>
  <label><input type="checkbox" bind:checked={match} /> Match loudness</label>
  <label>From (s) <input bind:value={from} inputmode="decimal" size="5" placeholder="auto" /></label>
  <label>To (s) <input bind:value={to} inputmode="decimal" size="5" placeholder="auto" /></label>
  <button type="submit" disabled={busy || !a || !b || a === b} data-testid="duel-create">Create duel</button>
  {#if a && a === b}<p class="muted">Pick two different tracks.</p>{/if}
  {#if error}<p role="alert">{error}</p>{/if}
</form>

<style>
  .new {
    display: flex;
    flex-wrap: wrap;
    gap: 0.6rem 1rem;
    align-items: end;
    border: 1px solid #3a322b;
    border-radius: 0.5rem;
    padding: 0.5rem 0.9rem;
  }
  h2 {
    flex-basis: 100%;
    margin: 0;
    font-size: 1rem;
  }
  .pick {
    display: grid;
    gap: 0.2rem;
  }
</style>
