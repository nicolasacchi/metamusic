<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import BatchCard from "../lib/components/BatchCard.svelte";
  import JobRow from "../lib/components/JobRow.svelte";
  import { pushToast } from "../lib/judge.svelte";
  import { pollDelayMs, type Viewer } from "../lib/jobState";

  type Batch = components["schemas"]["Batch"];
  interface Props {
    id: string;
  }
  let { id }: Props = $props();
  const viewer: Viewer = { kind: "human" };
  let batch = $state<Batch | null>(null);
  let error = $state("");
  let busy = $state(false);
  let tick = $state(0);

  async function load() {
    try {
      const r = await api.GET("/api/batches/{id}", { params: { path: { id } } });
      if (r.data) {
        batch = r.data;
        error = "";
      } else error = r.error?.message ?? "Could not load the batch";
    } catch {
      error = "Could not load the batch";
    }
  }
  $effect(() => {
    void load();
  });
  // Poll every 5 s while any take is live.
  $effect(() => {
    void tick; // re-armed after every attempt, so a failed fetch does not stop the poll
    const delay = pollDelayMs(batch?.jobs ?? []);
    if (delay === null) return;
    const t = setTimeout(() => void load().finally(() => tick++), delay);
    return () => clearTimeout(t);
  });

  async function terminateJob(jobId: string) {
    if (busy) return;
    busy = true;
    try {
      const r = await api.POST("/api/jobs/{id}/cancel", { params: { path: { id: jobId } }, body: {} });
      if (!r.data) pushToast(r.error?.message ?? "Could not terminate the job");
      await load();
    } catch {
      pushToast("Could not terminate the job");
    } finally {
      busy = false;
    }
  }
  async function terminateBatch() {
    if (busy) return;
    busy = true;
    try {
      const r = await api.POST("/api/batches/{id}/cancel", { params: { path: { id } }, body: {} });
      if (!r.data) pushToast(r.error?.message ?? "Could not terminate the batch");
      await load();
    } catch {
      pushToast("Could not terminate the batch");
    } finally {
      busy = false;
    }
  }
</script>

<h1>Batch</h1>
{#if error}<p role="alert">{error}</p>{/if}
{#if batch}
  <BatchCard {batch} {viewer} {busy} onterminate={terminateBatch} />
  {#if batch.rationale}<p>Rationale: {batch.rationale}</p>{/if}
  {#if batch.approval.reasons.length > 0}<p class="muted">Approval notes: {batch.approval.reasons.join("; ")}</p>{/if}
  <h2>Takes</h2>
  <table data-testid="batch-jobs">
    <tbody>
      {#each batch.jobs as j (j.id)}<JobRow job={j} {viewer} {busy} onterminate={terminateJob} />{/each}
    </tbody>
  </table>
{:else if !error}
  <p class="muted">Loading…</p>
{/if}
