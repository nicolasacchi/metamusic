<script lang="ts">
  // Shown while a confirmation runs in the second tab (api/stepup.ts). The music keeps playing in this tab.
  import { stepUp } from "../../api/stepup";
</script>

{#if $stepUp.waiting}
  <div class="stepup" role="status" data-testid="stepup-banner">
    <span>Confirm in the other tab, then come back here: the action continues by itself.</span>
    <button type="button" onclick={() => void stepUp.check()} data-testid="stepup-continue">I confirmed</button>
    <button type="button" onclick={() => stepUp.reopen()} data-testid="stepup-reopen">Open it again</button>
    <button type="button" onclick={() => stepUp.cancel()} data-testid="stepup-cancel">Cancel</button>
  </div>
{/if}

<style>
  .stepup {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem;
    margin: 0;
    padding: 0.5rem 1rem;
    background: #3a322b;
  }
  .stepup span {
    flex: 1 1 14rem;
  }
</style>
