<script lang="ts">
  import { confirmDisabledReasons, type EstimateLike } from "../confirm";
  import { can } from "../me.svelte";
  import { formatMicroUSD, formatMicroUSDCeil } from "../money";
  import { takeDetail, taskClasses, warningHint } from "../engines";
  import { checkCostLine, hasListeningCheckChoice, takeBadge, tierBSummary, tierBWarningText } from "../estimateTierB";
  import BudgetBar from "./BudgetBar.svelte";

  interface Props {
    estimate: EstimateLike;
    /** The same estimate with the listening check skipped, when the page has priced it: the check's cost is the difference. */
    without?: EstimateLike | null;
    /** "Skip the listening check" (default off). The page re-estimates with options.tier_b false. */
    skipTierB?: boolean;
    onskip?: (skip: boolean) => void;
  }
  let { estimate: e, without = null, skipTierB = false, onskip }: Props = $props();

  const tb = $derived(tierBSummary(e));
  const costLine = $derived(can("get_spend") ? checkCostLine(e, without) : "");
  const offered = $derived(hasListeningCheckChoice(e) && onskip !== undefined);
  // with no take getting a check (the worker image closed) a Skip box is pointless, unless it is already ticked and must be un-tickable
  const choice = $derived(offered && (tb.checked > 0 || skipTierB));

  const reasons = $derived(confirmDisabledReasons(e));
  const allTakes = $derived(e.items.flatMap((it) => it.takes));
  const classes = $derived(taskClasses(allTakes));
  const upTo = $derived(allTakes.length ? Math.max(...allTakes.map((t) => t.timeout_s)) : 0);
  const eta = $derived(e.eta_s >= 90 ? `${Math.round(e.eta_s / 60)} min` : `${e.eta_s} s`);
</script>

<section data-testid="estimate" aria-label="Estimate">
  <h2>Estimate</h2>
  <p class="muted">{e.takes} take{e.takes === 1 ? "" : "s"} · engine {e.engine}{#if classes.length} · class {classes.join(", ")}{/if} · backend {e.backend} · ETA about {eta}</p>
  {#if e.engine !== "yue2"}
    <p class="muted" data-testid="estimate-engine">Per take about {allTakes.length ? Math.round(allTakes.reduce((a, t) => a + t.expected_audio_s, 0) / allTakes.length) : 0} s of audio, up to {upTo} s of GPU time each.</p>
  {/if}

  <table class="cost" data-testid="estimate-cost">
    <tbody>
      <tr><th scope="row">Expected</th><td data-testid="cost-expected">{formatMicroUSD(e.cost.expected_micro_usd)}</td></tr>
      <tr><th scope="row">Maximum (no retries)</th><td data-testid="cost-max">{formatMicroUSDCeil(e.cost.max_micro_usd)}</td></tr>
      <tr><th scope="row">Retry allowance</th><td data-testid="cost-retry">{formatMicroUSDCeil(e.cost.retry_allowance_micro_usd)}</td></tr>
      <tr><th scope="row">Ceiling (up to)</th><td data-testid="cost-ceiling"><strong>{formatMicroUSDCeil(e.cost.ceiling_micro_usd)}</strong></td></tr>
    </tbody>
  </table>

  {#if tb.analyze || offered || tb.checked > 0}
    <div class="check" data-testid="tierb-estimate">
      <h3>Listening check</h3>
      <p data-testid="tierb-summary">
        {#if tb.analyze}
          Listens to the vocals of {tb.total} take{tb.total === 1 ? "" : "s"}: speech recognition and pitch tracking, no new audio.
        {:else if skipTierB}
          Skipped: no take gets a listening check.
        {:else if tb.checked === 0}
          No take gets a listening check after the render.
        {:else}
          {tb.checked} of {tb.total} take{tb.total === 1 ? "" : "s"} get{tb.total === 1 && tb.checked === 1 ? "s" : ""} a listening check after the render.
        {/if}
      </p>
      {#if costLine}<p data-testid="tierb-cost">{costLine}</p>{/if}
      {#if choice}
        <label class="skip"><input type="checkbox" checked={skipTierB} onchange={(ev) => onskip?.(ev.currentTarget.checked)} data-testid="skip-tierb" /> Skip the listening check</label>
      {/if}
    </div>
  {/if}

  {#if e.warnings.length}
    <ul class="warnings" data-testid="estimate-warnings">
      {#each e.warnings as w, i (i)}
        {#if tierBWarningText(w.code)}
          <li data-severity={w.severity} data-testid="tierb-warning"><b>note</b> {tierBWarningText(w.code)}{#if w.take_index >= 0} <span class="muted">(take {w.take_index + 1})</span>{/if}</li>
        {:else}
        <li data-severity={w.severity}><b>{w.severity}</b> <code>{w.code}</code> {w.message}{#if warningHint(w.code)} <span class="muted" data-testid="hint-{w.code}">{warningHint(w.code)}</span>{/if}{#if w.take_index >= 0} <span class="muted">(take {w.take_index + 1}{w.field ? `, ${w.field}` : ""})</span>{/if}</li>
        {/if}
      {/each}
    </ul>
  {/if}

  {#if e.budget?.month && e.budget.day && can("get_spend")}
    <h3>Budget</h3>
    <BudgetBar label="Month" settled={e.budget.month.used_micro_usd} cap={e.budget.month.cap_micro_usd} />
    <BudgetBar label="Today" settled={e.budget.day.used_micro_usd} cap={e.budget.day.cap_micro_usd} />
    {#if e.budget.batch_cap_micro_usd !== undefined}<p class="muted">Batch cap {formatMicroUSD(e.budget.batch_cap_micro_usd)}.</p>{/if}
  {/if}

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
    <div class="scroll">
    <table data-testid="estimate-takes">
      <thead><tr><th scope="col">#</th><th scope="col">Title</th><th scope="col">Seed</th><th scope="col">Operator</th>{#if e.engine !== "yue2"}<th scope="col">Task</th>{/if}{#if e.engine === "yue2"}<th scope="col">Check</th>{/if}<th scope="col">Up to</th></tr></thead>
      <tbody>
        {#each e.items as it (it.index)}
          {#each it.takes as t (t.take_index)}
            <tr><td>{t.take_index + 1}</td><td>{t.title}</td><td>{t.seed}</td><td>{t.operator}</td>{#if e.engine !== "yue2"}<td data-testid="take-detail">{takeDetail(t)}</td>{/if}{#if e.engine === "yue2"}{@const b = takeBadge(t, it.mode, e.warnings)}<td data-testid="take-check" data-on={b.on}><span class="badge" class:off={!b.on} title={b.reason}>{b.label}</span></td>{/if}<td>{formatMicroUSDCeil(t.max_micro_usd)}</td></tr>
          {/each}
        {/each}
      </tbody>
    </table>
    </div>
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
  .scroll {
    overflow-x: auto;
    max-width: 100%;
  }
  .warnings {
    overflow-wrap: anywhere;
  }
  .check {
    border-left: 3px solid #6a8caf;
    padding-left: 0.6rem;
    margin: 0.6rem 0;
  }
  .check h3 {
    margin: 0 0 0.2rem;
    font-size: 0.9rem;
  }
  .check p {
    margin: 0.2rem 0;
    overflow-wrap: anywhere;
  }
  .skip {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    min-height: var(--hit, 2rem);
  }
  .badge {
    border: 1px solid #6a8caf;
    border-radius: 0.8rem;
    padding: 0 0.5rem;
    font-size: 0.8rem;
    white-space: nowrap;
  }
  .badge.off {
    border-color: #8a7f74;
    opacity: 0.75;
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
