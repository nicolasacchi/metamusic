<script lang="ts">
  import { api } from "../../api/client";
  import { copyContext } from "../exportView";

  interface Props {
    trackId: string;
  }
  let { trackId }: Props = $props();

  let busy = $state(false);
  let chars = $state<number | null>(null);
  let copied = $state(false);
  let error = $state("");

  async function copy() {
    busy = true;
    error = "";
    const r = await copyContext(api, trackId, typeof navigator === "undefined" ? undefined : navigator.clipboard);
    busy = false;
    if (r.ok) {
      chars = r.chars;
      copied = r.copied;
    } else {
      chars = null;
      error = r.message;
    }
  }
</script>

<div class="ctx">
  <button type="button" onclick={copy} disabled={busy} data-testid="copy-context">Copy agent context</button>
  {#if chars !== null}
    <span class="muted" data-testid="context-chars" data-chars={chars} role="status">
      {chars} chars{copied ? " copied" : " (clipboard unavailable: not copied)"}
    </span>
  {/if}
  {#if error}<span role="alert">{error}</span>{/if}
</div>

<style>
  .ctx {
    display: flex;
    gap: 0.6rem;
    align-items: center;
    flex-wrap: wrap;
  }
</style>
