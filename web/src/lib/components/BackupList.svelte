<script lang="ts">
  import type { components } from "../../api/schema";

  type Listing = components["schemas"]["BackupListing"];
  interface Props {
    listing: Listing;
  }
  let { listing }: Props = $props();

  function size(n: number): string {
    if (n < 1024) return `${n} B`;
    if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KiB`;
    if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MiB`;
    return `${(n / 1024 ** 3).toFixed(2)} GiB`;
  }
</script>

<p class="muted" data-testid="backup-schedule">
  Schedule: {listing.schedule.at === "off" ? "off" : `daily at ${listing.schedule.at}`}
  {#if listing.schedule.next_run_at} · next {listing.schedule.next_run_at}{/if}
  · stored in <code>{listing.backup_dir_label}</code>
</p>

<h2>Runs</h2>
{#if listing.runs.length === 0}
  <p class="muted">No backups yet.</p>
{:else}
  <table data-testid="backup-runs">
    <thead><tr><th scope="col">Started</th><th scope="col">Cause</th><th scope="col">State</th><th scope="col">Snapshot</th><th scope="col">Files</th><th scope="col">Size</th></tr></thead>
    <tbody>
      {#each listing.runs as r (r.id)}
        <tr data-state={r.state}>
          <td>{r.started_at}</td>
          <td>{r.cause}</td>
          <td>{r.state}{#if r.error}: {r.error}{/if}</td>
          <td>{r.snapshot ?? "—"}</td>
          <td>{r.stats ? r.stats.files : "—"}</td>
          <td>{r.stats ? size(r.stats.bytes) : "—"}</td>
        </tr>
      {/each}
    </tbody>
  </table>
{/if}

<h2>Snapshots</h2>
{#if listing.snapshots.length === 0}
  <p class="muted">No snapshots.</p>
{:else}
  <table data-testid="backup-snapshots">
    <thead><tr><th scope="col">Name</th><th scope="col">Created</th><th scope="col">Files</th><th scope="col">Linked</th><th scope="col">Copied</th><th scope="col">Size</th></tr></thead>
    <tbody>
      {#each listing.snapshots as s (s.name)}
        <tr><td>{s.name}</td><td>{s.created_at}</td><td>{s.files}</td><td>{s.linked}</td><td>{s.copied}</td><td>{size(s.bytes)}</td></tr>
      {/each}
    </tbody>
  </table>
{/if}

<style>
  td,
  th {
    text-align: left;
    padding: 0.25rem 0.75rem 0.25rem 0;
  }
</style>
