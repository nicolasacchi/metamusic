<script lang="ts">
  import { api } from "../../api/client";
  import { copyPrompt, type PromptName } from "../promptButtons";

  interface Props {
    trackId: string;
    /** write_variant refuses a track whose recipe is unknown (RECIPE_UNKNOWN). */
    recipeKnown?: boolean;
  }
  let { trackId, recipeKnown = true }: Props = $props();

  let busy = $state(false);
  let chars = $state<number | null>(null);
  let copied = $state(false);
  let error = $state("");

  async function copy(name: PromptName) {
    if (busy) return;
    busy = true;
    error = "";
    const r = await copyPrompt(api, name, trackId, typeof navigator === "undefined" ? undefined : navigator.clipboard);
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

<div class="prompts">
  <button type="button" data-testid="copy-critique-prompt" disabled={busy} onclick={() => copy("critique_track")}>Copy critique prompt</button>
  <button type="button" data-testid="copy-variant-prompt" disabled={busy || !recipeKnown} title={recipeKnown ? "" : "The recipe of this track is unknown"} onclick={() => copy("write_variant")}>Copy variant prompt</button>
  {#if chars !== null}
    <span class="muted" data-testid="prompt-chars" data-chars={chars} role="status">{chars} chars{copied ? " copied" : " (clipboard unavailable: not copied)"}</span>
  {/if}
  {#if error}<span role="alert">{error}</span>{/if}
</div>

<style>
  .prompts {
    display: flex;
    gap: 0.6rem;
    align-items: center;
    flex-wrap: wrap;
  }
</style>
