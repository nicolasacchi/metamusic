<script lang="ts">
  import { confirmHref } from "../approvals";
  import { can, me } from "../me.svelte";

  let { id }: { id: string } = $props();
</script>

{#if !me.loaded}
  <!-- the role is not known yet: show neither note -->
{:else if can("approve_batch")}
  <p class="review" data-testid="review-approve"><a href={confirmHref(id)}><strong>Review &amp; approve</strong></a> <span class="muted">this batch is waiting for your approval; nothing is spent until you decide.</span></p>
{:else}
  <p class="review muted" data-testid="review-waiting">Waiting for the owner to approve this request; nothing runs or is spent until then.</p>
{/if}

<style>
  .review {
    border: 1px solid #f2a65a;
    border-radius: 0.4rem;
    padding: 0.5rem 0.8rem;
    font-size: 1.1rem;
  }
</style>
