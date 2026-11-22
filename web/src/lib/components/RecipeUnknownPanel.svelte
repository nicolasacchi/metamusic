<script lang="ts">
  // Replaces the Recipe and Lyrics panels of a reference track (M2c C64), and offers to recover a recipe from it (M5b C347.5):
  // the listening check is analysed first (Analyze, through the estimate), then "Create draft from transcript" makes a draft whose
  // lyrics are what was heard. Creating the draft spends nothing; rendering it goes through the ordinary estimate and approval.
  import { api } from "../../api/client";
  import type { Res } from "../crateApi";
  import { recoverRecipe, recoverStage, type RecoverResult } from "../dictionApi";
  import { can } from "../me.svelte";
  import AnalyzeButton from "./AnalyzeButton.svelte";

  interface Props {
    /** The stub recipe's title. */
    title: string;
    lossy: boolean;
    masterBytes: number;
    durationS: number;
    /** The reference's id; without it only the explanation is shown (unit tests of the old panel). */
    trackId?: string;
    /** The Ears dossier (the page's), null while it loads. */
    dossier?: { tier: string; flags: readonly { code: string }[] } | null;
    /** Client-router navigation. */
    go?: (to: string) => void;
  }
  let { title, lossy, masterBytes, durationS, trackId = "", dossier = null, go = () => {} }: Props = $props();
  const kbps = $derived(durationS > 0 ? Math.round((masterBytes * 8) / durationS / 1000) : 0);

  // state of the stored check, asked only when the dossier says one exists (the operation answers 409 otherwise)
  let checkState = $state<string | null>(null);
  $effect(() => {
    const mine = trackId;
    const d = dossier;
    checkState = null;
    if (!mine || !d) return;
    if (!(d.tier === "B" || d.flags.some((f) => f.code === "listening_check_failed"))) {
      checkState = "";
      return;
    }
    let live = true;
    void (async () => {
      try {
        const r = await api.GET("/api/tracks/{id}/tier-b", { params: { path: { id: mine } } });
        if (!live) return;
        const s = r.data?.state ?? (r.error?.details as { state?: unknown } | undefined)?.state;
        checkState = typeof s === "string" ? s : "none";
      } catch {
        if (live) checkState = "error";
      }
    })();
    return () => {
      live = false;
    };
  });

  const stage = $derived(trackId ? recoverStage(dossier, checkState) : "wait");
  const canRecover = $derived(can("recover_recipe"));

  let style = $state("");
  let busy = $state(false);
  let message = $state("");
  let retry: (() => Promise<Res<RecoverResult>>) | null = null;
  let styleAtClick = "";

  async function create() {
    if (busy || !trackId) return;
    busy = true;
    message = "";
    // one key per click: a retry of the same click replays; a changed style is a new action
    if (!retry || styleAtClick !== style) {
      retry = recoverRecipe(api, trackId, { style });
      styleAtClick = style;
    }
    const r = await retry();
    busy = false;
    if (r.ok) {
      retry = null;
      go(`/backlog/${encodeURIComponent(r.value.draft.id)}`);
    } else {
      message = r.message;
      // only a network failure keeps the key: the same click may have reached the server
      if (r.code !== "NETWORK") retry = null;
    }
  }
</script>

<section class="card" data-testid="recipe-unknown">
  <h2>Recipe unknown</h2>
  <p>No recipe exists for this track. It was imported as a reference take: it can be listened to, rated, commented and compared, but it cannot be re-rendered or used as a mother.</p>
  <p class="muted">Stub title: {title}</p>
  {#if lossy}
    <p class="muted" data-testid="lossy-info">Lossy MP3 master, {kbps} kbps. There is no lossless master.</p>
  {/if}

  {#if trackId && canRecover}
    <div class="recover" data-testid="recover">
      <h3>Recover a recipe</h3>
      {#if stage === "wait"}
        <p class="muted">Checking what is known about this take…</p>
      {:else if stage === "analyze"}
        <p class="muted" data-testid="recover-needs-check">Recovering writes a draft from what the recogniser hears in the vocals, so it needs a listening check first.</p>
        <AnalyzeButton {trackId} {go} />
      {:else if stage === "unavailable"}
        <p class="muted" role="status" data-testid="recover-unavailable">The listening check of this take holds no transcript, so there is nothing to recover from.</p>
      {:else}
        <p class="muted">The draft's lyrics are the transcript, split into sections, with a placeholder style from the measured tempo and key unless you write one. It costs nothing; rendering it asks for the usual estimate and approval. The recogniser can mishear sung words: check the lyrics before you render.</p>
        <label>Style (optional)
          <textarea rows="2" maxlength="4000" bind:value={style} placeholder="italian vocals, warm, 100 BPM" data-testid="recover-style"></textarea>
        </label>
        <button type="button" disabled={busy} onclick={create} data-testid="recover-button">{busy ? "Creating…" : "Create draft from transcript"}</button>
      {/if}
      {#if message}<p role="alert" data-testid="recover-error">{message}</p>{/if}
    </div>
  {/if}
</section>

<style>
  .card {
    border: 1px solid #3a322b;
    border-radius: 0.5rem;
    padding: 0.5rem 0.9rem;
  }
  h2 {
    margin: 0 0 0.4rem;
    font-size: 1rem;
  }
  h3 {
    margin: 0.6rem 0 0.3rem;
    font-size: 0.95rem;
  }
  p {
    margin: 0.3rem 0;
  }
  .recover {
    border-top: 1px solid var(--line);
    margin-top: 0.6rem;
  }
  label {
    display: grid;
    gap: 0.2rem;
    font-size: 0.85rem;
    margin: 0.4rem 0;
  }
  textarea {
    width: 100%;
    box-sizing: border-box;
  }
  button {
    min-height: 2rem;
  }
  @media (pointer: coarse) {
    button {
      min-height: var(--hit);
    }
  }
</style>
