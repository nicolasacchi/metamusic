<script lang="ts">
  // Receipts (C262, D-M5-R3-5): activity grouped by who did it, with the money their batches settled. Web-only, read-only.
  import { api } from "../../api/client";
  import { loadReceipts, type ReceiptsData } from "../receipts";
  import { formatMicroUSD } from "../money";

  let data = $state<ReceiptsData | null>(null);
  let loading = $state(true);
  let error = $state("");
  let unavailable = $state(false);

  async function load() {
    loading = true;
    error = "";
    try {
      const r = await loadReceipts(api);
      if (r.ok) data = r.value;
      else if (r.status === 501 || r.status === 503) unavailable = true;
      else error = r.message;
    } catch {
      error = "Could not load the receipts";
    }
    loading = false;
  }
  $effect(() => {
    void load();
  });

  const when = (iso: string) => iso.slice(0, 16).replace("T", " ") + " UTC";
  const who = (p: { kind: string; name: string }) => (p.name && p.name !== p.kind ? `${p.kind} · ${p.name}` : p.kind);
</script>

<section data-testid="receipts-table" aria-busy={loading}>
  {#if loading}
    <p class="muted">Loading…</p>
  {:else if unavailable}
    <p class="muted" data-testid="receipts-unavailable">Receipts are not available here.</p>
  {:else if error}
    <p role="alert">{error} <button type="button" onclick={() => void load()}>Retry</button></p>
  {:else if data}
    {#if data.groups.length === 0}
      <p class="muted" data-testid="receipts-empty">No activity yet.</p>
    {/if}
    <ul class="groups">
      {#each data.groups as g (g.key)}
        <li class="group" data-testid="receipt-group" data-principal={g.principal.id}>
          <div class="head">
            <strong>{who(g.principal)}</strong>
            <span class="muted">{when(g.from)}{g.to !== g.from ? ` to ${when(g.to)}` : ""}</span>
          </div>
          <p class="sum">
            {g.entries.length} {g.entries.length === 1 ? "call" : "calls"}
            {#if g.created > 0}· {g.created} created{/if}
            {#if g.failed > 0}· {g.failed} refused{/if}
            {#if !data.moneyMissing}· <span data-testid="receipt-spend">{formatMicroUSD(g.spendMicro)}</span> spent{/if}
          </p>
          {#if g.ops.length > 0}
            <p class="ops muted">{g.ops.map((o) => `${o.op} x${o.count}`).join(", ")}</p>
          {/if}
        </li>
      {/each}
    </ul>
    {#if data.moneyMissing}<p class="muted" data-testid="receipts-money-missing">Spend could not be loaded, so amounts are left out.</p>{/if}
    {#if data.truncated}<p class="muted" data-testid="receipts-truncated">Older activity is not shown.</p>{/if}
  {/if}
</section>

<style>
  .groups {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    gap: var(--sp-2);
  }
  .group {
    padding: var(--sp-3);
    border: 1px solid var(--line-strong);
    border-radius: var(--r-md);
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    gap: var(--sp-1) var(--sp-3);
    justify-content: space-between;
  }
  .sum,
  .ops {
    margin: var(--sp-1) 0 0;
  }
</style>
