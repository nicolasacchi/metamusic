<script lang="ts">
  import { approvals } from "../approvals.svelte";
  import { ageLabel, confirmHref, expiryLabel, reasonLabel } from "../approvals";
  import { formatMicroUSDCeil } from "../money";

  let now = $state(Date.now());
  $effect(() => {
    const t = setInterval(() => (now = Date.now()), 30_000);
    return () => clearInterval(t);
  });
  const readOnly = $derived(approvals.viewer === "device");
</script>

{#if approvals.items.length > 0}
  <section class="pending" aria-label="Approvals" data-testid="approvals">
    <h2>Approvals <span class="count" data-testid="approvals-count">{approvals.items.length}{approvals.more ? "+" : ""}</span></h2>
    <p class="muted">Agent batches waiting for your approval. Nothing is spent until you approve.</p>
    {#if readOnly}
      <p class="muted" data-testid="approvals-readonly">This device cannot approve: approving needs the operator's second-factor page, which each link opens.</p>
    {/if}
    {#each approvals.items as b (b.id)}
      <article class="row" data-testid="approval-row" data-batch={b.id}>
        <p>
          <strong data-testid="approval-by">{b.created_by.name}</strong> asks for <span data-testid="approval-takes">{b.takes} take{b.takes === 1 ? "" : "s"}</span>,
          spend up to <strong data-testid="approval-ceiling">{formatMicroUSDCeil(b.cost.ceiling_micro_usd)}</strong>
        </p>
        <p class="muted">
          {#each b.approval.reasons as r, i (r)}{i > 0 ? "; " : ""}<span data-testid="approval-reason">{reasonLabel(r)}</span>{/each}
          · {ageLabel(b.created_at, now)} · {expiryLabel(b.approval.expires_at, now)}
        </p>
        <p><a class="primary" href={confirmHref(b.id)} data-testid="approval-link">Review &amp; approve</a> <a href="/batches/{b.id}">Open batch</a></p>
      </article>
    {/each}
  </section>
{/if}

<style>
  .pending {
    border: 1px solid #f2a65a;
    border-radius: 0.4rem;
    padding: 0.2rem 0.8rem;
    margin: 0.8rem 0;
  }
  h2 {
    margin: 0.4rem 0;
  }
  .count {
    padding: 0 0.5rem;
    border-radius: 0.6rem;
    background: #f2a65a;
    color: #14110f;
    font-size: 0.85rem;
  }
  .row {
    border-top: 1px solid #3a322b;
  }
  p {
    margin: 0.3rem 0;
  }
  .primary {
    font-weight: 700;
  }
</style>
