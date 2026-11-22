<script lang="ts">
  import { api } from "../api/client";
  import { pushToast } from "../lib/judge.svelte";
  import { formatMicroUSD } from "../lib/money";
  import { can } from "../lib/me.svelte";
  import { reasonError } from "../lib/mandateView";
  import { ATTENDED_NOTICE, OFF_NOTICE, nightStatus, planSummary, reasonLabel, runRows, type NightPlan, type NightShift } from "../lib/nightView";

  let ns = $state<NightShift | null>(null);
  let plan = $state<NightPlan | null>(null);
  let error = $state("");
  let planError = $state("");
  let reason = $state("");
  let haltError = $state("");
  let busy = $state(false);

  async function load() {
    try {
      const r = await api.GET("/api/night-shift", {});
      if (r.data) {
        ns = r.data;
        error = "";
      } else error = r.error?.message ?? "Could not load the night shift";
    } catch {
      error = "Could not load the night shift";
    }
  }
  async function preview() {
    try {
      const r = await api.GET("/api/night-shift/preview", {});
      if (r.data) {
        plan = r.data;
        planError = "";
      } else planError = r.error?.message ?? "Could not price tonight";
    } catch {
      planError = "Could not price tonight";
    }
  }
  $effect(() => {
    void load();
    void preview();
  });

  async function halt(e: Event) {
    e.preventDefault();
    if (busy) return;
    const bad = reasonError(reason);
    if (bad) return void (haltError = bad);
    busy = true;
    try {
      const r = await api.POST("/api/night-shift/halt", { body: { reason: reason.trim() } });
      if (r.data) {
        pushToast("Night shift halted.");
        reason = "";
        haltError = "";
        await Promise.all([load(), preview()]);
      } else haltError = r.error?.message ?? "Could not halt the night shift";
    } catch {
      haltError = "Could not halt the night shift";
    } finally {
      busy = false;
    }
  }
  const status = $derived(ns ? nightStatus(ns) : null);
  const rows = $derived(ns ? runRows(ns.runs) : []);
  const summary = $derived(plan ? planSummary(plan) : null);
</script>

<section data-testid="night-shift-page">
  <h1>Night shift</h1>
  <div class="notice" data-testid="night-notice">
    {#if !ns || !ns.settings.enabled}<p data-testid="night-off-notice"><strong>{OFF_NOTICE}</strong></p>{/if}
    <p data-testid="night-attended-notice">{ATTENDED_NOTICE}</p>
  </div>
  {#if error}<p role="alert" data-testid="night-error">{error}</p>{/if}

  {#if ns && status}
    <h2>Status</h2>
    <p data-testid="night-status" data-tone={status.tone}><span class="chip {status.tone}">{status.headline}</span> {status.detail}</p>
    <dl>
      <dt>Runs at</dt><dd>{ns.settings.at}</dd>
      <dt>Next run</dt><dd data-testid="night-next">{ns.next_run_at ?? "not scheduled"}</dd>
      <dt>Takes per night</dt><dd>up to {ns.settings.max_takes}</dd>
      <dt>Authority</dt>
      <dd data-testid="night-authority">
        {#if ns.authority.mandate}
          <a href="/mandates/{ns.authority.mandate.id}">{ns.authority.mandate.title}</a> ({ns.authority.mandate.effective_state}), {formatMicroUSD(ns.authority.mandate.budget_micro_usd)}
        {:else}
          none ({reasonLabel(ns.authority.reason)}). <a href="/mandates">Mandates</a>
        {/if}
      </dd>
    </dl>
    {#if can("update_night_shift")}<p><a href="/human/night-shift" data-testid="night-settings-link">Change the night shift (secure)</a></p>{/if}
  {/if}

  <h2>Tonight</h2>
  {#if planError}<p role="alert" data-testid="night-plan-error">{planError}</p>{/if}
  {#if summary && plan}
    <p data-testid="night-plan">{summary.line}</p>
    {#if plan.mandate && !plan.mandate.covered}<p class="muted">The mandate does not cover it: {(plan.mandate.reasons ?? []).map(reasonLabel).join(", ")}.</p>{/if}
    {#if summary.dropped > 0 || summary.trimmed > 0}<p class="muted">{summary.dropped} lineage{summary.dropped === 1 ? "" : "s"} left out, {summary.trimmed} trimmed to fit.</p>{/if}
    {#if (plan.items ?? []).length > 0}
      <ul data-testid="night-plan-items">
        {#each plan.items ?? [] as i (i.root_track_id + i.kind)}
          <li><span class="chip">{i.kind}</span> <a href="/lineage/{i.root_track_id}">{i.root_track_id}</a> ({i.mode}): {i.reason}</li>
        {/each}
      </ul>
    {/if}
  {:else if !planError}
    <p class="muted" role="status">Pricing tonight…</p>
  {/if}
  <p><button type="button" onclick={() => void preview()} data-testid="night-preview-refresh">Price it again</button></p>

  <h2>Last nights</h2>
  {#if ns && rows.length === 0}
    <p class="muted" data-testid="night-runs-empty">No night has run yet.</p>
  {:else}
    <ul data-testid="night-runs">
      {#each rows as r (r.night)}
        <li><strong>{r.night}</strong> <span class="chip {r.state}">{r.stateText}</span> {r.detail}
          {#if r.batchId}<a href="/batches/{r.batchId}">batch</a>{/if}
        </li>
      {/each}
    </ul>
  {/if}

  <h2>Halt</h2>
  <form onsubmit={halt} data-testid="night-halt-form">
    <label>Reason
      <input type="text" bind:value={reason} maxlength="500" data-testid="night-halt-reason" />
    </label>
    {#if haltError}<p role="alert" data-testid="night-halt-error">{haltError}</p>{/if}
    <button type="submit" disabled={busy} data-testid="night-halt">Halt the night shift</button>
    <p class="muted">A halt stops tonight and every night after, until the Owner enables the night shift again in the secure settings.</p>
  </form>
</section>

<style>
  .notice {
    padding: var(--sp-3) var(--sp-4);
    background: var(--warn-soft);
    border: 1px solid var(--warn);
    border-radius: var(--r-md);
  }
  .notice p {
    margin: var(--sp-1) 0;
  }
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: var(--sp-1) var(--sp-4);
  }
  dt {
    color: var(--text-2);
  }
  dd {
    margin: 0;
  }
  .chip {
    padding: 0 var(--sp-2);
    border-radius: var(--r-pill);
    font-size: var(--fs-xs);
    background: var(--surface-2);
    color: var(--text-2);
  }
  .chip.on,
  .chip.done {
    background: color-mix(in srgb, var(--ok) 25%, transparent);
    color: var(--ok);
  }
  .chip.halted,
  .chip.failed {
    background: var(--bad-soft);
    color: var(--bad);
  }
  form {
    display: grid;
    gap: var(--sp-2);
    max-width: 32rem;
  }
  label {
    display: grid;
    gap: var(--sp-1);
  }
</style>
