<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import AuditTable from "../lib/components/AuditTable.svelte";
  import { auditQuery, defaultAuditFilters, type PrincipalKind } from "../lib/exportView";

  type Entry = components["schemas"]["AuditEntry"];
  const kinds: PrincipalKind[] = ["human", "device", "agent", "system"];

  let filters = $state(defaultAuditFilters());
  let entries = $state<Entry[]>([]);
  let next = $state<string | null>(null);
  let loading = $state(false);
  let error = $state("");
  let gen = 0;

  async function load(cursor?: string) {
    const mine = cursor ? gen : ++gen;
    loading = true;
    error = "";
    try {
      const r = await api.GET("/api/audit", { params: { query: auditQuery(filters, cursor) } });
      if (mine !== gen) return;
      if (r.data) {
        entries = cursor ? [...entries, ...r.data.items] : r.data.items;
        next = r.data.next_cursor;
      } else error = r.error?.message ?? "Failed to load the audit log";
    } catch {
      if (mine === gen) error = "Failed to load the audit log";
    } finally {
      if (mine === gen) loading = false;
    }
  }
  $effect(() => {
    void load();
  });

  function toggleKind(k: PrincipalKind) {
    filters.principalKind = filters.principalKind.includes(k) ? filters.principalKind.filter((x) => x !== k) : [...filters.principalKind, k];
  }
</script>

<h1>Audit log</h1>
<p><a href="/settings">Settings</a> · <a href="/settings/backups">Backups</a></p>

<form
  class="filters"
  data-testid="audit-filters"
  onsubmit={(e) => {
    e.preventDefault();
    void load();
  }}
>
  <fieldset>
    <legend>Who</legend>
    {#each kinds as k (k)}
      <label><input type="checkbox" checked={filters.principalKind.includes(k)} onchange={() => toggleKind(k)} /> {k}</label>
    {/each}
  </fieldset>
  <label>Operations (comma separated) <input bind:value={filters.op} data-testid="audit-op" /></label>
  <label>Since (RFC 3339) <input bind:value={filters.since} placeholder="2026-01-01T00:00:00Z" /></label>
  <label>Until (RFC 3339) <input bind:value={filters.until} /></label>
  <label>Status at least <input type="number" min="100" max="599" bind:value={filters.statusMin} data-testid="audit-status-min" /></label>
  <label><input type="checkbox" bind:checked={filters.includeListen} data-testid="audit-include-listen" /> Include listen events</label>
  <button type="submit" disabled={loading}>Apply</button>
</form>

{#if error}<p role="alert">{error}</p>{/if}
<AuditTable {entries} />
{#if next}
  <p><button type="button" onclick={() => load(next ?? undefined)} disabled={loading} data-testid="audit-more">Load more</button></p>
{/if}

<style>
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: 0.8rem;
    align-items: end;
    margin: 0.5rem 0 1rem;
  }
  fieldset label {
    margin-right: 0.6rem;
  }
</style>
