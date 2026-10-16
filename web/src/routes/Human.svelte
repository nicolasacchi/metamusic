<script lang="ts">
  import { api } from "../api/client";
  import PendingApprovals from "../lib/components/PendingApprovals.svelte";
  import { refreshApprovals } from "../lib/approvals.svelte";
  import { ageLabel, confirmHref, loadRecentDecisions, type PendingBatch } from "../lib/approvals";
  import { formatMicroUSDCeil } from "../lib/money";

  let decisions = $state<PendingBatch[] | null>(null);
  let now = $state(Date.now());

  async function load() {
    void refreshApprovals();
    const d = await loadRecentDecisions({ GET: (path, init) => api.GET(path, init) });
    if (d) decisions = d;
  }
  $effect(() => {
    void load();
    const t = setInterval(() => {
      now = Date.now();
      if (!document.hidden) void load();
    }, 30_000);
    return () => clearInterval(t);
  });

  const cards: { href: string; title: string; text: string }[] = [
    { href: "/human/generate", title: "Generate", text: "Estimate and start a batch of takes." },
    { href: "/human/spend", title: "Spend caps", text: "Budgets, auto-approve limits, arming." },
    { href: "/human/tokens", title: "Tokens", text: "Agent tokens and their scopes." },
    { href: "/human/devices", title: "Devices", text: "Paired phones and pairing codes." },
  ];
</script>

<h1>Human</h1>
<PendingApprovals showEmpty />

<section aria-label="Recent decisions" data-testid="decisions">
  <h2>Recent decisions</h2>
  {#if decisions === null}
    <p class="muted">Loading…</p>
  {:else if decisions.length === 0}
    <p class="muted">No decisions yet.</p>
  {:else}
    <ul>
      {#each decisions as b (b.id)}
        <li data-testid="decision-row" data-batch={b.id}>
          <b>{b.approval.state}</b>
          <a href="/batches/{b.id}">{b.takes} take{b.takes === 1 ? "" : "s"}</a>
          by {b.created_by.name}, up to {formatMicroUSDCeil(b.cost.ceiling_micro_usd)}
          <span class="muted">· {ageLabel(b.approval.decided_at ?? b.created_at, now)}{#if b.approval.reason} · {b.approval.reason}{/if}</span>
          <a class="muted" href={confirmHref(b.id)}>details</a>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<section aria-label="Human pages">
  <h2>Pages</h2>
  <div class="cards">
    {#each cards as c (c.href)}
      <a class="card" href={c.href} data-testid="human-card"><strong>{c.title}</strong><span class="muted">{c.text}</span></a>
    {/each}
  </div>
</section>

<style>
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr));
    gap: 0.6rem;
  }
  .card {
    display: grid;
    gap: 0.2rem;
    border: 1px solid #3a322b;
    border-radius: 0.4rem;
    padding: 0.7rem 0.9rem;
    text-decoration: none;
  }
  ul {
    padding-left: 1.1rem;
  }
  li {
    margin: 0.3rem 0;
  }
</style>
