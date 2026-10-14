<script lang="ts">
  import type { components } from "../../api/schema";
  import { canCancel, stateLabel, stateTone, type Viewer } from "../jobState";
  import { formatMicroUSD } from "../money";

  type Job = components["schemas"]["JobSummary"];
  interface Props {
    job: Job;
    viewer: Viewer;
    busy?: boolean;
    onterminate: (id: string) => void;
  }
  let { job, viewer, busy = false, onterminate }: Props = $props();
  const amount = $derived(job.cost.display_micro_usd ?? job.cost.expected_micro_usd);
</script>

<tr data-testid="job-row" data-state={job.state}>
  <td><a href="/jobs/{job.id}">{job.title || job.id}</a></td>
  <td><span class="chip {stateTone(job.state)}" data-testid="job-state">{stateLabel(job.state)}</span>{#if job.error_code} <code>{job.error_code}</code>{/if}</td>
  <td data-testid="job-cost">{formatMicroUSD(amount)} <span class="muted">{job.cost.basis}</span></td>
  <td><a href="/batches/{job.batch_id}">batch</a></td>
  <td>
    {#if canCancel(job, viewer)}
      <button type="button" disabled={busy} onclick={() => onterminate(job.id)} data-testid="terminate-job">Terminate</button>
    {/if}
  </td>
</tr>

<style>
  td {
    padding: 0.25rem 0.6rem 0.25rem 0;
  }
  .chip {
    padding: 0 0.5rem;
    border-radius: 0.6rem;
    background: #3a322b;
    font-size: 0.85rem;
  }
  .ok {
    background: #2a4a2c;
  }
  .bad {
    background: #5a2a22;
  }
  .warn {
    background: #5a4a22;
  }
  .busy {
    background: #8a6a45;
  }
</style>
