<script lang="ts">
  import { api } from "../api/client";
  import { idempotentAction } from "../api/idem";
  import type { components } from "../api/schema";
  import BackupList from "../lib/components/BackupList.svelte";
  import { pushToast } from "../lib/judge.svelte";

  type Listing = components["schemas"]["BackupListing"];
  let listing = $state<Listing | null>(null);
  let error = $state("");
  let busy = $state(false);

  async function load() {
    try {
      const r = await api.GET("/api/backups", {});
      if (r.data) {
        listing = r.data;
        error = "";
      } else error = r.error?.message ?? "Failed to load backups";
    } catch {
      error = "Failed to load backups";
    }
  }
  $effect(() => {
    void load();
  });

  const running = $derived(listing?.runs.some((r) => r.state === "running") ?? false);
  // Poll while a run is in progress.
  $effect(() => {
    if (!running) return;
    const h = setInterval(() => void load(), 2000);
    return () => clearInterval(h);
  });

  async function backup() {
    if (busy) return;
    busy = true;
    try {
      const send = idempotentAction(async (key) => api.POST("/api/backups", { params: { header: { "Idempotency-Key": key } } }));
      const r = await send();
      if (!r.data) pushToast(r.error?.message ?? "Could not start the backup");
      await load();
    } catch {
      pushToast("Could not start the backup");
    } finally {
      busy = false;
    }
  }
</script>

<h1>Backups</h1>
<p><a href="/settings">Settings</a> · <a href="/settings/audit">Audit log</a></p>
{#if error}<p role="alert">{error}</p>{/if}
<p>
  <button type="button" onclick={backup} disabled={busy || running} data-testid="backup-now">{running ? "Backing up…" : "Back up now"}</button>
</p>
{#if listing}
  <BackupList {listing} />
{:else if !error}
  <p class="muted">Loading…</p>
{/if}
