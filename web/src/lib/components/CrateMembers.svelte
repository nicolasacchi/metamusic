<script lang="ts">
  import { api } from "../../api/client";
  import { editItems, listMembers, type MemberRow } from "../crateApi";
  import type { Subject } from "../crateFilter";
  import { fmt } from "../time";

  type Query = Record<string, string | number | boolean | string[]>;
  interface Props {
    subject: Subject;
    /** `{crate_id}` for a saved Crate, or a filter's list query for a preview. */
    query: Query;
    /** Manual Crates: the Crate id; each row gets a Remove button (no multi-select). */
    removeFrom?: string;
    pageSize?: number;
    /** Bump to reload. */
    version?: number;
    onloaded?: (total: number, rows: MemberRow[]) => void;
    onremoved?: () => void;
    /** Test seam: a first page without fetching. */
    initial?: { items: MemberRow[]; next: string | null; total: number };
  }
  let { subject, query, removeFrom, pageSize = 50, version = 0, onloaded, onremoved, initial }: Props = $props();

  // svelte-ignore state_referenced_locally
  let rows = $state<MemberRow[]>(initial?.items ?? []);
  // svelte-ignore state_referenced_locally
  let next = $state<string | null>(initial?.next ?? null);
  // svelte-ignore state_referenced_locally
  let total = $state(initial?.total ?? 0);
  // svelte-ignore state_referenced_locally
  let loading = $state(!initial);
  let error = $state("");
  let seq = 0;

  async function load(more: boolean) {
    const mine = ++seq;
    loading = true;
    error = "";
    const r = await listMembers(api, subject, { ...query, limit: pageSize, ...(more && next ? { cursor: next } : {}) });
    if (mine !== seq) return;
    loading = false;
    if (!r.ok) {
      error = r.message;
      return;
    }
    rows = more ? [...rows, ...r.value.items] : r.value.items;
    next = r.value.next;
    total = r.value.total;
    onloaded?.(total, rows);
  }
  $effect(() => {
    void JSON.stringify(query);
    void version;
    if (initial && version === 0) return;
    void load(false);
  });

  async function remove(row: MemberRow) {
    if (!removeFrom) return;
    const r = await editItems(api, removeFrom, { remove: [subject === "song" ? { song_id: row.id } : { track_id: row.id }] });
    if (r.ok) {
      rows = rows.filter((x) => x.id !== row.id);
      total = r.value.count;
      onremoved?.();
    } else error = r.message;
  }
  const href = (r: MemberRow) => (subject === "song" ? "/songs/" : "/tracks/") + encodeURIComponent(r.id);
</script>

<div data-testid="crate-members">
  {#if error}<p role="alert" data-testid="members-error">{error}</p>{/if}
  {#if loading && rows.length === 0}
    <p class="muted">Loading…</p>
  {:else if rows.length === 0 && !error}
    <p class="muted" data-testid="members-empty">{removeFrom ? "No members yet. Add Songs or Takes from their pages or from the Board." : "Nothing matches."}</p>
  {:else}
    <ul>
      {#each rows as r (r.id)}
        <li data-testid="member-row" data-id={r.id}>
          <a class="title" href={href(r)}>{r.title}</a>
          <span class="muted">{r.detail}{r.durationS !== null ? ` · ${fmt(r.durationS)}` : ""}</span>
          {#if removeFrom}<button type="button" onclick={() => remove(r)} aria-label="Remove {r.title} from this Crate" data-testid="member-remove">Remove</button>{/if}
        </li>
      {/each}
    </ul>
    <p class="muted" data-testid="members-total">{rows.length} of {total}</p>
    {#if next}<button type="button" disabled={loading} onclick={() => void load(true)} data-testid="members-more">Load more</button>{/if}
  {/if}
</div>

<style>
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--sp-1);
  }
  li {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    align-items: center;
    min-height: var(--hit);
    padding: var(--sp-1) 0;
    border-bottom: 1px solid var(--line);
  }
  .title {
    flex: 1 1 12rem;
    min-height: var(--hit);
    display: inline-flex;
    align-items: center;
  }
</style>
