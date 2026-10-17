<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import { formatMicroUSD, formatMicroUSDCeil } from "../lib/money";
  import { pushToast } from "../lib/judge.svelte";
  import { approvals } from "../lib/approvals.svelte";
  import { stepUpExpired } from "../lib/approvals";

  type Batch = components["schemas"]["Batch"];
  type Budget = components["schemas"]["Budget"];
  interface Props {
    batch: string;
  }
  let { batch: id }: Props = $props();

  let b = $state<Batch | null>(null);
  let budget = $state<Budget | null>(null);
  let error = $state("");
  let reason = $state("");
  let busy = $state(false);

  async function load() {
    try {
      const [r, bud] = await Promise.all([api.GET("/api/batches/{id}", { params: { path: { id } } }), api.GET("/api/budget", {})]);
      if (r.data) {
        b = r.data;
        error = "";
      } else error = r.response.status === 404 ? "Batch not found" : (r.error?.message ?? "Could not load the batch");
      if (bud.data) budget = bud.data;
    } catch {
      error = "Could not load the batch";
    }
  }
  $effect(() => {
    void id;
    void load();
  });

  // Local mode only (null elsewhere): tell the operator before the tap that Approve will ask for the password first.
  let now = $state(Date.now());
  $effect(() => {
    const t = setInterval(() => (now = Date.now()), 15_000);
    return () => clearInterval(t);
  });
  const confirmNeeded = $derived(stepUpExpired(approvals.stepUpUntil, now));

  const pending = $derived(b?.state === "awaiting_confirmation");

  async function decide(approve: boolean) {
    if (busy) return;
    if (!approve && reason.trim() === "") {
      pushToast("A reason is required to reject.");
      return;
    }
    busy = true;
    try {
      const r = approve
        ? await api.POST("/human/api/batches/{id}/approve", { params: { path: { id } }, body: reason.trim() ? { reason: reason.trim() } : {} })
        : await api.POST("/human/api/batches/{id}/reject", { params: { path: { id } }, body: { reason: reason.trim() } });
      if (r.data) b = r.data;
      else pushToast(r.error?.message ?? "Could not record the decision");
    } catch {
      pushToast("Could not record the decision");
    } finally {
      busy = false;
    }
  }
</script>

<h1>Approve spend</h1>
{#if error}<p role="alert">{error}</p>{/if}
{#if b}
  <section data-testid="confirm-batch" data-state={b.state}>
    <p>
      Requested by <strong data-testid="requested-by">{b.created_by.name}</strong> ({b.created_by.kind}) at {b.created_at}.
      State: <b data-testid="batch-state">{b.state.replaceAll("_", " ")}</b>.
      <a href="/batches/{b.id}">Open batch</a>
    </p>
    {#if b.rationale}<p>Rationale: <q data-testid="rationale">{b.rationale}</q></p>{/if}
    <table>
      <tbody>
        <tr><th scope="row">Takes</th><td>{b.takes}</td></tr>
        <tr><th scope="row">Expected</th><td data-testid="cost-expected">{formatMicroUSD(b.cost.expected_micro_usd)}</td></tr>
        <tr><th scope="row">Maximum</th><td data-testid="cost-max">{formatMicroUSDCeil(b.cost.max_micro_usd)}</td></tr>
        <tr><th scope="row">Ceiling (up to)</th><td data-testid="cost-ceiling"><strong>{formatMicroUSDCeil(b.cost.ceiling_micro_usd)}</strong></td></tr>
      </tbody>
    </table>
    {#if budget}
      <p class="muted" data-testid="budget-impact">
        Budget impact: month {formatMicroUSD(budget.month.left_micro_usd)} left of {formatMicroUSD(budget.month.cap_micro_usd)};
        reserving up to {formatMicroUSDCeil(b.cost.ceiling_micro_usd)}.
        {#if b.cost.ceiling_micro_usd > budget.month.left_micro_usd}<b>This exceeds the monthly budget left.</b>{/if}
      </p>
    {/if}
    <h2>Takes</h2>
    <table data-testid="batch-takes">
      <thead><tr><th scope="col">#</th><th scope="col">Request</th><th scope="col">Title</th><th scope="col">Seed</th><th scope="col">Source</th></tr></thead>
      <tbody>
        {#each b.jobs as j (j.id)}
          <tr>
            <td>{j.take_index + 1}</td>
            <td><code>{j.request_id}</code></td>
            <td>{j.title}</td>
            <td>{j.seed}</td>
            <td>
              {#if j.parent_track_id}<a href="/tracks/{j.parent_track_id}">parent track</a>{/if}
              {#if j.draft_id}<a href="/backlog/{j.draft_id}">draft</a>{/if}
              <span class="muted">{j.operator}</span>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
    {#if pending}
      <label>Reason (required to reject)
        <input type="text" bind:value={reason} maxlength="500" data-testid="decision-reason" />
      </label>
      {#if confirmNeeded}
        <p class="muted" role="status" data-testid="confirm-needed">
          Password confirmation needed: Approve or Reject opens it in a new tab, then continues here.
        </p>
      {/if}
      <p>
        <button type="button" class="primary" disabled={busy} onclick={() => decide(true)} data-testid="approve">Approve</button>
        <button type="button" disabled={busy} onclick={() => decide(false)} data-testid="reject">Reject</button>
      </p>
    {:else}
      <p class="muted" data-testid="decided">Nothing to decide: this batch is {b.state.replaceAll("_", " ")}.</p>
    {/if}
  </section>
{:else if !error}
  <p class="muted">Loading…</p>
{/if}

<style>
  label {
    display: grid;
    gap: var(--sp-1);
    margin-bottom: var(--sp-2);
  }
  th {
    text-align: left;
    padding-right: 1.5rem;
    font-weight: normal;
  }
  td {
    padding-right: 0.8rem;
  }
</style>
