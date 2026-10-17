<script lang="ts">
  import type { components } from "../../api/schema";
  import { canCancel, stateLabel, stateTone, type Viewer } from "../jobState";
  import { formatMicroUSD } from "../money";
  import { player } from "../player.svelte";
  import PlayIcon from "./icons/PlayIcon.svelte";

  type Job = components["schemas"]["JobSummary"];
  interface Props {
    job: Job;
    viewer: Viewer;
    busy?: boolean;
    onterminate: (id: string) => void;
    /** Plays a finished take; default: the take alone. A batch page passes its own queue. */
    onplay?: (trackId: string) => void;
    /** false on the batch's own page, where the link would point at itself. */
    batchLink?: boolean;
  }
  let { job, viewer, busy = false, onterminate, onplay, batchLink = true }: Props = $props();
  const playable = $derived(job.state === "completed" && job.track_id !== null);
  function play() {
    const id = job.track_id;
    if (!id) return;
    if (onplay) onplay(id);
    else player.play(id, [id], { title: job.title });
  }
  const amount = $derived(job.cost.display_micro_usd ?? job.cost.expected_micro_usd);
</script>

<tr data-testid="job-row" data-state={job.state}>
  <td><a href="/jobs/{job.id}">{job.title || job.id}</a></td>
  <td><span class="chip {stateTone(job.state)}" data-testid="job-state">{stateLabel(job.state)}</span>{#if job.error_code} <code>{job.error_code}</code>{/if}</td>
  <td data-testid="job-cost">{formatMicroUSD(amount)} <span class="muted">{job.cost.basis}</span></td>
  <td>{#if batchLink}<a href="/batches/{job.batch_id}">batch</a>{/if}</td>
  <td>
    {#if playable}
      <button type="button" class="play" onclick={play} aria-label="Play {job.title || 'take'}" data-testid="job-play"><PlayIcon size={18} /> Play</button>
      <a href="/tracks/{job.track_id}">Open take</a>
    {/if}
    {#if canCancel(job, viewer)}
      <button type="button" disabled={busy} onclick={() => onterminate(job.id)} data-testid="terminate-job">Terminate</button>
    {/if}
  </td>
</tr>

<style>
  td {
    padding: 0.25rem 0.6rem 0.25rem 0;
  }
  .play {
    display: inline-flex;
    align-items: center;
    gap: 0.3rem;
    min-height: var(--hit);
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
