<script lang="ts">
  import { untrack } from "svelte";
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import BatchCard from "../lib/components/BatchCard.svelte";
  import BudgetBar from "../lib/components/BudgetBar.svelte";
  import JobRow from "../lib/components/JobRow.svelte";
  import ProviderBanner from "../lib/components/ProviderBanner.svelte";
  import SpendChart from "../lib/components/SpendChart.svelte";
  import { pushToast } from "../lib/judge.svelte";
  import { LIVE_STATES, pollDelayMs, shouldLoadLedger, type Viewer } from "../lib/jobState";
  import { formatMicroUSD } from "../lib/money";

  type Job = components["schemas"]["JobSummary"];
  type Batch = components["schemas"]["BatchSummary"];
  type Budget = components["schemas"]["Budget"];
  type Status = components["schemas"]["ProviderStatus"];
  type Spend = components["schemas"]["Spend"];
  type Ledger = components["schemas"]["LedgerEntry"];

  const viewer: Viewer = { kind: "human" };
  type Tab = "jobs" | "spend" | "ledger" | "provider";
  let tab = $state<Tab>("jobs");
  let live = $state<Job[]>([]);
  let recent = $state<Job[]>([]);
  let batches = $state<Batch[]>([]);
  let budget = $state<Budget | null>(null);
  let status = $state<Status | null>(null);
  let spend = $state<Spend | null>(null);
  let month = $state("");
  let ledger = $state<Ledger[]>([]);
  let ledgerNext = $state<string | null>(null);
  let error = $state("");
  let busy = $state(false);
  let ledgerLoaded = $state(false);
  let tick = $state(0);

  async function loadJobs() {
    try {
      const [l, r, b] = await Promise.all([
        api.GET("/api/jobs", { params: { query: { state: [...LIVE_STATES], limit: 100 } } }),
        api.GET("/api/jobs", { params: { query: { limit: 20 } } }),
        api.GET("/api/batches", { params: { query: { limit: 20 } } }),
      ]);
      if (l.data) live = l.data.items;
      else error = l.error?.message ?? "Could not load the jobs";
      if (r.data) recent = r.data.items;
      if (b.data) batches = b.data.items;
    } catch {
      error = "Could not load the jobs";
    }
  }
  async function loadHeader() {
    try {
      const [b, s] = await Promise.all([api.GET("/api/budget", {}), api.GET("/api/provider", {})]);
      if (b.data) budget = b.data;
      if (s.data) status = s.data;
    } catch {
      error = "Could not load the budget";
    }
  }
  async function loadSpend() {
    try {
      const r = await api.GET("/api/spend", { params: { query: month ? { month } : {} } });
      if (r.data) {
        spend = r.data;
        month = r.data.month;
      } else error = r.error?.message ?? "Could not load the spend";
    } catch {
      error = "Could not load the spend";
    }
  }
  async function loadLedger(more: boolean) {
    try {
      const r = await api.GET("/api/ledger", { params: { query: { limit: 50, ...(more && ledgerNext ? { cursor: ledgerNext } : {}) } } });
      if (r.data) {
        ledger = more ? [...ledger, ...r.data.items] : r.data.items;
        ledgerNext = r.data.next_cursor;
        ledgerLoaded = true;
      } else error = r.error?.message ?? "Could not load the ledger";
    } catch {
      error = "Could not load the ledger";
    }
  }

  // Header, jobs, spend: loaded on every visit. Polling re-runs loadJobs every 5 s while any job is live
  // and stops by itself when none is.
  $effect(() => {
    untrack(() => {
      void loadHeader();
      void loadJobs();
      void loadSpend();
    });
  });
  $effect(() => {
    void tick; // re-armed after every attempt, so a failed fetch does not stop the poll
    const delay = pollDelayMs(live);
    if (delay === null) return;
    const t = setTimeout(() => {
      void Promise.all([loadJobs(), loadHeader()]).finally(() => tick++);
    }, delay);
    return () => clearTimeout(t);
  });
  $effect(() => {
    if (shouldLoadLedger(tab, ledgerLoaded)) untrack(() => void loadLedger(false));
  });

  async function terminateJob(id: string) {
    if (busy) return;
    busy = true;
    try {
      const r = await api.POST("/api/jobs/{id}/cancel", { params: { path: { id } }, body: {} });
      if (!r.data) pushToast(r.error?.message ?? "Could not terminate the job");
      await Promise.all([loadJobs(), loadHeader()]);
    } catch {
      pushToast("Could not terminate the job");
    } finally {
      busy = false;
    }
  }
  async function terminateBatch(id: string) {
    if (busy) return;
    busy = true;
    try {
      const r = await api.POST("/api/batches/{id}/cancel", { params: { path: { id } }, body: {} });
      if (!r.data) pushToast(r.error?.message ?? "Could not terminate the batch");
      await Promise.all([loadJobs(), loadHeader()]);
    } catch {
      pushToast("Could not terminate the batch");
    } finally {
      busy = false;
    }
  }
  const tabs: [Tab, string][] = [
    ["jobs", "Jobs"],
    ["spend", "Spend"],
    ["ledger", "Ledger"],
    ["provider", "Provider"],
  ];
</script>

<h1>Jobs</h1>
<ProviderBanner provider={budget?.provider ?? null} />
{#if error}<p role="alert">{error}</p>{/if}
{#if budget}
  <section aria-label="Budget">
    <BudgetBar label="Month" settled={budget.month.settled_micro_usd} reserved={budget.month.outstanding_micro_usd} cap={budget.month.cap_micro_usd} />
    <BudgetBar label="Today" settled={budget.day.settled_micro_usd} reserved={budget.day.outstanding_micro_usd} cap={budget.day.cap_micro_usd} />
  </section>
{/if}

<div role="tablist" aria-label="Sections">
  {#each tabs as [id, label] (id)}
    <button type="button" role="tab" aria-selected={tab === id} class:on={tab === id} onclick={() => (tab = id)} data-testid="tab-{id}">{label}</button>
  {/each}
</div>

{#if tab === "jobs"}
  <h2>Active</h2>
  {#if live.length === 0}
    <p class="muted" data-testid="no-live">No jobs are running.</p>
  {:else}
    <table data-testid="live-jobs">
      <tbody>
        {#each live as j (j.id)}<JobRow job={j} {viewer} {busy} onterminate={terminateJob} />{/each}
      </tbody>
    </table>
  {/if}
  <h2>Recent jobs</h2>
  {#if recent.length === 0}
    <p class="muted">None yet.</p>
  {:else}
    <table data-testid="recent-jobs">
      <tbody>
        {#each recent as j (j.id)}<JobRow job={j} {viewer} {busy} onterminate={terminateJob} />{/each}
      </tbody>
    </table>
  {/if}
  <h2>Batches</h2>
  {#each batches as b (b.id)}<BatchCard batch={b} {viewer} {busy} onterminate={terminateBatch} />{:else}<p class="muted">None yet.</p>{/each}
{:else if tab === "spend"}
  <h2>Spend</h2>
  <label>Month
    <input type="month" bind:value={month} onchange={() => void loadSpend()} data-testid="month" />
  </label>
  {#if spend}
    <SpendChart days={spend.days} />
    <p data-testid="spend-totals">
      Settled {formatMicroUSD(spend.totals.settled_micro_usd)} · outstanding {formatMicroUSD(spend.totals.outstanding_micro_usd)} · bill adjustments {formatMicroUSD(spend.totals.bill_adjustments_micro_usd)}
    </p>
    <table>
      <thead><tr><th scope="col">Batch</th><th scope="col">By</th><th scope="col">Takes</th><th scope="col">Settled</th><th scope="col">Max</th></tr></thead>
      <tbody>
        {#each spend.batches as b (b.id)}
          <tr><td><a href="/batches/{b.id}">{b.created_at.slice(0, 10)}</a></td><td>{b.created_by.name}</td><td>{b.takes}</td><td>{formatMicroUSD(b.settled_micro_usd)}</td><td>{formatMicroUSD(b.max_micro_usd)}</td></tr>
        {/each}
      </tbody>
    </table>
  {/if}
{:else if tab === "ledger"}
  <h2>Ledger</h2>
  <table data-testid="ledger">
    <thead><tr><th scope="col">When</th><th scope="col">Kind</th><th scope="col">Amount</th><th scope="col">Who</th><th scope="col">Reason</th></tr></thead>
    <tbody>
      {#each ledger as e (e.id)}
        <tr><td>{e.at}</td><td>{e.kind}</td><td>{formatMicroUSD(e.micro_usd)}</td><td>{e.principal.name}</td><td>{e.reason}</td></tr>
      {/each}
    </tbody>
  </table>
  {#if ledgerNext}<p><button type="button" onclick={() => void loadLedger(true)} data-testid="ledger-more">Load more</button></p>{/if}
{:else if status}
  <section aria-label="Provider" data-testid="provider-tab">
    <h2>Provider</h2>
    <p>
      Backend <b>{status.backend}</b> · {status.generation_enabled ? "enabled" : "disabled"} · {status.armed ? "armed" : "disarmed"}{#if status.trip} · <b>tripped ({status.trip.reason})</b>{/if}
    </p>
    {#if status.workers}<p>Workers: {status.workers.running} running, {status.workers.idle} idle, {status.workers.initializing} starting, {status.workers.throttled} throttled, {status.workers.unhealthy} unhealthy.</p>{/if}
    {#if status.endpoint}
      <p>Endpoint {status.endpoint.name}: workers {status.endpoint.workers_min}..{status.endpoint.workers_max}, image {status.endpoint.image_digest_short}.</p>
      {#each status.endpoint.drift as d, i (i)}<p role="alert">Drift: <code>{JSON.stringify(d)}</code></p>{/each}
    {/if}
    {#if status.price}<p>GPU price {formatMicroUSD(status.price.per_hour_micro_usd)}/h.</p>{/if}
    {#if status.balance.micro_usd !== null}<p>Balance {formatMicroUSD(status.balance.micro_usd)}.</p>{/if}
    {#if status.rtf.length > 0}
      <h3>Real-time factor</h3>
      <ul>{#each status.rtf as r, i (i)}<li><code>{JSON.stringify(r)}</code></li>{/each}</ul>
    {/if}
    <p><a href="/human/spend">Arm, disarm and caps</a></p>
  </section>
{:else}
  <p class="muted">Loading the provider status…</p>
{/if}

<style>
  [role="tablist"] {
    display: flex;
    gap: 0.4rem;
    margin: 0.8rem 0;
  }
  [role="tab"].on {
    font-weight: 700;
    border-color: #f2a65a;
  }
  table {
    border-collapse: collapse;
  }
  th,
  td {
    text-align: left;
    padding: 0.2rem 0.8rem 0.2rem 0;
  }
</style>
