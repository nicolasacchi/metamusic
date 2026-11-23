<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import BatchReview from "../lib/components/BatchReview.svelte";
  import BatchCard from "../lib/components/BatchCard.svelte";
  import JobRow from "../lib/components/JobRow.svelte";
  import { pushToast } from "../lib/judge.svelte";
  import { pollDelayMs, type Viewer } from "../lib/jobState";
  import { player } from "../lib/player.svelte";
  import PodList from "../lib/components/PodList.svelte";
  import { podErrorCodes, shouldPollPods } from "../lib/bulkView";
  import { can } from "../lib/me.svelte";

  type Batch = components["schemas"]["Batch"];
  type Pod = components["schemas"]["PodSummary"];
  interface Props {
    id: string;
  }
  let { id }: Props = $props();
  const viewer: Viewer = { kind: "human" };
  let batch = $state<Batch | null>(null);
  let error = $state("");
  let busy = $state(false);
  let tick = $state(0);
  // The pod block (m6-bulk C398.6) is the Owner's: list_pods is Owner-only, so nobody else asks.
  let pods = $state<Pod[]>([]);

  let podSeq = 0;
  let podsAnswered = false;
  let mounted = true;
  $effect(() => () => {
    mounted = false;
  });
  async function loadPods() {
    // An empty answer never stops the poll while the batch is queued or running: its pod row may not exist yet.
    if (!mounted || !shouldPollPods({ canList: can("list_pods"), batchState: batch?.state, answered: podsAnswered })) return;
    const mine = ++podSeq;
    try {
      const r = await api.GET("/api/pods", { params: { query: { batch_id: id, state: "all", limit: 20 } } });
      if (r.data && mine === podSeq && mounted) {
        pods = r.data.items;
        podsAnswered = true;
      }
    } catch {
      // the pod block is secondary: the batch stays readable
    }
  }

  async function load() {
    try {
      const r = await api.GET("/api/batches/{id}", { params: { path: { id } } });
      if (r.data) {
        batch = r.data;
        error = "";
        void loadPods();
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

  // Playing a finished take queues the batch's finished takes in take order.
  function playTake(trackId: string) {
    const done = (batch?.jobs ?? []).filter((j) => j.state === "completed" && j.track_id !== null);
    const queue = done.map((j) => j.track_id as string);
    const t = done.find((j) => j.track_id === trackId);
    player.play(trackId, queue, t?.title ? { title: t.title } : {});
  }

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
  {#if batch.state === "awaiting_confirmation"}
    <BatchReview id={batch.id} />
  {/if}
  <BatchCard {batch} {viewer} {busy} onterminate={terminateBatch} />
  {#if batch.rationale}<p>Rationale: {batch.rationale}</p>{/if}
  {#if batch.approval.reasons.length > 0}<p class="muted">Approval notes: {batch.approval.reasons.join("; ")}</p>{/if}
  {#if pods.length > 0}
    <section data-testid="batch-pods" aria-label="Pod">
      <h2>Pod</h2>
      <p class="muted">This batch runs on one rented GPU pod, which stops itself when the batch is done, cancelled or past its deadline.</p>
      <PodList {pods} empty="" now={new Date()} />
      {#if podErrorCodes(pods).length}<p role="alert" data-testid="batch-pod-alerts">Pod alerts: {podErrorCodes(pods).join(", ")}</p>{/if}
    </section>
  {/if}
  <h2>Takes</h2>
  <table data-testid="batch-jobs">
    <tbody>
      {#each batch.jobs as j (j.id)}<JobRow job={j} {viewer} {busy} onterminate={terminateJob} onplay={playTake} batchLink={false} />{/each}
    </tbody>
  </table>
{:else if !error}
  <p class="muted">Loading…</p>
{/if}
