<script lang="ts">
  import type { components } from "../../api/schema";

  type Proposal = components["schemas"]["PickProposal"];
  interface Props {
    proposals: Proposal[];
    /** Accept and Dismiss are operator actions (403 HUMAN_ONLY for agents). */
    canDecide?: boolean;
    titleOf?: (takeId: string) => string;
    onaccept?: (p: Proposal) => void;
    ondismiss?: (p: Proposal) => void;
  }
  let { proposals, canDecide = false, titleOf = (id) => id, onaccept, ondismiss }: Props = $props();
</script>

{#if proposals.length > 0}
  <ul class="proposals" data-testid="proposal-list">
    {#each proposals as p (p.id)}
      <li data-testid="proposal">
        <div>
          <strong>{p.proposed_by.name ?? p.proposed_by.id}</strong> proposes <a href="/tracks/{encodeURIComponent(p.take_id)}">{titleOf(p.take_id)}</a>
          <blockquote>{p.rationale}</blockquote>
        </div>
        {#if canDecide}
          <div class="acts">
            <button type="button" data-testid="proposal-accept" onclick={() => onaccept?.(p)}>Accept</button>
            <button type="button" data-testid="proposal-dismiss" onclick={() => ondismiss?.(p)}>Dismiss</button>
          </div>
        {/if}
      </li>
    {/each}
  </ul>
{/if}

<style>
  .proposals {
    list-style: none;
    padding: 0;
    margin: 0;
    display: grid;
    gap: 0.5rem;
  }
  li {
    display: flex;
    justify-content: space-between;
    gap: 0.5rem;
    border: 1px solid #5a4d43;
    border-radius: 0.4rem;
    padding: 0.4rem 0.6rem;
  }
  blockquote {
    margin: 0.2rem 0 0;
    font-style: italic;
  }
  .acts {
    display: flex;
    gap: 0.4rem;
    align-items: start;
  }
</style>
