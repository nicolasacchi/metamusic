<script lang="ts">
  import type { components } from "../../api/schema";
  import { confirmDisabledReasons } from "../confirm";
  import { formatMicroUSD, formatMicroUSDCeil } from "../money";
  import BudgetBar from "./BudgetBar.svelte";

  type Estimate = components["schemas"]["Estimate"];
  interface Props {
    estimate: Estimate;
  }
  let { estimate: e }: Props = $props();

  const reasons = $derived(confirmDisabledReasons(e));
  const eta = $derived(e.eta_s >= 90 ? `${Math.round(e.eta_s / 60)} min` : `${e.eta_s} s`);
</script>

<section data-testid="estimate" aria-label="Estimate">
  <h2>Estimate</h2>
  <p class="muted">{e.takes} take{e.takes === 1 ? "" : "s"} · engine {e.engine} · backend {e.backend} · ETA about {eta}</p>

  <table class="cost" data-testid="estimate-cost">
    <tbody>
      <tr><th scope="row">Expected</th><td data-testid="cost-expected">{formatMicroUSD(e.cost.expected_micro_usd)}</td></tr>
      <tr><th scope="row">Maximum (no retries)</th><td data-testid="cost-max">{formatMicroUSDCeil(e.cost.max_micro_usd)}</td></tr>
      <tr><th scope="row">Retry allowance</th><td data-testid="cost-retry">{formatMicroUSDCeil(e.cost.retry_allowance_micro_usd)}</td></tr>
      <tr><th scope="row">Ceiling (up to)</th><td data-testid="cost-ceiling"><strong>{formatMicroUSDCeil(e.cost.ceiling_micro_usd)}</strong></td></tr>
    </tbody>
  </table>

  {#if e.warnings.length}
    <ul class="warnings" data-testid="estimate-warnings">
      {#each e.warnings as w, i (i)}
        <li data-severity={w.severity}><b>{w.severity}</b> <code>{w.code}</code> {w.message}{#if w.take_index >= 0} <span class="muted">(take {w.take_index + 1}{w.field ? `, ${w.field}` : ""})</span>{/if}</li>
      {/each}
    </ul>
  {/if}

  <h3>Budget</h3>
  <BudgetBar label="Month" settled={e.budget.month.used_micro_usd} cap={e.budget.month.cap_micro_usd} />
  <BudgetBar label="Today" settled={e.budget.day.used_micro_usd} cap={e.budget.day.cap_micro_usd} />
  <p class="muted">Batch cap {formatMicroUSD(e.budget.batch_cap_micro_usd)}.</p>

  {#if e.requires_human_approval}
    <p class="note" data-testid="approval-notes">
      A human must approve this batch before it runs ({e.approval_reasons.map((r) => r.replaceAll("_", " ")).join(", ") || "approval required"}).
    </p>
  {/if}

  {#if reasons.length}
    <ul class="reasons" data-testid="disabled-reasons">
      {#each reasons as r (r)}<li>{r}</li>{/each}
    </ul>
  {/if}

  <details>
    <summary>Takes</summary>
    <table data-testid="estimate-takes">
      <thead><tr><th scope="col">#</th><th scope="col">Title</th><th scope="col">Seed</th><th scope="col">Operator</th><th scope="col">Up to</th></tr></thead>
      <tbody>
        {#each e.items as it (it.index)}
          {#each it.takes as t (t.take_index)}
            <tr><td>{t.take_index + 1}</td><td>{t.title}</td><td>{t.seed}</td><td>{t.operator}</td><td>{formatMicroUSDCeil(t.max_micro_usd)}</td></tr>
          {/each}
        {/each}
      </tbody>
    </table>
  </details>
</section>

<style>
  .cost th {
    text-align: left;
    padding-right: 1.5rem;
    font-weight: normal;
  }
  .warnings,
  .reasons {
    padding-left: 1.2rem;
  }
  .reasons {
    color: #e88;
  }
  .note {
    border-left: 3px solid #f2a65a;
    padding-left: 0.6rem;
  }
  td,
  th {
    padding: 0.15rem 0.5rem 0.15rem 0;
  }
</style>
