<script lang="ts">
  import type { components } from "../../api/schema";

  interface Props {
    cover: components["schemas"]["CoverLinks"] | null | undefined;
    /** Rendered edge in px. */
    size?: number;
    alt?: string;
  }
  let { cover, size = 64, alt = "" }: Props = $props();
</script>

<span class="cover" style:--edge="{size}px" data-testid="record-cover">
  {#if cover}
    <img src={cover.url_512} srcset="{cover.url_512} 512w, {cover.url_1024} 1024w" sizes="{size}px" {alt} width={size} height={size} loading="lazy" decoding="async" />
  {:else}
    <span class="blank" aria-hidden="true"></span>
  {/if}
</span>

<style>
  .cover {
    display: inline-block;
    flex: none;
    width: var(--edge);
    height: var(--edge);
    border-radius: var(--r-md);
    overflow: hidden;
    background: var(--surface-2);
  }
  img,
  .blank {
    display: block;
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
</style>
