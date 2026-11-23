<script lang="ts">
  import { bulkOptionLine, type BulkOffer } from "../bulkView";

  interface Props {
    offer: BulkOffer;
    /** The intent is priced on a pod backend now. */
    on: boolean;
    onchange: (on: boolean) => void;
  }
  let { offer, on, onchange }: Props = $props();
</script>

{#if offer.kind !== "hidden" || on}
  <section class="bulk" data-testid="bulk-option" data-kind={offer.kind}>
    <h2>Bulk pod batch</h2>
    {#if offer.kind === "ready"}
      <label>
        <input type="checkbox" checked={on} onchange={(ev) => onchange(ev.currentTarget.checked)} data-testid="bulk-toggle" />
        Run as one bulk pod batch
      </label>
      <p class="muted">
        One rented GPU pod renders the whole batch and shuts itself down. The estimate below then shows the pod's start-up and idle time as part of the price. It is
        still an ordinary estimate: nothing runs or is spent until you confirm it.
      </p>
      {#if offer.note}<p class="warn" data-testid="bulk-note">{offer.note}</p>{/if}
    {:else}
      {#if offer.text}<p data-testid="bulk-off" data-kind={offer.kind}>{offer.text}</p>{:else if bulkOptionLine(offer, on)}<p class="muted">{bulkOptionLine(offer, on)}</p>{/if}
      {#if on}
        <p><button type="button" onclick={() => onchange(false)} data-testid="bulk-clear">Use the ordinary backend instead</button></p>
      {/if}
    {/if}
  </section>
{/if}

<style>
  .bulk {
    border: 1px solid #3a3f4b;
    border-radius: 6px;
    padding: 0.4rem 0.8rem;
    margin: 0.8rem 0;
  }
  .bulk h2 {
    font-size: 1rem;
    margin: 0.2rem 0;
  }
  .bulk label {
    display: inline-flex;
    gap: 0.5rem;
    align-items: center;
    min-height: var(--hit);
  }
  .warn {
    color: #f2a65a;
  }
</style>
