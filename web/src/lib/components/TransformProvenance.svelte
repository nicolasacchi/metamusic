<script lang="ts">
  // Provenance of a transform output on the Take page: the kind and the spec hash prefix. Verify is CLI-only (C214.3).
  import { engineChip, specHashShort } from "../transformView";
  let { transform }: { transform: { kind: string; spec_hash: string; transform_id: string } } = $props();
</script>

<section class="card" data-testid="transform-provenance">
  <h2>Transform</h2>
  <p><span class="chip" data-testid="transform-engine-chip">{engineChip(transform)}</span> <code title="spec hash" data-testid="transform-spec-hash">{specHashShort(transform.spec_hash)}</code></p>
  <p><a href="/transforms">All transforms</a></p>
</section>
