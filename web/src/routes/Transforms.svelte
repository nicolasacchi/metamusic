<script lang="ts">
  // /transforms (More): the queue and history of CPU transforms. State chips, progress bars, Cancel on live rows,
  // today's CPU used / reserved / cap meter (a shared pool per principal kind) and a link to each Take.
  // 501/503 (B4 not landed, or transforms switched off) show a "not available yet" state, never an error banner.
  import { onDestroy, untrack } from "svelte";
  import { api as defaultApi } from "../api/client";
  import TransformCard from "../lib/components/TransformCard.svelte";
  import { cancelTransform, getCapabilities, getTransform, listTransforms, type Capabilities, type ListQuery } from "../lib/transformApi";
  import { isUnavailable, pollDelayMs, quotaMeter, refusalText, sortTransforms, kindLabel, type Refusal, type TransformView } from "../lib/transformView";
  import { KIND_ORDER } from "../lib/transformView";

  interface Props {
    client?: typeof defaultApi;
    /** Pre-loaded data (server-side rendering in unit tests). */
    initial?: { caps?: Capabilities | null; items?: TransformView[]; refusal?: Refusal | null };
  }
  let { client = defaultApi, initial }: Props = $props();

  // svelte-ignore state_referenced_locally
  let caps = $state<Capabilities | null>(initial?.caps ?? null);
  // svelte-ignore state_referenced_locally
  let items = $state<TransformView[]>(initial?.items ?? []);
  // svelte-ignore state_referenced_locally
  let refusal = $state<Refusal | null>(initial?.refusal ?? null);
  // svelte-ignore state_referenced_locally
  let loaded = $state(initial !== undefined);
  let next = $state<string | null>(null);
  let stateFilter = $state<"" | NonNullable<ListQuery["state"]>>("");
  let kindFilter = $state<"" | NonNullable<ListQuery["kind"]>>("");
  let busy = $state(false);
  let error = $state("");
  let tick = $state(0);
  let seq = 0;

  const unavailable = $derived(refusal !== null && isUnavailable(refusal));
  const sorted = $derived(sortTransforms(items));
  const meter = $derived(caps ? quotaMeter(caps.quota) : null);
  const kinds = $derived(caps ? caps.kinds.map((k) => k.kind) : [...KIND_ORDER]);

  function query(cursor?: string): ListQuery {
    return { limit: 50, ...(stateFilter ? { state: stateFilter } : {}), ...(kindFilter ? { kind: kindFilter } : {}), ...(cursor ? { cursor } : {}) };
  }
  async function load(more = false) {
    const mine = ++seq;
    const [l, c] = await Promise.all([listTransforms(client, query(more && next ? next : undefined)), getCapabilities(client)]);
    if (mine !== seq) return;
    loaded = true;
    if (c.ok) caps = c.value;
    if (!l.ok) {
      if (isUnavailable(l)) refusal = l;
      else error = refusalText(l);
      return;
    }
    refusal = null;
    error = "";
    items = more ? [...items, ...l.value.items.filter((t) => !items.some((x) => x.id === t.id))] : l.value.items;
    next = l.value.next_cursor;
  }
  $effect(() => {
    void stateFilter;
    void kindFilter;
    if (initial === undefined || tick > 0) untrack(() => void load());
  });
  // Polling: every 3 s while a row is live; re-armed after every attempt.
  $effect(() => {
    void tick;
    const delay = unavailable ? null : pollDelayMs(items);
    if (delay === null) return;
    const t = setTimeout(() => void load().finally(() => tick++), delay);
    return () => clearTimeout(t);
  });
  onDestroy(() => seq++);

  async function cancel(id: string) {
    if (busy) return;
    busy = true;
    const r = await cancelTransform(client, id);
    if (!r.ok) error = refusalText(r);
    else {
      // show the row's own state at once (cancelling…, or already cancelled) instead of waiting for the next list poll
      const g = await getTransform(client, id);
      const row = g.ok ? g.value : r.value;
      items = items.map((t) => (t.id === id ? row : t));
    }
    busy = false;
    await load();
  }
</script>

<h1>Transforms</h1>
{#if !loaded}
  <p class="muted" data-testid="transforms-loading">Loading…</p>
{:else if unavailable}
  <p class="muted" data-testid="transforms-unavailable">Transforms are not available on this server yet.</p>
{:else}
  {#if meter && caps}
    <section class="pool" aria-label="CPU allowance" data-testid="transforms-quota">
      <p class="muted">
        Today's CPU: {meter.label} · shared by every {caps.quota.principal_kind} · queue {caps.queue.running} running, {caps.queue.queued} waiting (limit {caps.queue.max})
      </p>
      <div class="meter" role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={meter.usedPct + meter.reservedPct} aria-label="CPU allowance used">
        <i class="used" style:width="{meter.usedPct}%"></i><i class="res" style:width="{meter.reservedPct}%"></i>
      </div>
    </section>
  {/if}
  {#if error}<p class="err" role="alert">{error}</p>{/if}
  <div class="filters">
    <label>State
      <select bind:value={stateFilter} data-testid="transforms-state-filter">
        <option value="">All</option>
        <option value="queued">Queued</option>
        <option value="running">Running</option>
        <option value="done">Done</option>
        <option value="failed">Failed</option>
        <option value="cancelled">Cancelled</option>
      </select>
    </label>
    <label>Kind
      <select bind:value={kindFilter} data-testid="transforms-kind-filter">
        <option value="">All</option>
        {#each kinds as k (k)}<option value={k}>{kindLabel(k)}</option>{/each}
      </select>
    </label>
  </div>
  {#if sorted.length === 0}
    <p class="muted" data-testid="transforms-empty">No transforms yet. Open a take and use its Transform panel.</p>
  {:else}
    <div class="list" data-testid="transforms-list">
      {#each sorted as t (t.id)}<TransformCard transform={t} {busy} oncancel={(id) => void cancel(id)} />{/each}
    </div>
  {/if}
  {#if next}<p><button type="button" onclick={() => void load(true)} data-testid="transforms-more">Load more</button></p>{/if}
{/if}

<style>
  .pool {
    margin-bottom: var(--sp-3);
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .meter {
    display: flex;
    height: 0.6rem;
    border-radius: var(--r-pill);
    background: var(--bg-sunken);
    overflow: hidden;
  }
  .used {
    background: var(--accent);
  }
  .res {
    background: var(--line-strong);
  }
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-3);
    margin: var(--sp-3) 0;
  }
  .filters label {
    display: flex;
    flex-direction: column;
    gap: var(--sp-1);
    font-size: var(--fs-sm);
  }
  .filters select {
    min-height: var(--hit);
  }
  .list {
    display: grid;
    gap: var(--sp-3);
    min-width: 0;
  }
  .err {
    color: var(--bad);
  }
</style>
