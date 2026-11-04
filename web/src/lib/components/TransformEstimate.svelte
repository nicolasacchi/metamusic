<script lang="ts">
  // The estimate of a transform request: cost and CPU line, resolved tempo/key per input with the source chip,
  // every warning as an inline note, and the shared-pool quota and queue state (C214.2).
  import { estimateLine, inputFacts, quotaMeter, warningNote, warningTone, type TransformEstimate } from "../transformView";

  let { estimate: e }: { estimate: TransformEstimate } = $props();
  const meter = $derived(quotaMeter(e.quota));
</script>

<section class="est" aria-label="Estimate" data-testid="transform-estimate">
  <p class="line" data-testid="transform-estimate-line">{estimateLine(e)}</p>
  {#each e.inputs as i (i.role + i.track_id)}
    {@const f = inputFacts(i)}
    <p class="input" data-testid="transform-estimate-input">
      <b>{f.role}</b> <span class="title">{f.title}</span>
      {#each f.chips as c, k (k)}<span class="chip src-{c.source}" data-testid="transform-source-chip" title="from {c.source}">{c.label} <small>{c.source}</small></span>{/each}
    </p>
  {/each}
  {#if e.warnings.length}
    <ul class="warnings" data-testid="transform-warnings">
      {#each e.warnings as w, k (k)}<li class={warningTone(w)} data-code={w.code}>{warningNote(w)}</li>{/each}
    </ul>
  {/if}
  {#if e.existing}
    <p class="note" data-testid="transform-existing">The same transform already exists ({e.existing.state}){#if e.existing.take_id}: <a href="/tracks/{e.existing.take_id}">open the take</a>{/if}.</p>
  {/if}
  <p class="muted pool" data-testid="transform-quota">
    <span class="meter" role="img" aria-label="CPU allowance {meter.usedPct}% used"><i class="used" style:width="{meter.usedPct}%"></i><i class="res" style:width="{meter.reservedPct}%"></i></span>
    {meter.label} today (shared by every {e.quota.principal_kind}) · queue {e.queue.running} running, {e.queue.queued} waiting
  </p>
</section>

<style>
  .est {
    min-width: 0;
  }
  .line {
    font-weight: var(--fw-medium);
    overflow-wrap: anywhere;
    margin: 0 0 var(--sp-2);
  }
  .input {
    margin: var(--sp-1) 0;
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-2);
    align-items: baseline;
    overflow-wrap: anywhere;
  }
  .title {
    min-width: 0;
  }
  .chip small {
    opacity: 0.7;
  }
  .warnings {
    margin: var(--sp-2) 0;
    padding-left: var(--sp-4);
    overflow-wrap: anywhere;
  }
  .warnings .caution {
    color: var(--warn, #e0a030);
  }
  .note {
    overflow-wrap: anywhere;
  }
  .pool {
    font-size: var(--fs-xs);
    overflow-wrap: anywhere;
  }
  .meter {
    display: inline-flex;
    width: 4rem;
    height: 0.5rem;
    vertical-align: middle;
    border-radius: var(--r-pill);
    background: var(--bg-sunken);
    overflow: hidden;
    margin-right: var(--sp-2);
  }
  .used {
    background: var(--accent);
  }
  .res {
    background: var(--line-strong);
  }
</style>
