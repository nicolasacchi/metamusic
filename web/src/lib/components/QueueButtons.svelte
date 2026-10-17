<script lang="ts">
  // "Play next" and "Add to queue" for one take: the row actions that sit next to a play button everywhere.
  // The player announces the change itself (live region + 2 s toast), so this is just two buttons.
  import { player } from "../player.svelte";
  import PlayNextIcon from "./icons/PlayNextIcon.svelte";
  import QueueAddIcon from "./icons/QueueAddIcon.svelte";

  interface Props {
    id: string;
    title?: string;
    size?: number;
    /** Hide the "play next" button where the row already has one of its own. */
    nextToo?: boolean;
  }
  let { id, title = "this take", size = 18, nextToo = true }: Props = $props();
</script>

{#if nextToo}
  <button type="button" class="qb" aria-label="Play {title} next" title="Play next" data-testid="play-next" onclick={() => player.playNext(id)}><PlayNextIcon {size} /></button>
{/if}
<button type="button" class="qb" aria-label="Add {title} to queue" title="Add to queue" data-testid="add-to-queue" onclick={() => player.enqueue(id)}><QueueAddIcon {size} /></button>

<style>
  .qb {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: var(--hit, 44px);
    min-height: var(--hit, 44px);
    padding: 0;
  }
</style>
