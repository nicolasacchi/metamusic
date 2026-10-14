<script lang="ts">
  // How a generated take came to be (C84.5): origin, operator, rationale, cost with its basis, environment.
  import type { components } from "../../api/schema";
  import { formatMicroUSD } from "../money";
  import { openAbWithParent, pushNavigate } from "../abParent";

  type Generation = NonNullable<components["schemas"]["TrackDetail"]["generation"]>;
  interface Props {
    trackId: string;
    generation: Generation;
    parentId: string | null;
    truncated: components["schemas"]["TrackDetail"]["truncated"];
    navigate?: (to: string) => void;
  }
  let { trackId, generation, parentId, truncated, navigate }: Props = $props();

  const basisLabel = { estimate: "estimated", provisional: "provisional", reconciled: "reconciled" } as const;
  const isTruncated = $derived(!!truncated && (truncated.abc === true || truncated.semantic === true));
  const env = $derived(generation.env);
  let busy = $state(false);
  let error = $state("");

  async function ab() {
    if (!parentId || busy) return;
    busy = true;
    error = await openAbWithParent(parentId, trackId, navigate ?? pushNavigate);
    busy = false;
  }
</script>

<section class="card" data-testid="provenance">
  <h2>Provenance</h2>
  <dl>
    <dt>Operator</dt>
    <dd data-testid="prov-operator"><span class="chip">{generation.operator.replaceAll("_", " ")}</span></dd>
    {#if generation.rationale}
      <dt>Rationale</dt>
      <dd data-testid="prov-rationale">{generation.rationale}</dd>
    {/if}
    {#if generation.cost}
      <dt>Cost</dt>
      <dd data-testid="prov-cost">{formatMicroUSD(generation.cost.display_micro_usd)} <span class="muted">({basisLabel[generation.cost.basis]})</span></dd>
    {/if}
    {#if env}
      <dt>Environment</dt>
      <dd data-testid="prov-env" class="muted">
        {env.engine} · {env.backend}{env.gpu_class ? ` · ${env.gpu_class}` : ""} · build {env.code_commit.slice(0, 7)}
      </dd>
    {/if}
  </dl>
  {#if isTruncated}<p><span class="chip warn" data-testid="prov-truncated">Truncated generation</span></p>{/if}
  <p><a href="/jobs/{generation.job_id}">Job</a> · <a href="/batches/{generation.batch_id}">Batch</a> · <a href="/lineage/{trackId}">Lineage</a></p>
  {#if parentId}
    <div class="actions">
      <button type="button" onclick={() => void ab()} disabled={busy} data-testid="prov-ab-parent">A/B with parent</button>
      <a href="/compare?a={parentId}&b={trackId}" data-testid="prov-compare">Compare</a>
    </div>
  {/if}
  {#if error}<p role="alert">{error}</p>{/if}
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
    grid-template-columns: max-content 1fr;
    gap: 0.2rem 0.8rem;
    margin: 0;
  }
  dt {
    color: #a89c90;
  }
  dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .chip {
    font-size: 0.75rem;
    padding: 0 0.5rem;
    border-radius: 0.6rem;
    background: #3a322b;
  }
  .warn {
    border: 1px solid #e0a030;
    color: #e0a030;
    background: transparent;
  }
  .actions {
    display: flex;
    gap: 0.5rem;
    align-items: center;
  }
</style>
