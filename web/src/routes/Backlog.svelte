<script lang="ts">
  import { SvelteURLSearchParams } from "svelte/reactivity";
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import Pager from "../lib/components/Pager.svelte";

  type Draft = components["schemas"]["DraftSummary"];
  type Stats = components["schemas"]["Stats"];
  type Status = "backlog" | "fulfilled" | "archived";

  const PAGE = 50;
  interface Props {
    /** window.location.search */
    search: string;
    navigate: (to: string, replace?: boolean) => void;
  }
  let { search, navigate }: Props = $props();

  const params = $derived(new SvelteURLSearchParams(search));
  const q = $derived(params.get("q") ?? "");
  const collection = $derived(params.get("collection") ?? "");
  const ORIGINS = ["import_backlog", "varianti_import", "editor", "agent", "jsonl_upload"] as const;
  const origin = $derived(params.get("origin") ?? "");
  const rawStatus = $derived(params.get("status"));
  const status = $derived<Status>(rawStatus === "fulfilled" || rawStatus === "archived" ? rawStatus : "backlog");

  let drafts = $state<Draft[]>([]);
  let total = $state(0);
  let groupsTotal = $state(0);
  let loading = $state(true);
  let error = $state("");
  let collections = $state<{ name: string; drafts: number }[]>([]);
  let cursors = $state<(string | undefined)[]>([undefined]);
  let starts = $state<number[]>([0]);
  let page = $state(0);
  let nextCursor = $state<string | null>(null);
  let seq = 0;
  let qInput = $derived(q);

  function commit(next: { q?: string; collection?: string; status?: Status; origin?: string }) {
    const p = new SvelteURLSearchParams();
    const nq = next.q ?? q;
    const nc = next.collection ?? collection;
    const ns = next.status ?? status;
    const no = next.origin ?? origin;
    if (nq) p.set("q", nq);
    if (nc) p.set("collection", nc);
    if (no) p.set("origin", no);
    if (ns !== "backlog") p.set("status", ns);
    const s = p.toString();
    navigate("/backlog" + (s ? "?" + s : ""), false);
  }

  async function load(cursor: string | undefined) {
    const mine = ++seq;
    loading = true;
    error = "";
    try {
      const r = await api.GET("/api/drafts", {
        params: { query: { status, limit: PAGE, ...(q ? { q } : {}), ...(collection ? { collection } : {}), ...(origin ? { origin } : {}), ...(cursor ? { cursor } : {}) } },
      });
      if (mine !== seq) return;
      if (!r.data) {
        error = r.error?.message ?? "Failed to load drafts";
        return;
      }
      drafts = r.data.items;
      total = r.data.total;
      groupsTotal = r.data.groups_total;
      nextCursor = r.data.next_cursor;
    } catch {
      if (mine === seq) error = "Failed to load drafts";
    } finally {
      if (mine === seq) loading = false;
    }
  }

  $effect(() => {
    void q;
    void collection;
    void origin;
    void status;
    cursors = [undefined];
    starts = [0];
    page = 0;
    nextCursor = null;
    void load(undefined);
  });

  $effect(() => {
    void (async () => {
      try {
        const r = await api.GET("/api/stats");
        const s: Stats | undefined = r.data;
        if (s) collections = s.backlog.collections;
      } catch {
        /* the filter is optional */
      }
    })();
  });

  function go(delta: number) {
    const target = page + delta;
    if (target < 0) return;
    if (delta > 0) {
      if (!nextCursor) return;
      cursors[target] = nextCursor;
      starts[target] = (starts[page] ?? 0) + drafts.length;
    }
    page = target;
    void load(cursors[target]);
  }

  // Items arrive ordered by (group_key, request_id): consecutive runs form the groups.
  const groups = $derived.by(() => {
    const out: { key: string; items: Draft[] }[] = [];
    for (const d of drafts) {
      const key = d.group_key ?? "";
      const last = out[out.length - 1];
      if (last && last.key === key) last.items.push(d);
      else out.push({ key, items: [d] });
    }
    return out;
  });

  function submit(e: Event) {
    e.preventDefault();
    commit({ q: qInput.trim() });
  }
</script>

<h1>Backlog</h1>
<p><a href="/backlog/new">New draft</a> · <a href="/backlog/upload">Upload JSONL</a></p>

<form class="filters" onsubmit={submit} role="search" aria-label="Filter drafts">
  <label>Search <input type="search" bind:value={qInput} maxlength="200" data-testid="backlog-q" /></label>
  <label>
    Collection
    <select value={collection} onchange={(e) => commit({ collection: e.currentTarget.value })} data-testid="backlog-collection">
      <option value="">All</option>
      {#each collections as c (c.name)}
        <option value={c.name}>{c.name} ({c.drafts})</option>
      {/each}
    </select>
  </label>
  <label>
    Origin
    <select value={origin} onchange={(e) => commit({ origin: e.currentTarget.value })} data-testid="backlog-origin">
      <option value="">All</option>
      {#each ORIGINS as o (o)}<option value={o}>{o}</option>{/each}
    </select>
  </label>
  <label>
    Status
    <select value={status} onchange={(e) => commit({ status: e.currentTarget.value as Status })} data-testid="backlog-status">
      <option value="backlog">Backlog</option>
      <option value="fulfilled">Fulfilled</option>
      <option value="archived">Archived</option>
    </select>
  </label>
  <button type="submit">Search</button>
</form>

<p class="muted" data-testid="backlog-counts">{total} drafts in {groupsTotal} groups</p>
{#if error}<p role="alert">{error}</p>{/if}

<div aria-busy={loading}>
  {#each groups as g (g.key + ":" + g.items[0]?.id)}
    <section class="group" data-testid="backlog-group" data-group-key={g.key}>
      <h2>{g.key || "Ungrouped"} <span class="muted">({g.items.length})</span></h2>
      <ul>
        {#each g.items as d (d.id)}
          <li data-testid="draft-row" data-draft-id={d.id}>
            <a href="/backlog/{d.id}">{d.label ?? d.request_id}</a>
            <span class="muted">{d.request_id}</span>
            {#if d.lint.errors > 0}<span class="badge err" data-testid="lint-errors">{d.lint.errors} errors</span>{/if}
            {#if d.lint.warnings > 0}<span class="badge warn" data-testid="lint-warnings">{d.lint.warnings} warnings</span>{/if}
            {#each d.collections as c (c)}<span class="badge">{c}</span>{/each}
            <div class="style">{d.style_excerpt}</div>
          </li>
        {/each}
      </ul>
    </section>
  {:else}
    {#if !loading && !error}<p class="muted">No drafts.</p>{/if}
  {/each}
</div>

<Pager {page} offset={starts[page] ?? 0} count={drafts.length} {total} busy={loading} hasNext={nextCursor !== null} onprev={() => go(-1)} onnext={() => go(1)} />

<style>
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: 0.75rem;
    align-items: end;
    margin-bottom: 0.5rem;
  }
  .filters label {
    display: grid;
    gap: 0.2rem;
    font-size: 0.85rem;
  }
  .group h2 {
    font-size: 1rem;
    margin: 1rem 0 0.25rem;
  }
  ul {
    list-style: none;
    padding: 0;
    margin: 0;
  }
  li {
    padding: 0.4rem 0;
    border-bottom: 1px solid #2a241f;
    overflow-wrap: anywhere;
  }
  .style {
    font-size: 0.8rem;
    opacity: 0.7;
  }
  .err {
    color: #e66;
  }
  .warn {
    color: #db5;
  }
  .badge {
    font-size: 0.7rem;
    border: 1px solid #3a322b;
    border-radius: 0.5rem;
    padding: 0 0.4rem;
    margin-left: 0.3rem;
  }
</style>
