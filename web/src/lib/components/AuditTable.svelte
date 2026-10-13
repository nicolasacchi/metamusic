<script lang="ts">
  import type { components } from "../../api/schema";

  type Entry = components["schemas"]["AuditEntry"];
  interface Props {
    entries: readonly Entry[];
  }
  let { entries }: Props = $props();
</script>

<table data-testid="audit-table">
  <thead>
    <tr><th scope="col">When</th><th scope="col">Who</th><th scope="col">Operation</th><th scope="col">Status</th><th scope="col">Created</th><th scope="col">ms</th></tr>
  </thead>
  <tbody>
    {#each entries as e (e.id)}
      <tr data-op={e.op} data-status={e.status}>
        <td>{e.at}</td>
        <td>{e.principal.kind}{e.principal.name ? ` · ${e.principal.name}` : ""}</td>
        <td>{e.op} <span class="muted">{e.method}</span></td>
        <td>{e.status}{#if e.error_code} <span class="muted">{e.error_code}</span>{/if}</td>
        <td>{e.created_ids.join(", ")}</td>
        <td>{e.duration_ms}</td>
      </tr>
    {:else}
      <tr><td colspan="6" class="muted">No matching entries.</td></tr>
    {/each}
  </tbody>
</table>

<style>
  td,
  th {
    text-align: left;
    padding: 0.25rem 0.75rem 0.25rem 0;
    vertical-align: top;
  }
</style>
