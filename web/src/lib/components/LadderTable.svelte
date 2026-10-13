<script lang="ts">
  import type { components } from "../../api/schema";
  import { ladderFooter, ladderRows } from "../ladderView";

  interface Props {
    page: components["schemas"]["LadderPage"];
  }
  let { page }: Props = $props();
  const W = 320;
  const model = $derived(ladderRows(page, W));
  const footer = $derived(ladderFooter(page));
</script>

<table data-testid="ladder-table">
  <thead>
    <tr><th>#</th><th>Track</th><th>u ± 1.96 sd</th><th class="scale">Scale (reject line at centre)</th><th>W-L-T-BB</th></tr>
  </thead>
  <tbody>
    {#each model.rows as r (r.trackId)}
      <tr class:prov={r.provisional}>
        <td>{r.rank}</td>
        <td><a href={r.href}>{r.title}</a> <span class="muted">seed {r.seed}</span>{#if r.provisional} <span class="chip">provisional</span>{/if}</td>
        <td class="num">{r.u}</td>
        <td class="scale">
          <svg viewBox="0 0 {W} 16" width="100%" height="16" role="img" aria-label="Uncertainty of {r.title}">
            <line x1={model.zeroX} x2={model.zeroX} y1="0" y2="16" stroke="#e5484d" stroke-width="1" />
            <line x1={r.lo} x2={r.hi} y1="8" y2="8" stroke="#f2a65a" stroke-width="2" />
            <circle cx={r.x} cy="8" r="4" fill="#f2a65a" />
          </svg>
        </td>
        <td class="num">{r.record}</td>
      </tr>
    {/each}
  </tbody>
</table>
<footer class="foot" data-testid="ladder-footer">
  <span>ν {footer.nu}</span>
  <span>{footer.iterations} iterations{footer.converged ? ", converged" : ", not converged"}</span>
  <span>{footer.counts}</span>
  {#if footer.slotAWinRate}<span>slot A wins {footer.slotAWinRate}</span>{/if}
  {#if footer.warning}<span class="chip warn" role="status">{footer.warning}</span>{/if}
</footer>

<style>
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th,
  td {
    text-align: left;
    padding: 0.25rem 0.5rem;
  }
  .num {
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
  .scale {
    width: 40%;
    min-width: 8rem;
  }
  tr.prov {
    opacity: 0.7;
  }
  .chip {
    font-size: 0.75rem;
    padding: 0 0.5rem;
    border-radius: 0.6rem;
    background: #3a322b;
  }
  .warn {
    background: #5a4a22;
  }
  .foot {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem 1.2rem;
    margin-top: 0.6rem;
    font-size: 0.85rem;
  }
</style>
