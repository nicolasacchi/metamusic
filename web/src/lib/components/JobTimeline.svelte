<script lang="ts">
  import type { components } from "../../api/schema";

  type Ev = components["schemas"]["JobEvent"];
  interface Props {
    events: Ev[];
  }
  let { events }: Props = $props();
</script>

{#if events.length === 0}
  <p class="muted">No events yet.</p>
{:else}
  <ol data-testid="job-timeline">
    {#each events as e, i (i)}
      <li>
        <time datetime={e.at}>{e.at}</time>
        {e.event}{#if e.from || e.to}: {e.from ?? "—"} → {e.to ?? "—"}{/if}{#if e.code} <code>{e.code}</code>{/if}
      </li>
    {/each}
  </ol>
{/if}
