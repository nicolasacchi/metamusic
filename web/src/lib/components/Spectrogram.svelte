<script lang="ts">
  import type { components } from "../../api/schema";
  import { jumpTo } from "../jump";
  import { spectroSeek } from "../spectroSeek";

  type Plot = components["schemas"]["SpectrogramPlot"];
  interface Props {
    trackId: string;
    url: string;
    plot: Plot;
    width: number;
    height: number;
  }
  let { trackId, url, plot, width, height }: Props = $props();
  let img: HTMLImageElement | undefined = $state();

  function onclick(e: MouseEvent) {
    if (!img) return;
    const r = img.getBoundingClientRect();
    jumpTo(trackId, spectroSeek(e.clientX - r.left, r.width, img.naturalWidth || width, plot));
  }
</script>

<button type="button" class="spectro" {onclick} aria-label="Spectrogram: click to play from that time" data-testid="spectrogram">
  <img bind:this={img} src={url} {width} {height} alt="Spectrogram with section boundaries" />
</button>

<style>
  .spectro {
    display: block;
    padding: 0;
    border: 0;
    background: none;
    cursor: crosshair;
    width: 100%;
  }
  img {
    width: 100%;
    height: auto;
    display: block;
  }
</style>
