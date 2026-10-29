<script lang="ts">
  import { isTapeItem } from "../recordMedia";
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
  let box = $state<HTMLElement | null>(null);
  // Only a tile that is (nearly) on screen asks for its link: a long list must not fetch every cover at once.
  let seen = $state(typeof IntersectionObserver === "undefined");

  $effect(() => {
    if (seen || !box) return;
    const io = new IntersectionObserver(
      (es) => {
        if (es.some((e) => e.isIntersecting)) seen = true;
      },
      { rootMargin: "200px" },
    );
    io.observe(box);
    return () => io.disconnect();
  });

  $effect(() => {
    const id = trackId;
    src = null;
    failed = false;
    if (!seen || isTapeItem(id)) return;
    void covers.get(id).then((c) => {
      if (id === trackId) src = c?.url512 ?? null;
    });
  });
</script>

{#if src && !failed}
  <img bind:this={box} class="cover" data-testid="cover-art" {src} alt={title} width={size} height={size} onerror={() => (failed = true)} />
{:else}
  <div bind:this={box} class="cover ph" data-testid="cover-placeholder" style:width="{size}px" style:height="{size}px" aria-hidden="true"></div>
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
