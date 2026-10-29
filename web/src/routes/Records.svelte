<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { createRecord, listRecords } from "../lib/curationApi";
  import { createReadyOnce, STATUS_LABEL, type RecordStatus } from "../lib/recordView";
  import { routeReady } from "../lib/scrollRestore";
  import { fmt } from "../lib/time";
  import { pushToast } from "../lib/judge.svelte";
  import RecordCover from "../lib/components/RecordCover.svelte";
  import RecordStatusChip from "../lib/components/RecordStatus.svelte";

  type Summary = components["schemas"]["RecordSummary"];
  interface Props {
    /** Pre-loaded page (unit tests; the page otherwise fetches it). */
    initial?: { items: Summary[]; next_cursor: string | null; total: number };
    navigate?: (to: string) => void;
  }
  let { initial, navigate }: Props = $props();

  const STATUSES: RecordStatus[] = ["draft", "proposed", "published"];
  // svelte-ignore state_referenced_locally
  let items = $state<Summary[]>(initial?.items ?? []);
  // svelte-ignore state_referenced_locally
  let next = $state<string | null>(initial?.next_cursor ?? null);
  // svelte-ignore state_referenced_locally
  let total = $state(initial?.total ?? 0);
  // svelte-ignore state_referenced_locally
  let loading = $state(!initial);
  let error = $state("");
  let q = $state("");
  let status = $state<RecordStatus[]>([]);
  let showHidden = $state(false);
  let title = $state("");
  let creating = $state(false);
  let seq = 0;
  const ready = createReadyOnce(() => void routeReady());
  // svelte-ignore state_referenced_locally
  if (initial) ready.loaded(true);

  function go(to: string) {
    if (navigate) navigate(to);
    else {
      history.pushState(null, "", to);
      window.dispatchEvent(new PopStateEvent("popstate"));
    }
  }

  async function load(more: boolean) {
    const mine = ++seq;
    loading = true;
    error = "";
    const r = await listRecords(api, {
      ...(status.length ? { status } : {}),
      ...(q.trim() ? { q: q.trim() } : {}),
      ...(showHidden ? { include_hidden: true } : {}),
      ...(more && next ? { cursor: next } : {}),
    });
    if (mine !== seq) return;
    loading = false;
    if (r.kind === "ok") {
      items = more ? [...items, ...r.value.items] : r.value.items;
      next = r.value.next_cursor;
      total = r.value.total;
    } else error = r.kind === "unavailable" ? "Records are not available yet." : "Could not load records.";
    ready.loaded(true);
  }

  // Filters (search debounced) reload from the first page.
  let first = true;
  $effect(() => {
    void [q, status.join(), showHidden];
    if (first && initial) {
      first = false;
      return;
    }
    first = false;
    const h = setTimeout(() => void load(false), q ? 300 : 0);
    return () => clearTimeout(h);
  });

  function toggle(s: RecordStatus) {
    status = status.includes(s) ? status.filter((x) => x !== s) : [...status, s];
  }

  async function create() {
    const t = title.trim();
    if (!t || creating) return;
    creating = true;
    // A retry of the same click reuses its key; a new title builds a new action.
    const r = await createRecord(api, { title: t })();
    creating = false;
    if (r.kind === "ok") {
      title = "";
      go(`/records/${r.value.record.id}`);
    } else if (r.kind === "failed") pushToast(r.message || "Could not create the record");
    else pushToast("Could not create the record");
  }
</script>

<section data-testid="records-list">
  <h1>Records</h1>

  <form class="new" onsubmit={(e) => { e.preventDefault(); void create(); }}>
    <label for="record-new-title" class="muted">New record</label>
    <input id="record-new-title" bind:value={title} maxlength="200" placeholder="Title" data-testid="record-new" />
    <button type="submit" class="primary" disabled={creating || !title.trim()}>Create</button>
  </form>

  <div class="filters">
    <input type="search" bind:value={q} placeholder="Search records" aria-label="Search records" />
    <div class="chips" role="group" aria-label="Status">
      {#each STATUSES as s (s)}
        <button type="button" aria-pressed={status.includes(s)} onclick={() => toggle(s)}>{STATUS_LABEL[s]}</button>
      {/each}
      <button type="button" aria-pressed={showHidden} onclick={() => (showHidden = !showHidden)}>Hidden</button>
    </div>
  </div>

  {#if error}<p role="alert">{error}</p>{/if}
  {#if !loading && !error && items.length === 0}<p class="muted">No records yet.</p>{/if}

  <ul class="rows">
    {#each items as r (r.id)}
      <li data-testid="record-row" data-id={r.id}>
        <a class="row" href="/records/{r.id}">
          <RecordCover cover={r.cover} size={56} />
          <span class="body">
            <span class="t">{r.title}</span>
            {#if r.subtitle}<span class="muted sub">{r.subtitle}</span>{/if}
            <span class="muted meta">
              <span class="num">{r.playable}/{r.slots}</span> tracks · <span class="num">{fmt(r.duration_s)}</span>
              {#if r.warnings > 0} · <span class="warnc">{r.warnings} {r.warnings === 1 ? "warning" : "warnings"}</span>{/if}
            </span>
          </span>
          <span class="st"><RecordStatusChip status={r.status} hidden={r.hidden} /></span>
        </a>
      </li>
    {/each}
  </ul>
  {#if loading}<p class="muted" role="status">Loading…</p>{/if}
  {#if next && !loading}<button type="button" onclick={() => load(true)}>Load more ({items.length} of {total})</button>{/if}
</section>

<style>
  .new,
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    align-items: center;
    margin-bottom: var(--sp-3);
  }
  .new input,
  .filters input {
    flex: 1;
    min-width: 12ch;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
  }
  .chips button {
    min-height: var(--hit);
  }
  .chips button[aria-pressed="true"] {
    background: var(--accent-soft);
    border-color: var(--accent);
    color: var(--accent);
  }
  .rows {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--sp-2);
  }
  .row {
    display: flex;
    align-items: center;
    gap: var(--sp-3);
    min-height: 64px;
    padding: var(--sp-2) var(--sp-3);
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: var(--r-md);
    color: var(--text);
    text-decoration: none;
  }
  .body {
    flex: 1;
    min-width: 0;
    display: grid;
  }
  .t {
    font-weight: var(--fw-medium);
    overflow-wrap: anywhere;
  }
  .sub,
  .meta {
    font-size: var(--fs-sm);
  }
  .warnc {
    color: var(--warn);
  }
  .st {
    display: flex;
    gap: var(--sp-1);
    flex-wrap: wrap;
    justify-content: flex-end;
  }
</style>
