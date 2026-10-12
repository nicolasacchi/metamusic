<script lang="ts">
  import type { components } from "../../api/schema";

  type Analysis = components["schemas"]["AnalysisInfo"];
  type Truncated = components["schemas"]["TrackDetail"]["truncated"];
  interface Props {
    analysis: Analysis;
    truncated: Truncated;
  }
  let { analysis, truncated }: Props = $props();
  const l = $derived(analysis.loudness);
  const bw = $derived(analysis.spectrum?.bandwidth_hz ?? null);
  const cut = $derived(truncated !== null && (truncated.abc === true || truncated.semantic === true));
  const dash = (v: number | null | undefined, unit: string, d = 1) => (v === null || v === undefined ? "—" : `${v.toFixed(d)} ${unit}`);
</script>

<section class="card" aria-label="Metrics" data-testid="metrics">
  <h2>Metrics</h2>
  {#if cut}
    <p class="badge bad" data-testid="truncated-badge">
      Truncated: {[truncated?.abc ? "score stream" : "", truncated?.semantic ? "audio stream" : ""].filter(Boolean).join(" and ")}
    </p>
  {/if}
  <dl>
    <dt>Integrated loudness</dt>
    <dd>{dash(l?.integrated_lufs, "LUFS")}</dd>
    <dt>True peak</dt>
    <dd>{dash(l?.true_peak_dbtp, "dBTP")}</dd>
    <dt>Loudness range</dt>
    <dd>{dash(l?.lra_lu, "LU")}</dd>
    <dt>ReplayGain</dt>
    <dd>{dash(l?.replaygain_track_gain_db, "dB", 2)}</dd>
    <dt>Bandwidth</dt>
    <dd>{bw === null ? "—" : `${(bw / 1000).toFixed(1)} kHz`}</dd>
  </dl>
</section>

<style>
  .card {
    border: 1px solid #3a322b;
    border-radius: 0.5rem;
    padding: 0.5rem 0.9rem;
  }
  h2 {
    margin: 0 0 0.4rem;
    font-size: 1rem;
  }
  dl {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 0.2rem 1rem;
    margin: 0;
  }
  dt {
    opacity: 0.7;
  }
  dd {
    margin: 0;
    font-variant-numeric: tabular-nums;
  }
  .badge.bad {
    display: inline-block;
    background: #a02a1f;
    color: #fff;
    padding: 0.1rem 0.6rem;
    border-radius: 0.6rem;
    margin: 0 0 0.5rem;
  }
</style>
