<script lang="ts">
  import { api } from "../api/client";
  import type { components } from "../api/schema";
  import JobTimeline from "../lib/components/JobTimeline.svelte";
  import { pushToast } from "../lib/judge.svelte";
  import { canCancel, followJob, stateLabel, type Viewer } from "../lib/jobState";
  import { formatMicroUSD } from "../lib/money";

  type Job = components["schemas"]["Job"];
  interface Props {
    id: string;
  }
  let { id }: Props = $props();
  const viewer: Viewer = { kind: "human" };
  let job = $state<Job | null>(null);
  let error = $state("");
  let busy = $state(false);

  // Long-polls wait_for_job (25 s) while mounted and not terminal; the cleanup flips `active`.
  $effect(() => {
    let active = true;
    void (async () => {
      try {
        const r = await api.GET("/api/jobs/{id}", { params: { path: { id } } });
        if (!active) return;
        if (!r.data) return void (error = r.error?.message ?? "Could not load the job");
        job = r.data;
        await followJob({
          initial: r.data,
          active: () => active,
          onJob: (j) => (job = j),
          onError: () => (error = "Lost contact with the server"),
          wait: async (since) => {
            try {
              const w = await api.GET("/api/jobs/{id}/wait", { params: { path: { id }, query: { since_state: since, timeout_s: 25 } } });
              return w.data?.job ?? null;
            } catch {
              return null;
            }
          },
        });
      } catch {
        if (active) error = "Could not load the job";
      }
    })();
    return () => {
      active = false;
    };
  });

  async function terminate() {
    if (busy || !job) return;
    busy = true;
    try {
      const r = await api.POST("/api/jobs/{id}/cancel", { params: { path: { id } }, body: {} });
      if (r.data) job = r.data;
      else pushToast(r.error?.message ?? "Could not terminate the job");
    } catch {
      pushToast("Could not terminate the job");
    } finally {
      busy = false;
    }
  }
</script>

<h1>Job</h1>
{#if error}<p role="alert">{error}</p>{/if}
{#if job}
  <p>
    <b data-testid="job-state">{stateLabel(job.state)}</b>
    {#if job.error_code}<code>{job.error_code}</code> {job.error_message ?? ""}{/if}
    {#if job.outcome === "truncated"}<span class="muted"> · truncated</span>{/if}
  </p>
  <p>
    {job.title || job.id} · take {job.take_index + 1} · seed {job.seed} · <a href="/batches/{job.batch_id}">batch</a>
    {#if job.track_id} · <a href="/track/{job.track_id}">track</a>{/if}
    {#if job.parent_track_id} · parent <a href="/track/{job.parent_track_id}">track</a>{/if}
  </p>
  <p data-testid="job-cost">
    Cost {formatMicroUSD(job.cost.display_micro_usd ?? job.cost.expected_micro_usd)} <span class="muted">({job.cost.basis})</span>
    · expected {job.expected_audio_s} s of audio{#if job.audio_s !== null}, got {job.audio_s.toFixed(1)} s{/if}
  </p>
  {#if job.timings.cold_start_s !== null || job.timings.execution_ms !== null}
    <p class="muted">
      {#if job.timings.cold_start_s !== null}cold start {job.timings.cold_start_s} s{/if}
      {#if job.timings.execution_ms !== null} · run {Math.round(job.timings.execution_ms / 1000)} s{/if}
    </p>
  {/if}
  {#if canCancel(job, viewer)}
    <p><button type="button" disabled={busy} onclick={terminate} data-testid="terminate-job">Terminate</button></p>
  {/if}
  <h2>Timeline</h2>
  <JobTimeline events={job.events} />
{:else if !error}
  <p class="muted">Loading…</p>
{/if}
