<script lang="ts">
  import { covers } from "../cover";

  interface Props {
    trackId: string;
    /** alt text */
    title: string;
    /** CSS pixel edge; the 512 cover is used at every size. */
    size?: number;
  }
  let { trackId, title, size = 160 }: Props = $props();

  let src = $state<string | null>(null);
  let failed = $state(false);

  $effect(() => {
    const id = trackId;
    src = null;
    failed = false;
    void covers.get(id).then((c) => {
      if (id === trackId) src = c?.url512 ?? null;
    });
  });
</script>

{#if src && !failed}
  <img class="cover" data-testid="cover-art" {src} alt={title} width={size} height={size} onerror={() => (failed = true)} />
{:else}
  <div class="cover ph" data-testid="cover-placeholder" style:width="{size}px" style:height="{size}px" aria-hidden="true"></div>
{/if}

<style>
  .cover {
    display: block;
    flex: none;
    border-radius: 0.4rem;
    object-fit: cover;
    background: #3a322b;
  }
</style>
