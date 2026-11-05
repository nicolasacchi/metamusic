<script lang="ts">
  // Judge > Callbacks (C262): /judge/callbacks lists them (pending first), /judge/callbacks/:id answers one.
  import { api } from "../api/client";
  import { listCallbacks, type Callback } from "../lib/callbacksApi";
  import { isUnavailable, spotText, UNAVAILABLE_TEXT, waitingText } from "../lib/callbackView";
  import CallbackCard from "../lib/components/CallbackCard.svelte";

  interface Props {
    /** The callback to show; empty lists them (/judge/callbacks vs /judge/callbacks/:id). */
    id?: string;
    navigate: (to: string) => void;
  }
  let { id = "", navigate }: Props = $props();

  let items = $state<Callback[]>([]);
  let total = $state(0);
  let cursor = $state<string | null>(null);
  let loading = $state(true);
  let unavailable = $state(false);
  let error = $state("");

  async function load(more: boolean) {
    if (!more) loading = true;
    error = "";
    try {
      const r = await listCallbacks(api, { limit: 25, ...(more && cursor ? { cursor } : {}) });
      if (r.ok) {
        items = more ? [...items, ...r.value.items] : r.value.items;
        total = r.value.total;
        cursor = r.value.next_cursor;
      } else if (isUnavailable(r)) unavailable = true;
      else error = r.message;
    } catch {
      error = "Could not load the callbacks";
    }
    loading = false;
  }
  $effect(() => {
    if (id === "") void load(false);
  });
  const pending = $derived(items.filter((c) => c.state === "pending").length);
</script>

<h1>Callbacks</h1>
{#if id}
  <CallbackCard {id} {navigate} />
{:else if loading}
  <p class="muted">Loading…</p>
{:else if unavailable}
  <p class="muted" data-testid="callbacks-unavailable">{UNAVAILABLE_TEXT}</p>
{:else if error}
  <p role="alert">{error} <button type="button" onclick={() => void load(false)}>Retry</button></p>
{:else if items.length === 0}
  <p class="muted" data-testid="callbacks-empty">No callbacks yet. They appear when a patched take has been rendered.</p>
{:else}
  <p class="muted" data-testid="callbacks-count">{waitingText(pending)}{pending === items.length ? "" : ` · ${total} in all`}</p>
  <ul class="list" data-testid="callbacks-list">
    {#each items as c (c.id)}
      <li>
        <a class="item" href={`/judge/callbacks/${encodeURIComponent(c.id)}`} data-state={c.state}>
          <span class="t">{c.child.title}</span>
          <span class="muted">{c.kind === "fix" ? "fix" : "keep"} · {spotText(c)} · {c.state === "pending" ? "waiting" : "answered"}</span>
        </a>
      </li>
    {/each}
  </ul>
  {#if cursor}<button type="button" class="more" onclick={() => void load(true)}>Load more</button>{/if}
{/if}

<style>
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--sp-2);
  }
  .item {
    display: grid;
    gap: 2px;
    min-height: var(--hit);
    padding: var(--sp-2) var(--sp-3);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-md);
    color: var(--text);
    text-decoration: none;
    overflow-wrap: anywhere;
  }
  .item[data-state="pending"] {
    border-color: var(--accent);
  }
  .more {
    min-height: var(--hit);
    margin-top: var(--sp-2);
  }
</style>
